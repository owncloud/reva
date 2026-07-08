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
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec
	"encoding/hex"
	"io"
	"strings"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/storage"
)

// initiateAndGetID calls InitiateUpload on coord and returns the session ID.
// It sets up the expected FS mock calls for a new-file happy path.
func initiateAndGetID(t *testing.T, coord Coordinator, mockFs *mockFS, content string) string {
	t.Helper()
	ctx := ctxpkg.ContextSetUser(context.Background(), &userpb.User{
		Id: &userpb.UserId{OpaqueId: "user1", Idp: "idp"},
	})
	r := &provider.Reference{Path: "/dir/file.txt"}

	mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
	mockFs.On("GetQuota", mock.Anything, r).Return(uint64(100), uint64(50), uint64(50), nil)
	mockFs.On("TouchFile", mock.Anything, r, false, "").Return(&storage.TouchFileResult{
		ResourceID: &provider.ResourceId{OpaqueId: "node1"},
		SpaceID:    "space1",
		SpaceOwner: &userpb.UserId{OpaqueId: "owner1"},
	}, nil)
	mockFs.On("GetMD", mock.Anything, mock.Anything, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound("")).Maybe()
	mockFs.On("MarkProcessing", mock.Anything, mock.Anything, true, mock.AnythingOfType("string")).Return(nil)

	ids, err := coord.InitiateUpload(ctx, r, int64(len(content)), nil)
	require.NoError(t, err)
	return ids["simple"]
}

// newUploadStore creates a new FileStore at the same root as used by the coordinator.
func newUploadStore(t *testing.T, root string) *FileStore {
	t.Helper()
	log := zerolog.Nop()
	return NewFileStore(root, TokenOptions{
		DownloadEndpoint:     "http://dl",
		DataGatewayEndpoint:  "http://gw",
		TransferSharedSecret: "secret",
		TransferExpires:      3600,
	}, &log)
}

// TestChecksumAndFinish tests the checksumAndFinish package-level function.
func TestChecksumAndFinish(t *testing.T) {
	t.Run("no checksum in metadata: sets checksums on session", func(t *testing.T) {
		root := t.TempDir()
		_, _, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)

		content := "hello world"
		n, err := session.WriteChunk(context.Background(), 0, strings.NewReader(content))
		require.NoError(t, err)
		require.Equal(t, int64(len(content)), n)

		err = checksumAndFinish(context.Background(), session)
		require.NoError(t, err)

		cs := session.Checksums()
		assert.NotEmpty(t, cs.SHA1)
		assert.NotEmpty(t, cs.MD5)
		assert.NotEmpty(t, cs.Adler32)
	})

	t.Run("correct sha1 checksum passes", func(t *testing.T) {
		root := t.TempDir()
		_, _, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)

		content := "hello"
		_, err := session.WriteChunk(context.Background(), 0, strings.NewReader(content))
		require.NoError(t, err)

		h := sha1.New() //nolint:gosec
		h.Write([]byte(content))
		expected := hex.EncodeToString(h.Sum(nil))
		session.SetMetadata("checksum", "sha1 "+expected)
		require.NoError(t, session.Persist(context.Background()))

		require.NoError(t, checksumAndFinish(context.Background(), session))
	})

	t.Run("wrong sha1 checksum returns ChecksumMismatch and cleans bin", func(t *testing.T) {
		root := t.TempDir()
		_, _, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "file.txt", "node1", "space1", false)

		content := "hello"
		_, err := session.WriteChunk(context.Background(), 0, strings.NewReader(content))
		require.NoError(t, err)

		session.SetMetadata("checksum", "sha1 0000000000000000000000000000000000000000")
		require.NoError(t, session.Persist(context.Background()))

		err = checksumAndFinish(context.Background(), session)
		require.Error(t, err)
		_, isMismatch := err.(errtypes.ChecksumMismatch)
		assert.True(t, isMismatch)
		assert.NoFileExists(t, session.BinPath())
	})
}

