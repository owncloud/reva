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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/owncloud/reva/v2/pkg/autoprop"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/rhttp/datatx/metrics"
	"github.com/owncloud/reva/v2/pkg/storage"
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
	FinishBytesOnly(ctx context.Context) error
	Cleanup(revertNodeMetadata, cleanBin, cleanInfo, unmarkPostprocessing bool)
	Context(ctx context.Context) context.Context
	// Typed setters used by Coordinator.InitiateUpload to populate a new session
	// without knowing internal storage key names.
	SetStorageValue(key, value string)
	SetMetadata(key, value string)
	SetSize(size int64)
	SetSizeIsDeferred(value bool)
	SetExecutant(u *user.User)
	TouchBin() error
}


// bytesFinisher is implemented by OcisSession to compute checksums without
// touching the node. The coordinator uses it from coordinatedUpload.FinishUpload.
type bytesFinisher interface {
	FinishBytesOnly(ctx context.Context) error
}

// coordinatedUpload wraps a Session so the TUS FinishUpload hook runs the
// coordinator path (checksums + MarkProcessing + BytesReceived) instead of
// the legacy FinishUploadDecomposed path.
type coordinatedUpload struct {
	tusd.Upload
	session Session
	coord   *Coordinator
}

func (u *coordinatedUpload) FinishUpload(ctx context.Context) error {
	if bf, ok := u.session.(bytesFinisher); ok {
		if err := bf.FinishBytesOnly(ctx); err != nil {
			return err
		}
		// Persist checksums to disk so the postprocessing handler can read them
		// from the session store after the BytesReceived event is processed.
		if err := u.session.Persist(ctx); err != nil {
			return err
		}
	}
	ref := u.session.Reference()
	if err := u.coord.FS.MarkProcessing(ctx, &ref, true, u.session.ID()); err != nil {
		// Slot already owned by another session. Clean up this session's bin and
		// info files — they will never reach postprocessing.
		u.session.Cleanup(false, true, true, false)
		return err
	}

	metrics.UploadProcessing.Inc()
	metrics.UploadSessionsBytesReceived.Inc()

	if u.coord.pub != nil && u.session.Size() > 0 {
		s, err := u.session.URL(ctx)
		if err != nil {
			return err
		}
		if err := events.Publish(ctx, u.coord.pub, events.BytesReceived{
			UploadID:   u.session.ID(),
			URL:        s,
			SpaceOwner: u.session.SpaceOwner(),
			ExecutingUser: &user.User{
				Id: &user.UserId{
					Type:     u.session.Executant().Type,
					Idp:      u.session.Executant().Idp,
					OpaqueId: u.session.Executant().OpaqueId,
				},
			},
			ResourceID: &provider.ResourceId{
				StorageId: u.session.ProviderID(),
				SpaceId:   u.session.SpaceID(),
				OpaqueId:  u.session.NodeID(),
			},
			Filename: u.session.Filename(),
			Filesize: uint64(u.session.Size()),
		}); err != nil {
			return err
		}
	}
	return nil
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

	log = c.log.With().Str("spaceid", session.SpaceID()).Str("nodeid", session.NodeID()).Logger()
	ref := session.Reference()
	if _, err := c.FS.GetMD(ctx, &ref, []string{}, []string{}); err != nil {
		log.Debug().Err(err).Msg("node no longer exists or not accessible; cleaning up")
		session.Cleanup(false, true, true, false)
		return
	}

	var (
		failed             bool
		revertNodeMetadata bool
		keepUpload         bool
		retryCommit        bool
	)

	switch ev.Outcome {
	default:
		log.Error().Str("outcome", string(ev.Outcome)).Msg("unknown postprocessing outcome - aborting")
		fallthrough
	case events.PPOutcomeAbort:
		failed = true
		// Only revert node metadata for new files: for overwrites, CommitUpload
		// never ran so the node still holds the previous content — nothing to undo.
		revertNodeMetadata = !session.NodeExists()
		keepUpload = true
		metrics.UploadSessionsAborted.Inc()
	case events.PPOutcomeContinue:
		f, fopenErr := os.Open(session.BinPath())
		if fopenErr != nil {
			log.Error().Err(fopenErr).Msg("could not open staged binary for CommitUpload")
			failed = true
			keepUpload = true
			retryCommit = true
		} else {
			commitRef := session.Reference()
			_, commitErr := c.FS.CommitUpload(ctx, &commitRef, storage.UploadSource{
				Body:      f,
				Length:    session.Size(),
				Metadata:  session.Metadata(),
				Checksums: session.Checksums(),
			})
			if commitErr != nil {
				log.Error().Err(commitErr).Msg("could not commit upload")
				failed = true
				keepUpload = true
				retryCommit = true
			} else {
				metrics.UploadSessionsFinalized.Inc()
			}
		}
	case events.PPOutcomeDelete:
		failed = true
		// Only revert node metadata for new files: for overwrites, CommitUpload
		// never ran so the node still holds the previous content — nothing to undo.
		revertNodeMetadata = !session.NodeExists()
		metrics.UploadSessionsDeleted.Inc()
	}

	now := time.Now()

	// Clean up bin and info files. Node reversion (for aborted new-file uploads)
	// is handled below via the FS interface, not inside session.Cleanup.
	session.Cleanup(false, !keepUpload, !keepUpload, false)

	nodeRef := session.Reference()
	if !retryCommit {
		if err := c.FS.MarkProcessing(ctx, &nodeRef, false, session.ID()); err != nil {
			log.Error().Err(err).Msg("could not unmark processing after postprocessing finished")
		}
		if revertNodeMetadata {
			if _, delErr := c.FS.Delete(ctx, &nodeRef); delErr != nil {
				if _, ok := delErr.(errtypes.NotFound); !ok {
					log.Error().Err(delErr).Msg("could not delete placeholder node on abort")
				}
			}
		}
	}

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
			SpaceOwner:        session.SpaceOwner(),
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
		SpaceOwner:    session.SpaceOwner(),
		ExecutingUser: &user.User{Id: &user.UserId{OpaqueId: "postprocessing-restart"}},
		ResourceID: &provider.ResourceId{
			SpaceId:  session.SpaceID(),
			OpaqueId: session.NodeID(),
		},
		Filename: session.Filename(),
		Filesize: uint64(session.Size()),
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
	ctx = session.Context(ctx)
	session.Cleanup(false, !ev.KeepUpload, !ev.KeepUpload, false)
	nodeRef := session.Reference()
	if err := c.FS.MarkProcessing(ctx, &nodeRef, false, session.ID()); err != nil {
		log.Error().Err(err).Msg("could not unmark processing during CleanUpload")
	}
	if !session.NodeExists() {
		if _, delErr := c.FS.Delete(ctx, &nodeRef); delErr != nil {
			if _, ok := delErr.(errtypes.NotFound); !ok {
				log.Error().Err(delErr).Msg("could not delete placeholder node during CleanUpload")
			}
		}
	}
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
	log = c.log.With().Str("spaceid", session.SpaceID()).Str("nodeid", session.NodeID()).Logger()

	session.SetScanData(res.Description, res.Scandate)
	if err := session.Persist(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to persist scan results")
	}

	ctx = session.Context(ctx)
	ref := session.Reference()
	if err := c.FS.SetArbitraryMetadata(ctx, &ref, &provider.ArbitraryMetadata{
		Metadata: map[string]string{
			"scanstatus": res.Description,
			"scandate":   res.Scandate.Format(time.RFC3339Nano),
		},
	}); err != nil {
		log.Error().Err(err).Msg("Failed to write scan results to node")
	}

	metrics.UploadSessionsScanned.Inc()
}

