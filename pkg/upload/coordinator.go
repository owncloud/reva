// Copyright 2018-2024 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

// Package upload provides the driver-agnostic upload coordinator:
// TUS session management, postprocessing event loop, lifecycle event
// publishing. Any storage driver can embed the Coordinator to inherit
// the full oCIS upload pipeline.
package upload

import (
	"context"
	"path/filepath"
	"time"

	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/owncloud/reva/v2/pkg/autoprop"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/rhttp/datatx/metrics"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/utils"
)

var tracer trace.Tracer

func init() {
	tracer = otel.Tracer("github.com/owncloud/reva/pkg/upload")
}

var errNotImplemented = tusd.NewError("ERR_NOT_IMPLEMENTED", "use InitiateUpload on the CS3 API to start a new upload", 501)

// Session is the driver-agnostic view of an upload session the Coordinator
// needs. OcisSession satisfies this interface.
type Session interface {
	tusd.Upload
	storage.UploadSession
	ID() string
	Filename() string
	Size() int64
	SizeDiff() int64
	BinPath() string
	SpaceGid() string
	ProviderID() string
	SpaceID() string
	NodeID() string
	NodeExists() bool
	Dir() string
	IsProcessing() bool
	SpaceOwner() *user.UserId
	Executant() user.UserId
	Reference() provider.Reference
	URL(ctx context.Context) (string, error)
	SetScanData(result string, date time.Time)
	Checksums() storage.UploadChecksums
	Metadata() map[string]string
	Persist(ctx context.Context) error
	Finalize(ctx context.Context) error
	Cleanup(revertNodeMetadata, cleanBin, cleanInfo, unmarkPostprocessing bool)
	Context(ctx context.Context) context.Context
	Node(ctx context.Context) (*node.Node, error)
}