// TestUpload_Sync tests Upload in sync mode.
func TestUpload_Sync(t *testing.T) {
	t.Run("happy path: CommitUpload called, uff invoked, ResourceInfo returned", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		content := "hello"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		mockFs.On("CommitUpload", mock.Anything, mock.Anything, mock.Anything).Return((*provider.ResourceInfo)(nil), nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, mock.AnythingOfType("string")).Return(nil)

		ctx := context.Background()
		var uffSpaceOwner, uffExecutant *userpb.UserId
		var uffRef *provider.Reference
		uff := func(so, ex *userpb.UserId, r *provider.Reference) {
			uffSpaceOwner = so
			uffExecutant = ex
			uffRef = r
		}

		ri, err := coord.Upload(ctx, storage.UploadRequest{
			Ref:    &provider.Reference{Path: "/" + sessionID},
			Body:   io.NopCloser(bytes.NewBufferString(content)),
			Length: int64(len(content)),
		}, uff)
		require.NoError(t, err)
		require.NotNil(t, ri)
		assert.Equal(t, "node1", ri.GetId().GetOpaqueId())
		assert.Equal(t, "space1", ri.GetId().GetSpaceId())
		assert.NotNil(t, uffSpaceOwner)
		assert.NotNil(t, uffExecutant)
		assert.NotNil(t, uffRef)
	})

	t.Run("size mismatch returns PartialContent", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		content := "hello"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		ctx := context.Background()
		_, err := coord.Upload(ctx, storage.UploadRequest{
			Ref:    &provider.Reference{Path: "/" + sessionID},
			Body:   io.NopCloser(bytes.NewBufferString("hi")),
			Length: int64(len(content)),
		}, nil)
		require.Error(t, err)
		_, isPartial := err.(errtypes.PartialContent)
		assert.True(t, isPartial)
	})

	t.Run("session not found returns error", func(t *testing.T) {
		root := t.TempDir()
		coord, _, _ := newTestCoordinatorWithStore(t, root, false, nil)

		ctx := context.Background()
		_, err := coord.Upload(ctx, storage.UploadRequest{
			Ref:    &provider.Reference{Path: "/nonexistent-id"},
			Body:   io.NopCloser(bytes.NewBufferString("data")),
			Length: 4,
		}, nil)
		require.Error(t, err)
	})

	t.Run("checksumAndFinish failure triggers rollback", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		content := "test"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		// Inject a bad checksum into the persisted session.
		secondStore := newUploadStore(t, root)
		sess, err := secondStore.Get(context.Background(), sessionID)
		require.NoError(t, err)
		sess.SetMetadata("checksum", "sha1 0000000000000000000000000000000000000000")
		require.NoError(t, sess.Persist(context.Background()))

		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, sessionID).Return(nil)
		mockFs.On("Delete", mock.Anything, mock.Anything).Return((*storage.DeleteResult)(nil), nil)

		ctx := context.Background()
		_, err = coord.Upload(ctx, storage.UploadRequest{
			Ref:    &provider.Reference{Path: "/" + sessionID},
			Body:   io.NopCloser(bytes.NewBufferString(content)),
			Length: int64(len(content)),
		}, nil)
		require.Error(t, err)
	})
}

// TestUpload_Async tests Upload in async mode.
func TestUpload_Async(t *testing.T) {
	t.Run("happy path: BytesReceived published, uff invoked", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)
		content := "asyncdata"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		ctx := context.Background()
		var uffCalled bool
		uff := func(_, _ *userpb.UserId, _ *provider.Reference) { uffCalled = true }

		ri, err := coord.Upload(ctx, storage.UploadRequest{
			Ref:    &provider.Reference{Path: "/" + sessionID},
			Body:   io.NopCloser(bytes.NewBufferString(content)),
			Length: int64(len(content)),
		}, uff)
		require.NoError(t, err)
		require.NotNil(t, ri)
		assert.True(t, uffCalled)

		require.Len(t, pub.events, 1)
		ev, ok := pub.events[0].(events.BytesReceived)
		require.True(t, ok)
		assert.Equal(t, sessionID, ev.UploadID)
		assert.Equal(t, "file.txt", ev.Filename)
		assert.Equal(t, uint64(len(content)), ev.Filesize)
	})
}

