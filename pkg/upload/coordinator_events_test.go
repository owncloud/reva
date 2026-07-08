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

package upload

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/storage"
)

// TestHandlePostprocessingFinished_Continue covers PPOutcomeContinue.
func TestHandlePostprocessingFinished_Continue(t *testing.T) {
	t.Run("happy path: CommitUpload, MarkProcessing(false), UploadReady{Failed:false}", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)
		require.NoError(t, os.WriteFile(session.BinPath(), []byte("data"), 0600))

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("CommitUpload", mock.Anything, &ref, mock.Anything).Return((*provider.ResourceInfo)(nil), nil)
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeContinue,
		})

		mockFs.AssertExpectations(t)
		require.Len(t, pub.events, 1)
		ev := pub.events[0].(events.UploadReady)
		assert.False(t, ev.Failed)
		assert.Equal(t, session.ID(), ev.UploadID)
	})

	t.Run("CommitUpload fails: retryCommit=true, no MarkProcessing, UploadReady{Failed:true}", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)
		require.NoError(t, os.WriteFile(session.BinPath(), []byte("data"), 0600))

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("CommitUpload", mock.Anything, &ref, mock.Anything).Return((*provider.ResourceInfo)(nil), errors.New("disk full"))

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeContinue,
		})

		// MarkProcessing must NOT be called (retryCommit=true).
		mockFs.AssertNotCalled(t, "MarkProcessing", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		require.Len(t, pub.events, 1)
		ev := pub.events[0].(events.UploadReady)
		assert.True(t, ev.Failed)
	})
}

// TestHandlePostprocessingFinished_Abort covers PPOutcomeAbort.
func TestHandlePostprocessingFinished_Abort(t *testing.T) {
	t.Run("new file: MarkProcessing(false), Delete, UploadReady{Failed:true}", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		// Abort + new file: revertNodeMetadata=true → Delete called
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeAbort,
		})

		mockFs.AssertExpectations(t)
		require.Len(t, pub.events, 1)
		ev := pub.events[0].(events.UploadReady)
		assert.True(t, ev.Failed)
	})

	t.Run("overwrite: MarkProcessing(false), no Delete", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", true)

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeAbort,
		})

		mockFs.AssertExpectations(t)
		require.Len(t, pub.events, 1)
	})
}

// TestHandlePostprocessingFinished_Delete covers PPOutcomeDelete.
func TestHandlePostprocessingFinished_Delete(t *testing.T) {
	t.Run("new file: session cleaned, MarkProcessing(false), Delete, UploadReady{Failed:true}", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeDelete,
		})

		mockFs.AssertExpectations(t)
		require.Len(t, pub.events, 1)
		ev := pub.events[0].(events.UploadReady)
		assert.True(t, ev.Failed)
		assert.NoFileExists(t, session.BinPath())
	})

	t.Run("overwrite: session cleaned, MarkProcessing(false), no Delete", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", true)

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return(&provider.ResourceInfo{
			Id: &provider.ResourceId{OpaqueId: "n1", SpaceId: "sp1"},
		}, nil)
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeDelete,
		})

		mockFs.AssertExpectations(t)
	})
}

// TestHandlePostprocessingFinished_EdgeCases covers edge cases.
func TestHandlePostprocessingFinished_EdgeCases(t *testing.T) {
	t.Run("different storageId ignored", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)

		coord.(*coordinator).handlePostprocessingFinished(context.Background(), events.PostprocessingFinished{
			UploadID: "any",
			ResourceID: &provider.ResourceId{
				StorageId: "different-mount",
				OpaqueId:  "node1",
			},
			Outcome: events.PPOutcomeContinue,
		})

		mockFs.AssertNotCalled(t, "GetMD", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		assert.Empty(t, pub.events)
	})

	t.Run("session not found: MarkProcessing via ResourceID ref", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)

		resID := &provider.ResourceId{
			StorageId: "test-mount",
			OpaqueId:  "node-orphan",
		}
		mockFs.On("MarkProcessing", mock.Anything, &provider.Reference{ResourceId: resID}, false, "orphan-id").Return(nil)

		coord.(*coordinator).handlePostprocessingFinished(context.Background(), events.PostprocessingFinished{
			UploadID:   "orphan-id",
			ResourceID: resID,
			Outcome:    events.PPOutcomeContinue,
		})

		mockFs.AssertExpectations(t)
		assert.Empty(t, pub.events)
	})

	t.Run("GetMD fails for node: session cleaned, MarkProcessing, no UploadReady", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ctx := context.Background()
		ref := session.Reference()
		mockFs.On("GetMD", mock.Anything, &ref, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound("n1"))
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).handlePostprocessingFinished(ctx, events.PostprocessingFinished{
			UploadID: session.ID(),
			Outcome:  events.PPOutcomeContinue,
		})

		mockFs.AssertExpectations(t)
		assert.Empty(t, pub.events)
		assert.NoFileExists(t, session.BinPath())
	})
}

// TestHandleRestartPostprocessing covers handleRestartPostprocessing.
func TestHandleRestartPostprocessing(t *testing.T) {
	t.Run("happy path: BytesReceived with postprocessing-restart executant", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, _, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		coord.(*coordinator).handleRestartPostprocessing(context.Background(), events.RestartPostprocessing{
			UploadID: session.ID(),
		})

		require.Len(t, pub.events, 1)
		ev, ok := pub.events[0].(events.BytesReceived)
		require.True(t, ok)
		assert.Equal(t, session.ID(), ev.UploadID)
		assert.Equal(t, "postprocessing-restart", ev.ExecutingUser.GetId().GetOpaqueId())
	})

	t.Run("session not found: no event published", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, _, _ := newTestCoordinatorWithStore(t, root, true, pub)

		coord.(*coordinator).handleRestartPostprocessing(context.Background(), events.RestartPostprocessing{
			UploadID: "nonexistent",
		})

		assert.Empty(t, pub.events)
	})
}