// SessionStore abstracts upload-session persistence for the Coordinator.
type SessionStore interface {
	New(ctx context.Context) Session
	Get(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
}

// RevisionReverter is an optional interface a storage driver may implement
// to handle RevertRevision postprocessing events. Decomposedfs implements it.
type RevisionReverter interface {
	RevertUploadRevision(ctx context.Context, id *provider.ResourceId) error
}

// SizePropagator is an optional interface a storage driver may implement to
// propagate a reverted size delta up the directory tree after a failed upload.
// Decomposedfs implements it. Drivers whose tree accounting is server-side do
// not need to implement this.
type SizePropagator interface {
	PropagateRevertedSize(ctx context.Context, id *provider.ResourceId, sizeDiff int64) error
}

// Coordinator owns the upload state machine:
//   - TUS HTTP layer (InitiateUpload, UseIn, GetUpload, ListUploadSessions)
//   - postprocessing event loop (PostprocessingFinished, PostprocessingStepFinished,
//     RestartPostprocessing, CleanUpload, RevertRevision)
//
// It embeds storage.FS so it IS-A storage.FS and can be passed wherever the
// underlying driver is expected. All methods not overridden here delegate to
// the embedded FS.
type Coordinator struct {
	storage.FS
	store    SessionStore
	pub      events.Publisher
	mountID  string
	numConc  int
	conGroup string
	log      *zerolog.Logger
}

// NewCoordinator constructs a Coordinator. Call Start to begin consuming events.
func NewCoordinator(
	fs storage.FS,
	store SessionStore,
	pub events.Publisher,
	mountID string,
	consumerGroup string,
	numConsumers int,
	log *zerolog.Logger,
) *Coordinator {
	if numConsumers <= 0 {
		numConsumers = 1
	}
	return &Coordinator{
		FS:       fs,
		store:    store,
		pub:      pub,
		mountID:  mountID,
		numConc:  numConsumers,
		conGroup: consumerGroup,
		log:      log,
	}
}

// Start subscribes to the event stream and launches numConsumers goroutines
// that process postprocessing events.
func (c *Coordinator) Start(stream events.Consumer) error {
	ch, err := events.Consume(
		stream,
		c.conGroup,
		events.PostprocessingFinished{},
		events.PostprocessingStepFinished{},
		events.RestartPostprocessing{},
		events.CleanUpload{},
		events.RevertRevision{},
	)
	if err != nil {
		return err
	}
	for i := 0; i < c.numConc; i++ {
		go c.postprocessingLoop(ch)
	}
	return nil
}

func (c *Coordinator) postprocessingLoop(ch <-chan events.Event) {
	for event := range ch {
		c.processEvent(context.Background(), event)
	}
}

func (c *Coordinator) processEvent(evCtx context.Context, event events.Event) {
	ctx, span := events.TraceEventConsumerWithTracer(evCtx, tracer, event)
	ctx = autoprop.SetMetaToContext(ctx, event.ExtraInfo)
	defer span.End()

	switch ev := event.Event.(type) {
	case events.PostprocessingFinished:
		c.handlePostprocessingFinished(ctx, ev)
	case events.PostprocessingStepFinished:
		c.handlePostprocessingStepFinished(ctx, ev)
	case events.RestartPostprocessing:
		c.handleRestartPostprocessing(ctx, ev)
	case events.CleanUpload:
		c.handleCleanUpload(ctx, ev)
	case events.RevertRevision:
		c.handleRevertRevision(ctx, ev)
	default:
		c.log.Error().Interface("event", ev).Msg("coordinator: unknown event")
	}
}

func (c *Coordinator) handlePostprocessingFinished(ctx context.Context, ev events.PostprocessingFinished) {
	log := c.log.With().Str("event", "PostprocessingFinished").Str("uploadid", ev.UploadID).Logger()
	if ev.ResourceID != nil && ev.ResourceID.GetStorageId() != "" && ev.ResourceID.GetStorageId() != c.mountID {
		log.Debug().Msg("ignoring event for different storage")
		return
	}
	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get upload")
		return
	}

	ctx = session.Context(ctx)

	n, err := session.Node(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not read node")
		return
	}
	log = c.log.With().Str("spaceid", session.SpaceID()).Str("nodeid", session.NodeID()).Logger()
	if !n.Exists {
		log.Debug().Msg("node no longer exists")
		session.Cleanup(false, true, true, false)
		return
	}

	var (
		failed             bool
		revertNodeMetadata bool
		keepUpload         bool
	)
	unmarkPostprocessing := true

	switch ev.Outcome {
	default:
		log.Error().Str("outcome", string(ev.Outcome)).Msg("unknown postprocessing outcome - aborting")
		fallthrough
	case events.PPOutcomeAbort:
		failed = true
		revertNodeMetadata = true
		keepUpload = true
		metrics.UploadSessionsAborted.Inc()
	case events.PPOutcomeContinue:
		if err := session.Finalize(ctx); err != nil {
			log.Error().Err(err).Msg("could not finalize upload")
			failed = true
			revertNodeMetadata = false
			keepUpload = true
			unmarkPostprocessing = false
		} else {
			metrics.UploadSessionsFinalized.Inc()
		}
	case events.PPOutcomeDelete:
		failed = true
		revertNodeMetadata = true
		metrics.UploadSessionsDeleted.Inc()
	}

	now := time.Now()
	if failed {
		// Propagate the reverted size so parent tree counters stay correct.
		// This is only needed when the session still owns the processing slot
		// (i.e. no later upload has taken over). The check and the propagation
		// are both delegated to the driver via SizePropagator so that external
		// drivers (which do tree accounting server-side) can no-op this.
		latestSession, err := n.ProcessingID(ctx)
		if err != nil {
			log.Error().Err(err).Msg("reading node processingID failed")
		} else if latestSession == session.ID() {
			if sp, ok := c.FS.(SizePropagator); ok {
				if err := sp.PropagateRevertedSize(ctx, session.Reference().ResourceId, -session.SizeDiff()); err != nil {
					log.Error().Err(err).Msg("could not propagate reverted size")
				}
			}
		}
	} else {
		// Finalize writes the blob but does not bump parent tmtime; do it here
		// so etag changes propagate to folder listings after async uploads.
		p, perr := n.Parent(ctx)
		if perr == nil && p != nil {
			_ = p.SetTMTime(ctx, &now)
		}
	}

	session.Cleanup(revertNodeMetadata, !keepUpload, !keepUpload, unmarkPostprocessing)

	var isVersion bool
	if session.NodeExists() {
		info, err := session.GetInfo(ctx)
		if err == nil && info.MetaData["versionsPath"] != "" {
			isVersion = true
		}
	}

	if err := events.Publish(
		ctx,
		c.pub,
		events.UploadReady{
			UploadID:      ev.UploadID,
			Failed:        failed,
			ExecutingUser: ev.ExecutingUser,
			Filename:      ev.Filename,
			FileRef: &provider.Reference{
				ResourceId: &provider.ResourceId{
					StorageId: session.ProviderID(),
					SpaceId:   session.SpaceID(),
					OpaqueId:  session.SpaceID(),
				},
				Path: utils.MakeRelativePath(filepath.Join(session.Dir(), session.Filename())),
			},
			ResourceID: &provider.ResourceId{
				StorageId: session.ProviderID(),
				SpaceId:   session.SpaceID(),
				OpaqueId:  session.NodeID(),
			},
			Timestamp:         utils.TimeToTS(now),
			SpaceOwner:        n.SpaceOwnerOrManager(ctx),
			IsVersion:         isVersion,
			ImpersonatingUser: ev.ImpersonatingUser,
		},
	); err != nil {
		log.Error().Err(err).Msg("Failed to publish UploadReady event")
	}
}