// InitiateUpload creates a node placeholder via TouchFile and builds an upload
// session. For new files TouchFile creates the node; for overwrites it already
// exists and we skip it. All session fields are populated via typed setters so
// the coordinator has no knowledge of internal storage key names.
func (c *Coordinator) InitiateUpload(ctx context.Context, ref *provider.Reference, uploadLength int64, metadata map[string]string) (map[string]string, error) {
	// Resolve node metadata: determine whether the target exists, get its ID,
	// parent ID, space ID, space owner, and the path for Dir.
	existing, err := c.FS.GetMD(ctx, ref, []string{}, []string{})
	nodeExists := err == nil

	mtime := ""
	if m, ok := metadata["mtime"]; ok && m != "null" {
		mtime = m
	}

	var nodeID, spaceID, parentID, dir, nodeName string
	var spaceOwner *user.UserId

	if nodeExists {
		// Overwrite: node is already there; TouchFile is a no-op for existing nodes.
		nodeID = existing.GetId().GetOpaqueId()
		spaceID = existing.GetId().GetSpaceId()
		parentID = existing.GetParentId().GetOpaqueId()
		dir = filepath.Dir(existing.GetPath())
		nodeName = existing.GetName()
		// SpaceOwner is not on ResourceInfo directly; read from a space listing
		// or fall back to the owner field (which is the node owner, not space owner).
		// The session field is used to populate BytesReceived / UploadReady events.
		spaceOwner = existing.GetOwner()
	} else {
		// New file: create the placeholder node via TouchFile.
		result, tfErr := c.FS.TouchFile(ctx, ref, false, mtime)
		if tfErr != nil {
			return nil, tfErr
		}
		nodeID = result.ResourceID.GetOpaqueId()
		spaceID = result.SpaceID
		spaceOwner = result.SpaceOwner
		// Derive dir and name from the ref path — ref must carry a path for new files.
		dir = filepath.Dir(ref.GetPath())
		nodeName = filepath.Base(ref.GetPath())
		// Parent ResourceId is not returned by TouchFile; derive from GetMD on the parent.
		parentRef := &provider.Reference{
			ResourceId: ref.ResourceId,
			Path:       filepath.Dir(ref.GetPath()),
		}
		if parentInfo, pErr := c.FS.GetMD(ctx, parentRef, []string{}, []string{}); pErr == nil {
			parentID = parentInfo.GetId().GetOpaqueId()
		}
	}

	if nodeName == "" {
		return nil, errtypes.BadRequest("coordinator: missing filename in ref")
	}
	if dir == "" {
		return nil, errtypes.BadRequest("coordinator: could not determine upload directory")
	}

	session := c.store.New(ctx)
	session.SetMetadata("filename", nodeName)
	session.SetStorageValue("NodeName", nodeName)
	session.SetMetadata("dir", dir)
	session.SetStorageValue("Dir", dir)
	session.SetStorageValue("NodeId", nodeID)
	session.SetStorageValue("SpaceRoot", spaceID)
	if nodeExists {
		session.SetStorageValue("NodeExists", "true")
	}
	session.SetStorageValue("NodeParentId", parentID)
	if spaceOwner != nil {
		session.SetStorageValue("SpaceOwnerOrManager", spaceOwner.GetOpaqueId())
	}

	usr := ctxpkg.ContextMustGetUser(ctx)
	session.SetExecutant(usr)

	lockID, _ := ctxpkg.ContextGetLockID(ctx)
	session.SetMetadata("lockid", lockID)

	iid, _ := ctxpkg.ContextGetInitiator(ctx)
	session.SetMetadata("initiatorid", iid)

	session.SetSize(uploadLength)

	var mtimeSet bool
	if metadata != nil {
		session.SetMetadata("providerID", metadata["providerID"])
		if v, ok := metadata["mtime"]; ok && v != "null" {
			session.SetMetadata("mtime", v)
			mtimeSet = true
		}
		if v, ok := metadata["expires"]; ok && v != "null" {
			session.SetMetadata("expires", v)
		}
		if _, ok := metadata["sizedeferred"]; ok {
			session.SetSizeIsDeferred(true)
		}
		if checksum, ok := metadata["checksum"]; ok {
			parts := strings.SplitN(checksum, " ", 2)
			if len(parts) != 2 {
				return nil, errtypes.BadRequest("invalid checksum format. must be '[algorithm] [checksum]'")
			}
			switch parts[0] {
			case "sha1", "md5", "adler32":
				session.SetMetadata("checksum", checksum)
			default:
				return nil, errtypes.BadRequest("unsupported checksum algorithm: " + parts[0])
			}
		}
		if v := metadata["if-match"]; v != "" {
			session.SetMetadata("if-match", v)
		}
		if v := metadata["if-none-match"]; v != "" {
			session.SetMetadata("if-none-match", v)
		}
		if v := metadata["if-unmodified-since"]; v != "" {
			session.SetMetadata("if-unmodified-since", v)
		}
	}

	if !mtimeSet {
		session.SetMetadata("mtime", utils.TimeToOCMtime(time.Now()))
	}

	if err := session.TouchBin(); err != nil {
		return nil, fmt.Errorf("coordinator: could not create bin file: %w", err)
	}
	if err := session.Persist(ctx); err != nil {
		return nil, fmt.Errorf("coordinator: could not persist session: %w", err)
	}

	metrics.UploadSessionsInitiated.Inc()

	if uploadLength == 0 {
		// Zero-length uploads complete immediately: compute checksums on the empty
		// bin, commit, and clean up — no postprocessing needed.
		if err := session.FinishBytesOnly(ctx); err != nil {
			session.Cleanup(false, true, true, false)
			return nil, fmt.Errorf("coordinator: zero-length FinishBytesOnly: %w", err)
		}
		commitRef := session.Reference()
		if _, err := c.FS.CommitUpload(ctx, &commitRef, storage.UploadSource{
			Body:      io.NopCloser(bytes.NewReader(nil)),
			Length:    0,
			Metadata:  session.Metadata(),
			Checksums: session.Checksums(),
		}); err != nil {
			session.Cleanup(false, true, true, false)
			return nil, fmt.Errorf("coordinator: zero-length CommitUpload: %w", err)
		}
		session.Cleanup(false, true, true, false)
		metrics.UploadSessionsFinalized.Inc()
		return map[string]string{
			"simple": session.ID(),
			"tus":    session.ID(),
		}, nil
	}

	return map[string]string{
		"simple": session.ID(),
		"tus":    session.ID(),
	}, nil
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

// GetUpload returns the upload session wrapped in a coordinatedUpload so the
// TUS FinishUpload hook runs the coordinator path rather than the legacy one.
func (c *Coordinator) GetUpload(ctx context.Context, id string) (tusd.Upload, error) {
	session, err := c.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &coordinatedUpload{Upload: session, session: session, coord: c}, nil
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
