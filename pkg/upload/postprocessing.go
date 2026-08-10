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

// This file holds the postprocessing result collector: the second half of an
// async upload. finishUpload stages the bytes and publishes BytesReceived; the
// postprocessing service scans them and reports back here, and only then are the
// bytes committed through the driver seam.
//
// It is driver-agnostic on purpose: this logic used to live inside decomposedfs,
// which meant only that driver could offer async uploads.

package upload

import (
	"context"
	"errors"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/zerolog"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/rhttp/datatx/metrics"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// AsyncConf is how a service asks for async uploads: whether they are enabled,
// and the consumer subscription to use if they are.
type AsyncConf struct {
	Enabled       bool
	ConsumerGroup string
	NumConsumers  int
	// MountID is the storage id this provider answers for, used to drop
	// postprocessing events belonging to other storages.
	MountID string
}

// AsyncConfFromDriverConf reads the postprocessing settings off the driver config
// map the services already hand us.
//
// The keys are decomposedfs's (options.go: `asyncfileuploads`, `events`). Reading
// the driver's own keys rather than introducing service-level ones keeps a single
// source of truth: if the coordinator and the driver disagreed, uploads would
// either commit twice or never get scanned.
//
// The consumer group matters most. It is what makes retiring the driver's
// consumer a move rather than an addition: two consumers in one group take turns
// stealing each other's events, two in different groups both act and commit the
// same upload twice.
func AsyncConfFromDriverConf(driverConf map[string]interface{}) AsyncConf {
	if driverConf == nil {
		return AsyncConf{}
	}
	var ac struct {
		AsyncFileUploads bool   `mapstructure:"asyncfileuploads"`
		MountID          string `mapstructure:"mount_id"`
		Events           struct {
			NumConsumers  int    `mapstructure:"numconsumers"`
			ConsumerGroup string `mapstructure:"consumer_group"`
		} `mapstructure:"events"`
	}
	_ = mapstructure.Decode(driverConf, &ac)
	group := ac.Events.ConsumerGroup
	if group == "" {
		// decomposedfs's default (options.go:177). The coordinator takes over the
		// driver's subscription, so it must land in the same group.
		group = "dcfs"
	}
	return AsyncConf{
		Enabled:       ac.AsyncFileUploads,
		ConsumerGroup: group,
		NumConsumers:  ac.Events.NumConsumers,
		MountID:       ac.MountID,
	}
}

// RegisteredEvents are the postprocessing events the coordinator consumes.
var RegisteredEvents = []events.Unmarshaller{
	events.PostprocessingFinished{},
	events.PostprocessingStepFinished{},
	events.RestartPostprocessing{},
	events.CleanUpload{},
}

// RunPostprocessingConsumer subscribes to postprocessing results and switches the
// coordinator over to async uploads: from here on finished uploads stage their
// bytes and wait for a scan verdict instead of committing inline.
//
// The two go together on purpose. Deferring a commit is only safe if something
// will arrive to finish it, so there is no way to enable async without a running
// consumer, and none to run a consumer that never receives work.
//
// mountID is the storage id this provider serves. Postprocessing events are
// broadcast to every provider, so events for other storages must be dropped;
// pass "" only in tests, where a single provider sees a private stream.
//
// numConsumers goroutines share the subscription. Call once, before serving
// requests. Fails without a publisher: nothing would hand uploads to
// postprocessing, so every one of them would wait for a verdict that never comes.
func (c *coordinator) RunPostprocessingConsumer(stream events.Consumer, conf AsyncConf) error {
	if c.pub == nil {
		return errors.New("coordinator: async uploads need an event publisher")
	}
	ch, err := events.Consume(stream, conf.ConsumerGroup, RegisteredEvents...)
	if err != nil {
		return err
	}
	numConsumers := conf.NumConsumers
	if numConsumers <= 0 {
		numConsumers = 1
	}
	c.mountID = conf.MountID
	c.async = true
	for i := 0; i < numConsumers; i++ {
		go c.Postprocessing(ch)
	}
	return nil
}

// Postprocessing consumes postprocessing results until ch is closed. Run it in
// its own goroutine, one per configured consumer.
func (c *coordinator) Postprocessing(ch <-chan events.Event) {
	for event := range ch {
		c.processEvent(context.Background(), event)
	}
}

// servesStorage reports whether an event is for the storage this coordinator
// serves. Postprocessing runs as a separate service and broadcasts its results to
// every storage provider, so each has to recognise its own. Events that name no
// storage predate this and are accepted.
func (c *coordinator) servesStorage(id *provider.ResourceId) bool {
	if c.mountID == "" || id.GetStorageId() == "" {
		return true
	}
	return id.GetStorageId() == c.mountID
}

func (c *coordinator) processEvent(ctx context.Context, event events.Event) {
	log := appctx.GetLogger(ctx)

	switch ev := event.Event.(type) {
	case events.PostprocessingFinished:
		if !c.servesStorage(ev.ResourceID) {
			return
		}
		c.onPostprocessingFinished(ctx, ev, log)
	case events.RestartPostprocessing:
		c.onRestartPostprocessing(ctx, ev, log)
	case events.CleanUpload:
		session, err := c.store.Get(ctx, ev.UploadID)
		if err != nil {
			log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("CleanUpload: could not load session")
			return
		}
		if !ev.KeepUpload {
			c.rollbackPrepared(ctx, session, session.SizeDiff())
		}
	case events.PostprocessingStepFinished:
		if !c.servesStorage(ev.ResourceID) {
			return
		}
		if ev.FinishedStep != events.PPStepAntivirus {
			// only the antivirus result is recorded on the session
			return
		}
		res, ok := ev.Result.(events.VirusscanResult)
		if !ok || res.ErrorMsg != "" {
			// the scan itself failed; PostprocessingFinished decides the outcome
			return
		}
		session, err := c.store.Get(ctx, ev.UploadID)
		if err != nil {
			// an empty upload id means an on-demand scan, which has no session
			if ev.UploadID != "" {
				log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("PostprocessingStepFinished: could not load session")
			}
			return
		}
		session.SetScanData(res.Description, res.Scandate)
		if err := session.Persist(ctx); err != nil {
			log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("could not persist scan result")
		}
	}
}

// onPostprocessingFinished completes or discards an upload according to the
// outcome postprocessing reported.
func (c *coordinator) onPostprocessingFinished(ctx context.Context, ev events.PostprocessingFinished, log *zerolog.Logger) {
	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		// Without the session we cannot reach the staged bytes, so they are leaked
		// here. Housekeeping cleans them up later.
		log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("PostprocessingFinished: could not load session")
		return
	}
	ctx = session.Context(ctx)
	log = appctx.GetLogger(ctx)

	switch ev.Outcome {
	case events.PPOutcomeContinue:
		if err := c.finishAsync(ctx, session); err != nil {
			log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("could not commit upload after postprocessing. Upload preserved for restart.")
			c.publishUploadFailed(ctx, session, ev)
		}
		return
	case events.PPOutcomeAbort:
		metrics.UploadSessionsAborted.Inc()
		// Keep the staged bytes: an abort is a transient failure and the upload can
		// be restarted with RestartPostprocessing.
		c.rollbackNode(ctx, session)
	case events.PPOutcomeDelete:
		metrics.UploadSessionsDeleted.Inc()
		c.rollbackPrepared(ctx, session, session.SizeDiff())
	default:
		log.Error().Str("outcome", string(ev.Outcome)).Str("uploadid", ev.UploadID).Msg("unknown postprocessing outcome, aborting")
		metrics.UploadSessionsAborted.Inc()
		c.rollbackNode(ctx, session)
	}

	c.publishUploadFailed(ctx, session, ev)
}