// TestHandleCleanUpload covers handleCleanUpload.
func TestHandleCleanUpload(t *testing.T) {
	t.Run("KeepUpload=false, new file: Cleanup(true,true), MarkProcessing(false), Delete", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		coord.(*coordinator).handleCleanUpload(context.Background(), events.CleanUpload{
			UploadID:   session.ID(),
			KeepUpload: false,
		})

		mockFs.AssertExpectations(t)
		assert.NoFileExists(t, session.BinPath())
	})

	t.Run("KeepUpload=true, new file: Cleanup(false,false), MarkProcessing(false), Delete", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		coord.(*coordinator).handleCleanUpload(context.Background(), events.CleanUpload{
			UploadID:   session.ID(),
			KeepUpload: true,
		})

		mockFs.AssertExpectations(t)
		// KeepUpload=true means Cleanup(false,false): bin and info stay.
		assert.FileExists(t, session.BinPath())
	})

	t.Run("KeepUpload=false, overwrite: Cleanup, MarkProcessing(false), no Delete", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", true)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).handleCleanUpload(context.Background(), events.CleanUpload{
			UploadID:   session.ID(),
			KeepUpload: false,
		})

		mockFs.AssertExpectations(t)
	})

	t.Run("Delete returns NotFound: silently ignored", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), errtypes.NotFound("n1"))

		// Should not panic or return error to caller.
		coord.(*coordinator).handleCleanUpload(context.Background(), events.CleanUpload{
			UploadID:   session.ID(),
			KeepUpload: false,
		})
		mockFs.AssertExpectations(t)
	})

	t.Run("session not found: no FS calls", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)

		coord.(*coordinator).handleCleanUpload(context.Background(), events.CleanUpload{
			UploadID:   "nonexistent",
			KeepUpload: false,
		})

		mockFs.AssertNotCalled(t, "MarkProcessing", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// TestHandlePostprocessingStepFinished covers handlePostprocessingStepFinished.
func TestHandlePostprocessingStepFinished(t *testing.T) {
	t.Run("antivirus clean scan: SetArbitraryMetadata called, session scan data persisted", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		ref := session.Reference()
		mockFs.On("SetArbitraryMetadata", mock.Anything, &ref, mock.MatchedBy(func(md *provider.ArbitraryMetadata) bool {
			return md.Metadata["scanstatus"] == "clean" && md.Metadata["scandate"] != ""
		})).Return(nil)

		scanDate := time.Now()
		coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
			UploadID:     session.ID(),
			FinishedStep: events.PPStepAntivirus,
			Result: events.VirusscanResult{
				Infected:    false,
				Description: "clean",
				Scandate:    scanDate,
			},
		})

		mockFs.AssertExpectations(t)

		// Reload session from disk and check scan data was persisted.
		reloaded, err := store.Get(context.Background(), session.ID())
		require.NoError(t, err)
		desc, _ := reloaded.ScanData()
		assert.Equal(t, "clean", desc)
	})

	t.Run("antivirus result with ErrorMsg: SetArbitraryMetadata not called", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
			UploadID:     session.ID(),
			FinishedStep: events.PPStepAntivirus,
			Result: events.VirusscanResult{
				ErrorMsg: "scanner unavailable",
			},
		})

		mockFs.AssertNotCalled(t, "SetArbitraryMetadata", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("non-antivirus step: no side effects", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
			UploadID:     session.ID(),
			FinishedStep: events.PPStepDelay,
		})

		mockFs.AssertNotCalled(t, "SetArbitraryMetadata", mock.Anything, mock.Anything, mock.Anything)
		mockFs.AssertNotCalled(t, "MarkProcessing", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("result wrong type: no panic", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		session := newPopulatedSession(t, store, "/dir", "f.txt", "n1", "sp1", false)

		// Should not panic; wrong result type is logged.
		require.NotPanics(t, func() {
			coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
				UploadID:     session.ID(),
				FinishedStep: events.PPStepAntivirus,
				Result:       "not-a-virusscan-result",
			})
		})
		mockFs.AssertNotCalled(t, "SetArbitraryMetadata", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("empty UploadID (on-demand scan): return early, no session lookup", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)

		coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
			UploadID:     "",
			FinishedStep: events.PPStepAntivirus,
			Result: events.VirusscanResult{
				Description: "clean",
			},
		})

		mockFs.AssertNotCalled(t, "SetArbitraryMetadata", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("different storageId: ignored", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)

		coord.(*coordinator).handlePostprocessingStepFinished(context.Background(), events.PostprocessingStepFinished{
			UploadID:     "any",
			FinishedStep: events.PPStepAntivirus,
			ResourceID:   &provider.ResourceId{StorageId: "other-mount"},
			Result: events.VirusscanResult{
				Description: "clean",
			},
		})

		mockFs.AssertNotCalled(t, "SetArbitraryMetadata", mock.Anything, mock.Anything, mock.Anything)
	})
}

// Ensure userpb is referenced to satisfy the import check.
var _ = userpb.UserType_USER_TYPE_PRIMARY
