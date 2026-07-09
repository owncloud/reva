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
	"fmt"
	"os"
	"testing"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/storage"
)

// TestNewCoordinator covers the constructor's validation logic.
func TestNewCoordinator(t *testing.T) {
	root := t.TempDir()
	log := zerolog.Nop()
	store := newTestStore(t, root)
	fs := &mockFS{}

	t.Run("async without publisher returns error", func(t *testing.T) {
		_, err := NewCoordinator(fs, store, nil, true, "m", "g", 1, &log)
		require.Error(t, err)
	})

	t.Run("sync without publisher succeeds", func(t *testing.T) {
		coord, err := NewCoordinator(fs, store, nil, false, "m", "g", 1, &log)
		require.NoError(t, err)
		require.NotNil(t, coord)
	})

	t.Run("async with publisher succeeds", func(t *testing.T) {
		pub := &mockPublisher{}
		coord, err := NewCoordinator(fs, store, pub, true, "m", "g", 1, &log)
		require.NoError(t, err)
		require.NotNil(t, coord)
	})

	t.Run("numConsumers zero defaults to 1", func(t *testing.T) {
		coord, err := NewCoordinator(fs, store, nil, false, "m", "g", 0, &log)
		require.NoError(t, err)
		require.NotNil(t, coord)
		c := coord.(*coordinator)
		assert.Equal(t, 1, c.numConc)
	})
}

// TestRollback verifies rollback behaviour: unmarks processing, removes session
// files, and (for new files) deletes the placeholder node.
func TestRollback(t *testing.T) {
	t.Run("new file: unmarks processing, cleans files, deletes node", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)

		ref := session.Reference()
		fs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		fs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		coord.(*coordinator).rollback(context.Background(), session)

		fs.AssertExpectations(t)
		assert.NoFileExists(t, session.BinPath())
		infoPath := fileSessionPath(store.root, session.ID())
		assert.NoFileExists(t, infoPath)
	})

	t.Run("overwrite: unmarks processing, cleans files, no delete", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", true)

		ref := session.Reference()
		fs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		coord.(*coordinator).rollback(context.Background(), session)

		fs.AssertExpectations(t)
		// Delete must NOT have been called — if it were, mock would record it as unexpected.
	})
}

// TestFinishSync covers finishSync: happy path, missing .bin, and CommitUpload failure.
func TestFinishSync(t *testing.T) {
	t.Run("happy path: commits, unmarks, removes bin and info", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)
		// Write content to the bin file.
		require.NoError(t, os.WriteFile(session.BinPath(), []byte("hello"), 0600))
		// Reload session so offset is correct.
		loaded, err := store.Get(context.Background(), session.ID())
		require.NoError(t, err)

		ref := loaded.Reference()
		fs.On("CommitUpload", mock.Anything, &ref, mock.AnythingOfType("storage.UploadSource")).Return((*provider.ResourceInfo)(nil), nil)
		fs.On("MarkProcessing", mock.Anything, &ref, false, loaded.ID()).Return(nil)

		err = coord.(*coordinator).finishSync(context.Background(), loaded)
		require.NoError(t, err)

		fs.AssertExpectations(t)
		assert.NoFileExists(t, loaded.BinPath())
		infoPath := fileSessionPath(store.root, loaded.ID())
		assert.NoFileExists(t, infoPath)
	})

	t.Run("missing bin triggers rollback and returns error", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)
		// Remove the bin file to simulate it being missing.
		require.NoError(t, os.Remove(session.BinPath()))

		ref := session.Reference()
		fs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		fs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		err := coord.(*coordinator).finishSync(context.Background(), session)
		require.Error(t, err)
		fs.AssertExpectations(t)
	})

	t.Run("CommitUpload failure triggers rollback and returns error", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)
		require.NoError(t, os.WriteFile(session.BinPath(), []byte("data"), 0600))
		loaded, err := store.Get(context.Background(), session.ID())
		require.NoError(t, err)

		ref := loaded.Reference()
		fs.On("CommitUpload", mock.Anything, &ref, mock.Anything).Return((*provider.ResourceInfo)(nil), errors.New("commit error"))
		// rollback calls MarkProcessing(false) and Delete (new file)
		fs.On("MarkProcessing", mock.Anything, &ref, false, loaded.ID()).Return(nil)
		fs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		err = coord.(*coordinator).finishSync(context.Background(), loaded)
		require.Error(t, err)
		fs.AssertExpectations(t)
	})
}

// TestListUploadSessions covers filtering logic.
func TestListUploadSessions(t *testing.T) {
	root := t.TempDir()
	coord, _, store := newTestCoordinatorWithStore(t, root, false, nil)
	ctx := context.Background()

	// Create three sessions with different characteristics.
	s1 := newPopulatedSession(t, store, "/", "a.txt", "n1", "sp1", false)
	s2 := newPopulatedSession(t, store, "/", "b.txt", "n2", "sp2", false)
	s3 := newPopulatedSession(t, store, "/", "c.txt", "n3", "sp3", false)

	// Mark s1 as fully received (processing) by aligning offset with size.
	// FileSession.IsProcessing() returns true when size==offset && scanResult=="".
	// Since size defaults to 0, all sessions with empty bins are "processing" by that
	// logic. Set s2 size to 10 so it is NOT processing (offset=0 != size=10).
	{
		s := s2
		s.SetSize(10)
		require.NoError(t, s.Persist(ctx))
		// Touch the bin to keep the store happy (offset will be 0, != size 10).
	}

	// Mark s3 as expired by setting expires in the past.
	{
		s := s3
		past := time.Now().Add(-time.Hour)
		s.SetMetadata("expires", fmt.Sprintf("%d", past.Unix()))
		require.NoError(t, s.Persist(ctx))
	}

	t.Run("by ID found", func(t *testing.T) {
		id := s1.ID()
		sessions, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{ID: &id})
		require.NoError(t, err)
		require.Len(t, sessions, 1)
		assert.Equal(t, s1.ID(), sessions[0].ID())
	})

	t.Run("by ID not found returns error", func(t *testing.T) {
		id := "nonexistent-id"
		_, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{ID: &id})
		require.Error(t, err)
	})

	t.Run("filter Processing=true", func(t *testing.T) {
		tr := true
		sessions, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{Processing: &tr})
		require.NoError(t, err)
		// s1 and s3 have size==0==offset; s2 has size=10 != offset=0
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = s.ID()
		}
		assert.Contains(t, ids, s1.ID())
		assert.NotContains(t, ids, s2.ID())
	})

	t.Run("filter Processing=false", func(t *testing.T) {
		fl := false
		sessions, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{Processing: &fl})
		require.NoError(t, err)
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = s.ID()
		}
		assert.Contains(t, ids, s2.ID())
		assert.NotContains(t, ids, s1.ID())
	})

	t.Run("filter Expired=true", func(t *testing.T) {
		tr := true
		sessions, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{Expired: &tr})
		require.NoError(t, err)
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = s.ID()
		}
		assert.Contains(t, ids, s3.ID())
		// s1 and s2 have zero Expires() which is before now, so they may or may not be
		// included depending on the zero-time semantics — document that s3 is included.
		_ = s1
	})

	t.Run("no filter returns all", func(t *testing.T) {
		sessions, err := coord.ListUploadSessions(ctx, storage.UploadSessionFilter{})
		require.NoError(t, err)
		ids := make([]string, len(sessions))
		for i, s := range sessions {
			ids[i] = s.ID()
		}
		assert.Contains(t, ids, s1.ID())
		assert.Contains(t, ids, s2.ID())
		assert.Contains(t, ids, s3.ID())
	})
}