// TestCoordinatedUpload_FinishUpload tests the TUS FinishUpload path.
func TestCoordinatedUpload_FinishUpload(t *testing.T) {
	t.Run("sync: CommitUpload called, bin removed", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		content := "finishme"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		ctx := context.Background()
		up, err := coord.GetUpload(ctx, sessionID)
		require.NoError(t, err)
		_, err = up.WriteChunk(ctx, 0, strings.NewReader(content))
		require.NoError(t, err)

		mockFs.On("CommitUpload", mock.Anything, mock.Anything, mock.Anything).Return((*provider.ResourceInfo)(nil), nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, sessionID).Return(nil)

		err = up.FinishUpload(ctx)
		require.NoError(t, err)
		mockFs.AssertExpectations(t)
	})

	t.Run("async: BytesReceived published for non-zero size", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, true, pub)
		content := "asyncfinish"
		sessionID := initiateAndGetID(t, coord, mockFs, content)

		ctx := context.Background()
		up, err := coord.GetUpload(ctx, sessionID)
		require.NoError(t, err)
		_, err = up.WriteChunk(ctx, 0, strings.NewReader(content))
		require.NoError(t, err)

		err = up.FinishUpload(ctx)
		require.NoError(t, err)

		require.Len(t, pub.events, 1)
		ev, ok := pub.events[0].(events.BytesReceived)
		require.True(t, ok)
		assert.Equal(t, sessionID, ev.UploadID)
	})

	t.Run("async: size=0 no BytesReceived published", func(t *testing.T) {
		root := t.TempDir()
		pub := &mockPublisher{}
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, true, pub)
		// Create a session with NodeExists=true and size=0 (overwrite).
		session := newPopulatedSession(t, store, "/dir", "zero.txt", "n2", "sp2", true)
		session.SetSize(0)
		require.NoError(t, session.Persist(context.Background()))

		ctx := context.Background()
		up, err := coord.GetUpload(ctx, session.ID())
		require.NoError(t, err)

		mockFs.On("CommitUpload", mock.Anything, mock.Anything, mock.Anything).Return((*provider.ResourceInfo)(nil), nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, session.ID()).Return(nil)

		err = up.FinishUpload(ctx)
		require.NoError(t, err)
		assert.Empty(t, pub.events)
	})
}

// TestCoordinatedUpload_Terminate tests the TUS Terminate path.
func TestCoordinatedUpload_Terminate(t *testing.T) {
	t.Run("new file: Cleanup, MarkProcessing(false), Delete called", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "term.txt", "n1", "sp1", false)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)
		mockFs.On("Delete", mock.Anything, &ref).Return((*storage.DeleteResult)(nil), nil)

		up, err := coord.GetUpload(context.Background(), session.ID())
		require.NoError(t, err)
		cu := up.(*coordinatedUpload)
		require.NoError(t, cu.Terminate(context.Background()))

		mockFs.AssertExpectations(t)
		assert.NoFileExists(t, session.BinPath())
	})

	t.Run("overwrite: Cleanup, MarkProcessing(false), no Delete", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, false, nil)
		session := newPopulatedSession(t, store, "/dir", "term.txt", "n1", "sp1", true)

		ref := session.Reference()
		mockFs.On("MarkProcessing", mock.Anything, &ref, false, session.ID()).Return(nil)

		up, err := coord.GetUpload(context.Background(), session.ID())
		require.NoError(t, err)
		cu := up.(*coordinatedUpload)
		require.NoError(t, cu.Terminate(context.Background()))

		mockFs.AssertExpectations(t)
	})
}

// TestCoordinatedUpload_DeclareLength tests deferred-length declaration.
func TestCoordinatedUpload_DeclareLength(t *testing.T) {
	root := t.TempDir()
	coord, _, store := newTestCoordinatorWithStore(t, root, false, nil)
	session := newPopulatedSession(t, store, "/dir", "dl.txt", "n1", "sp1", false)
	session.SetSizeIsDeferred(true)
	require.NoError(t, session.Persist(context.Background()))

	up, err := coord.GetUpload(context.Background(), session.ID())
	require.NoError(t, err)
	cu := up.(*coordinatedUpload)
	require.NoError(t, cu.DeclareLength(context.Background(), 42))

	reloaded, err := store.Get(context.Background(), session.ID())
	require.NoError(t, err)
	assert.Equal(t, int64(42), reloaded.Size())
	info, err := reloaded.GetInfo(context.Background())
	require.NoError(t, err)
	assert.False(t, info.SizeIsDeferred)
}
