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
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	tusd "github.com/tus/tusd/v2/pkg/handler"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
)

// authedCtx returns a context carrying a user, as required by InitiateUpload.
func authedCtx() context.Context {
	return ctxpkg.ContextSetUser(context.Background(), &userpb.User{
		Id: &userpb.UserId{OpaqueId: "user1", Idp: "idp"},
	})
}

// touchFileResult is a convenience helper for a successful TouchFile result.
func touchFileResult(nodeID, spaceID string) *storage.TouchFileResult {
	return &storage.TouchFileResult{
		ResourceID: &provider.ResourceId{OpaqueId: nodeID},
		SpaceID:    spaceID,
		SpaceOwner: &userpb.UserId{OpaqueId: "owner1"},
	}
}

// ref builds a simple path-only reference.
func ref(path string) *provider.Reference {
	return &provider.Reference{Path: path}
}

// dirInfo returns a minimal ResourceInfo for a directory with upload permission.
func dirInfo() *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Type: provider.ResourceType_RESOURCE_TYPE_CONTAINER,
		Id:   &provider.ResourceId{OpaqueId: "dir1", SpaceId: "space1"},
		PermissionSet: &provider.ResourcePermissions{InitiateFileUpload: true},
	}
}

// existingNodeInfo builds a ResourceInfo as GetMD would return for an existing node.
func existingNodeInfo(nodeID, spaceID string, size uint64) *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Id:            &provider.ResourceId{OpaqueId: nodeID, SpaceId: spaceID},
		Name:          filepath.Base("/dir/file.txt"),
		Path:          "/dir/file.txt",
		Size:          size,
		Owner:         &userpb.UserId{OpaqueId: "owner1"},
		PermissionSet: &provider.ResourcePermissions{InitiateFileUpload: true},
	}
}

// TestInitiateUpload_NewFile covers new-file scenarios.
func TestInitiateUpload_NewFile(t *testing.T) {
	t.Run("happy path creates session files and returns IDs", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, store := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		fs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)

		ids, err := coord.InitiateUpload(ctx, r, 10, nil)
		require.NoError(t, err)
		require.NotEmpty(t, ids["simple"])
		assert.Equal(t, ids["simple"], ids["tus"])

		sessionID := ids["simple"]
		binPath := filepath.Join(store.root, "uploads", sessionID)
		infoPath := filepath.Join(store.root, "uploads", sessionID+".info")
		assert.FileExists(t, binPath)
		assert.FileExists(t, infoPath)
	})

	t.Run("quota exceeded returns InsufficientStorage, no TouchFile", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(90), uint64(5), nil)

		_, err := coord.InitiateUpload(ctx, r, 10, nil)
		require.Error(t, err)
		_, isInsufficient := err.(errtypes.InsufficientStorage)
		assert.True(t, isInsufficient)
		fs.AssertNotCalled(t, "TouchFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("GetQuota failure skips quota check and proceeds", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		fs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(0), uint64(0), uint64(0), errors.New("quota unavailable"))

		_, err := coord.InitiateUpload(ctx, r, 10, nil)
		require.NoError(t, err)
		fs.AssertNotCalled(t, "TouchFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// TestInitiateUpload_Overwrite covers overwrite (existing node) scenarios.
func TestInitiateUpload_Overwrite(t *testing.T) {
	t.Run("happy path overwrite creates session without touching node", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")
		existing := existingNodeInfo("node1", "space1", 20)

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return(existing, nil)
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)
		fs.On("GetLock", mock.Anything, mock.Anything).Return((*provider.Lock)(nil), nil)

		ids, err := coord.InitiateUpload(ctx, r, 30, nil)
		require.NoError(t, err)
		require.NotEmpty(t, ids["simple"])
		fs.AssertNotCalled(t, "TouchFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		fs.AssertNotCalled(t, "MarkProcessing", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("quota exceeded for overwrite", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")
		existing := existingNodeInfo("node1", "space1", 5)

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return(existing, nil)
		fs.On("GetLock", mock.Anything, mock.Anything).Return((*provider.Lock)(nil), nil)
		// remaining=90, net_required=100-5=95 > 90
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(200), uint64(110), uint64(90), nil)

		_, err := coord.InitiateUpload(ctx, r, 100, nil)
		require.Error(t, err)
		_, isInsufficient := err.(errtypes.InsufficientStorage)
		assert.True(t, isInsufficient)
	})

	t.Run("upload smaller than existing: net_required=0, proceeds", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")
		existing := existingNodeInfo("node1", "space1", 50)

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return(existing, nil)
		// remaining=0 but net_required=0, should still succeed
		fs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(100), uint64(0), nil)
		fs.On("GetLock", mock.Anything, mock.Anything).Return((*provider.Lock)(nil), nil)

		_, err := coord.InitiateUpload(ctx, r, 30, nil)
		require.NoError(t, err)
	})

	t.Run("size-deferred (uploadLength=-1): GetQuota not called", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")
		existing := existingNodeInfo("node1", "space1", 20)

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return(existing, nil)
		fs.On("GetLock", mock.Anything, mock.Anything).Return((*provider.Lock)(nil), nil)

		_, err := coord.InitiateUpload(ctx, r, -1, map[string]string{"sizedeferred": "true"})
		require.NoError(t, err)
		fs.AssertNotCalled(t, "GetQuota", mock.Anything, mock.Anything)
	})
}