func (c *Coordinator) handleRestartPostprocessing(ctx context.Context, ev events.RestartPostprocessing) {
	log := c.log.With().Str("event", "RestartPostprocessing").Str("uploadid", ev.UploadID).Logger()
	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get upload")
		return
	}
	n, err := session.Node(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not read node")
		return
	}
	log = c.log.With().Str("spaceid", session.SpaceID()).Str("nodeid", session.NodeID()).Logger()
	s, err := session.URL(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not create url")
		return
	}

	metrics.UploadSessionsRestarted.Inc()

	if err := events.Publish(ctx, c.pub, events.BytesReceived{
		UploadID:      session.ID(),
		URL:           s,
		SpaceOwner:    n.SpaceOwnerOrManager(ctx),
		ExecutingUser: &user.User{Id: &user.UserId{OpaqueId: "postprocessing-restart"}},
		ResourceID:    &provider.ResourceId{SpaceId: n.SpaceID, OpaqueId: n.ID},
		Filename:      session.Filename(),
		Filesize:      uint64(session.Size()),
	}); err != nil {
		log.Error().Err(err).Msg("Failed to publish BytesReceived event")
	}
}

func (c *Coordinator) handleCleanUpload(ctx context.Context, ev events.CleanUpload) {
	log := c.log.With().Str("event", "CleanUpload").Str("uploadid", ev.UploadID).Logger()
	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get upload")
		return
	}
	session.Cleanup(true, !ev.KeepUpload, !ev.KeepUpload, true)
}

func (c *Coordinator) handleRevertRevision(ctx context.Context, ev events.RevertRevision) {
	log := c.log.With().Str("event", "RevertRevision").Interface("nodeid", ev.ResourceID).Logger()
	if ev.ResourceID != nil && ev.ResourceID.GetStorageId() != "" && ev.ResourceID.GetStorageId() != c.mountID {
		log.Debug().Msg("ignoring event for different storage")
		return
	}
	rr, ok := c.FS.(RevisionReverter)
	if !ok {
		log.Error().Msg("storage driver does not implement RevisionReverter")
		return
	}
	if err := rr.RevertUploadRevision(ctx, ev.ResourceID); err != nil {
		log.Error().Err(err).Msg("Failed to revert revision")
	}
}