// onRestartPostprocessing re-publishes BytesReceived so a previously aborted
// upload gets another postprocessing run.
func (c *coordinator) onRestartPostprocessing(ctx context.Context, ev events.RestartPostprocessing, log *zerolog.Logger) {
	session, err := c.store.Get(ctx, ev.UploadID)
	if err != nil {
		log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("RestartPostprocessing: could not load session")
		return
	}
	metrics.UploadSessionsRestarted.Inc()
	if err := c.publishBytesReceived(session.Context(ctx), session); err != nil {
		log.Error().Err(err).Str("uploadid", ev.UploadID).Msg("could not restart postprocessing")
	}
}

// rollbackNode reverts the node to its pre-upload state but keeps the staged
// bytes and the session, so postprocessing can be restarted.
func (c *coordinator) rollbackNode(ctx context.Context, session Session) {
	ref := session.Reference()
	if err := c.fs.RollbackUpload(ctx, &ref, session.ID(), session.NodeExists(), session.SizeDiff()); err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Str("uploadid", session.ID()).Msg("could not roll back upload")
	}
	if err := c.fs.MarkProcessing(ctx, &ref, false, session.ID()); err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Str("uploadid", session.ID()).Msg("could not unmark processing")
	}
}

// publishUploadFailed tells consumers the upload will not become available.
// Clients wait on UploadReady, so staying silent would leave them hanging.
func (c *coordinator) publishUploadFailed(ctx context.Context, session Session, ev events.PostprocessingFinished) {
	if c.pub == nil {
		return
	}
	if err := events.Publish(ctx, c.pub, events.UploadReady{
		UploadID:      session.ID(),
		Failed:        true,
		Filename:      session.Filename(),
		SpaceOwner:    session.SpaceOwner(),
		ExecutingUser: ev.ExecutingUser,
		FileRef:       c.uploadRef(session),
		ResourceID: &provider.ResourceId{
			StorageId: session.ProviderID(),
			SpaceId:   session.SpaceID(),
			OpaqueId:  session.NodeID(),
		},
		Timestamp:         utils.TSNow(),
		IsVersion:         session.VersionCreated(),
		ImpersonatingUser: ev.ImpersonatingUser,
	}); err != nil {
		appctx.GetLogger(ctx).Error().Err(err).Str("uploadid", session.ID()).Msg("failed to publish UploadReady event")
	}
}