// TestInitiateUpload_ZeroLength covers the zero-length inline commit path.
func TestInitiateUpload_ZeroLength(t *testing.T) {
	t.Run("zero-length: CommitUpload called inline, files cleaned", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)
		mockFs.On("TouchFile", mock.Anything, mock.Anything, false, mock.Anything).Return(touchFileResult("node1", "space1"), nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, true, mock.AnythingOfType("string")).Return(nil)
		mockFs.On("PrepareUpload", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&storage.PrepareUploadResult{}, nil)
		mockFs.On("CommitUpload", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, mock.AnythingOfType("string")).Return(nil)

		ids, err := coord.InitiateUpload(ctx, r, 0, nil)
		require.NoError(t, err)
		require.NotEmpty(t, ids["simple"])

		sessionID := ids["simple"]
		binPath := filepath.Join(store.root, "uploads", sessionID)
		infoPath := filepath.Join(store.root, "uploads", sessionID+".info")
		assert.NoFileExists(t, binPath)
		assert.NoFileExists(t, infoPath)
	})

	t.Run("zero-length CommitUpload failure triggers rollback", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)
		mockFs.On("TouchFile", mock.Anything, mock.Anything, false, mock.Anything).Return(touchFileResult("node1", "space1"), nil)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, true, mock.AnythingOfType("string")).Return(nil)
		mockFs.On("PrepareUpload", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&storage.PrepareUploadResult{}, nil)
		mockFs.On("CommitUpload", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("commit failed"))
		// rollback: MarkProcessing(false), Delete (ref is session.Reference(), ResourceId-based)
		mockFs.On("MarkProcessing", mock.Anything, mock.Anything, false, mock.AnythingOfType("string")).Return(nil)
		mockFs.On("Delete", mock.Anything, mock.Anything).Return((*storage.DeleteResult)(nil), nil)

		_, err := coord.InitiateUpload(ctx, r, 0, nil)
		require.Error(t, err)
	})
}

// TestInitiateUpload_ErrorPaths covers failure cases during session setup.
func TestInitiateUpload_ErrorPaths(t *testing.T) {
	t.Run("GetMD unexpected error returns error without TouchFile", func(t *testing.T) {
		root := t.TempDir()
		coord, fs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		fs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errors.New("unexpected"))

		_, err := coord.InitiateUpload(ctx, r, 10, nil)
		require.Error(t, err)
		fs.AssertNotCalled(t, "TouchFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("TouchBin failure when uploads dir is missing: no node to delete", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, store := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		// Remove the uploads directory so TouchBin fails.
		require.NoError(t, os.RemoveAll(filepath.Join(store.root, "uploads")))

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)

		_, err := coord.InitiateUpload(ctx, r, 10, nil)
		require.Error(t, err)
		mockFs.AssertNotCalled(t, "TouchFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockFs.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
	})
}

// TestInitiateUpload_Metadata covers metadata/checksum validation.
func TestInitiateUpload_Metadata(t *testing.T) {
	t.Run("unsupported checksum algorithm returns BadRequest", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)

		_, err := coord.InitiateUpload(ctx, r, 10, map[string]string{
			"checksum": "crc32 abc123",
		})
		require.Error(t, err)
		_, isBad := err.(errtypes.BadRequest)
		assert.True(t, isBad)
	})

	t.Run("malformed checksum (no space) returns BadRequest", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)

		_, err := coord.InitiateUpload(ctx, r, 10, map[string]string{
			"checksum": "nospace",
		})
		require.Error(t, err)
		_, isBad := err.(errtypes.BadRequest)
		assert.True(t, isBad)
	})

	t.Run("valid sha1 checksum accepted", func(t *testing.T) {
		root := t.TempDir()
		coord, mockFs, _ := newTestCoordinatorWithStore(t, root, false, nil)
		ctx := authedCtx()
		r := ref("/dir/file.txt")

		mockFs.On("GetMD", mock.Anything, r, []string{}, []string{}).Return((*provider.ResourceInfo)(nil), errtypes.NotFound(""))
		mockFs.On("GetMD", mock.Anything, ref("/dir"), []string{}, []string{}).Return(dirInfo(), nil)
		mockFs.On("GetQuota", mock.Anything, mock.Anything).Return(uint64(100), uint64(50), uint64(50), nil)

		_, err := coord.InitiateUpload(ctx, r, 10, map[string]string{
			"checksum": "sha1 aabbccdd",
		})
		require.NoError(t, err)
	})
}

// noSuchPath returns true when the given path does not exist on disk.
func noSuchPath(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// Ensure tusd is imported so it stays in go.mod.
var _ = tusd.ErrNotFound