func (c *Coordinator) handlePostprocessingStepFinished(ctx context.Context, ev events.PostprocessingStepFinished) {
	log := c.log.With().Str("event", "PostprocessingStepFinished").Str("uploadid", ev.UploadID).Logger()
	if ev.ResourceID != nil && ev.ResourceID.GetStorageId() != "" && ev.ResourceID.GetStorageId() != c.mountID {
		log.Debug().Msg("ignoring event for different storage")
		return
	}
	if ev.FinishedStep != events.PPStepAntivirus {
		return
	}

	res := ev.Result.(events.VirusscanResult)
	if res.ErrorMsg != "" {
		return
	}
	log = c.log.With().Str("scan_description", res.Description).Bool("infected", res.Infected).Logger()

	if ev.UploadID == "" {
		// on-demand scanning not supported
		return
	}

	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get upload")
		return
	}

	n, err := session.Node(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get node after scan")
		return
	}
	log = c.log.With().Str("spaceid", session.SpaceID()).Str("nodeid", session.NodeID()).Logger()

	session.SetScanData(res.Description, res.Scandate)
	if err := session.Persist(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to persist scan results")
	}

	if err := n.SetScanData(ctx, res.Description, res.Scandate); err != nil {
		log.Error().Err(err).Msg("Failed to set scan results")
		return
	}

	metrics.UploadSessionsScanned.Inc()
}

// InitiateUpload delegates to the underlying storage.FS.
func (c *Coordinator) InitiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error) {
	return c.FS.InitiateUpload(ctx, ref, uploadLength, metadata)
}

// UseIn registers the coordinator as the TUS data store in the composer.
func (c *Coordinator) UseIn(composer *tusd.StoreComposer) {
	composer.UseCore(c)
	composer.UseTerminater(c)
	composer.UseConcater(c)
	composer.UseLengthDeferrer(c)
}

// NewUpload is not supported; uploads are initiated via the CS3 API.
func (c *Coordinator) NewUpload(_ context.Context, _ tusd.FileInfo) (tusd.Upload, error) {
	return nil, errNotImplemented
}

// GetUpload returns the upload session for the given id.
func (c *Coordinator) GetUpload(ctx context.Context, id string) (tusd.Upload, error) {
	return c.store.Get(ctx, id)
}

// ListUploadSessions returns upload sessions matching the given filter.
func (c *Coordinator) ListUploadSessions(ctx context.Context, filter storage.UploadSessionFilter) ([]storage.UploadSession, error) {
	sessions, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := []storage.UploadSession{}
	now := time.Now()
	for _, s := range sessions {
		if filter.ID != nil && *filter.ID != "" && s.ID() != *filter.ID {
			continue
		}
		if filter.Processing != nil && *filter.Processing != s.IsProcessing() {
			continue
		}
		if filter.Expired != nil {
			if *filter.Expired {
				if now.Before(s.Expires()) {
					continue
				}
			} else {
				if now.After(s.Expires()) {
					continue
				}
			}
		}
		if filter.HasVirus != nil {
			sr, _ := s.ScanData()
			infected := sr != ""
			if *filter.HasVirus != infected {
				continue
			}
		}
		result = append(result, s)
	}
	return result, nil
}

// AsTerminatableUpload returns the upload as a TerminatableUpload.
func (c *Coordinator) AsTerminatableUpload(up tusd.Upload) tusd.TerminatableUpload {
	return up.(tusd.TerminatableUpload)
}

// AsLengthDeclarableUpload returns the upload as a LengthDeclarableUpload.
func (c *Coordinator) AsLengthDeclarableUpload(up tusd.Upload) tusd.LengthDeclarableUpload {
	return up.(tusd.LengthDeclarableUpload)
}

// AsConcatableUpload returns the upload as a ConcatableUpload.
func (c *Coordinator) AsConcatableUpload(up tusd.Upload) tusd.ConcatableUpload {
	return up.(tusd.ConcatableUpload)
}
