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
	"io"
	"net/url"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	goevents "go-micro.dev/v4/events"

	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/storage"
)

// mockFS implements storage.FS for the coordinator tests.
// Only the methods actually exercised by the coordinator are wired to testify/mock;
// all others panic so accidental calls are caught immediately.
type mockFS struct {
	mock.Mock
}

func (m *mockFS) GetMD(ctx context.Context, ref *provider.Reference, mdKeys, fieldMask []string) (*provider.ResourceInfo, error) {
	args := m.Called(ctx, ref, mdKeys, fieldMask)
	ri, _ := args.Get(0).(*provider.ResourceInfo)
	return ri, args.Error(1)
}

func (m *mockFS) GetQuota(ctx context.Context, ref *provider.Reference) (uint64, uint64, uint64, error) {
	args := m.Called(ctx, ref)
	return args.Get(0).(uint64), args.Get(1).(uint64), args.Get(2).(uint64), args.Error(3)
}

func (m *mockFS) TouchFile(ctx context.Context, ref *provider.Reference, markprocessing bool, mtime string) (*storage.TouchFileResult, error) {
	args := m.Called(ctx, ref, markprocessing, mtime)
	r, _ := args.Get(0).(*storage.TouchFileResult)
	return r, args.Error(1)
}

func (m *mockFS) MarkProcessing(ctx context.Context, ref *provider.Reference, processing bool, sessionID string) error {
	args := m.Called(ctx, ref, processing, sessionID)
	return args.Error(0)
}

func (m *mockFS) CommitUpload(ctx context.Context, ref *provider.Reference, source storage.UploadSource) (*provider.ResourceInfo, error) {
	args := m.Called(ctx, ref, source)
	ri, _ := args.Get(0).(*provider.ResourceInfo)
	return ri, args.Error(1)
}

func (m *mockFS) Delete(ctx context.Context, ref *provider.Reference) (*storage.DeleteResult, error) {
	args := m.Called(ctx, ref)
	dr, _ := args.Get(0).(*storage.DeleteResult)
	return dr, args.Error(1)
}

func (m *mockFS) SetArbitraryMetadata(ctx context.Context, ref *provider.Reference, md *provider.ArbitraryMetadata) error {
	args := m.Called(ctx, ref, md)
	return args.Error(0)
}

// unimplemented methods panic to catch accidental calls

func (m *mockFS) Shutdown(_ context.Context) error { panic("not implemented: Shutdown") }

func (m *mockFS) ListStorageSpaces(_ context.Context, _ []*provider.ListStorageSpacesRequest_Filter, _ bool) ([]*provider.StorageSpace, error) {
	panic("not implemented: ListStorageSpaces")
}

func (m *mockFS) ListFolder(_ context.Context, _ *provider.Reference, _, _ []string) ([]*provider.ResourceInfo, error) {
	panic("not implemented: ListFolder")
}

func (m *mockFS) Download(_ context.Context, _ *provider.Reference, _ func(*provider.ResourceInfo) bool) (*provider.ResourceInfo, io.ReadCloser, error) {
	panic("not implemented: Download")
}

func (m *mockFS) GetPathByID(_ context.Context, _ *provider.ResourceId) (string, error) {
	panic("not implemented: GetPathByID")
}

func (m *mockFS) CreateReference(_ context.Context, _ string, _ *url.URL) error {
	panic("not implemented: CreateReference")
}

func (m *mockFS) CreateDir(_ context.Context, _ *provider.Reference) (*storage.CreateDirResult, error) {
	panic("not implemented: CreateDir")
}

func (m *mockFS) Move(_ context.Context, _, _ *provider.Reference) (*storage.MoveResult, error) {
	panic("not implemented: Move")
}

func (m *mockFS) InitiateUpload(_ context.Context, _ *provider.Reference, _ int64, _ map[string]string) (map[string]string, error) {
	panic("not implemented: InitiateUpload")
}

func (m *mockFS) Upload(_ context.Context, _ storage.UploadRequest, _ storage.UploadFinishedFunc) (*provider.ResourceInfo, error) {
	panic("not implemented: Upload")
}

func (m *mockFS) ListRevisions(_ context.Context, _ *provider.Reference) ([]*provider.FileVersion, error) {
	panic("not implemented: ListRevisions")
}

func (m *mockFS) DownloadRevision(_ context.Context, _ *provider.Reference, _ string, _ func(*provider.ResourceInfo) bool) (*provider.ResourceInfo, io.ReadCloser, error) {
	panic("not implemented: DownloadRevision")
}

func (m *mockFS) RestoreRevision(_ context.Context, _ *provider.Reference, _ string) (*storage.RestoreRevisionResult, error) {
	panic("not implemented: RestoreRevision")
}

func (m *mockFS) ListRecycle(_ context.Context, _ *provider.Reference, _, _ string) ([]*provider.RecycleItem, error) {
	panic("not implemented: ListRecycle")
}

func (m *mockFS) RestoreRecycleItem(_ context.Context, _ *provider.Reference, _, _ string, _ *provider.Reference) (*storage.RestoreRecycleItemResult, error) {
	panic("not implemented: RestoreRecycleItem")
}

func (m *mockFS) PurgeRecycleItem(_ context.Context, _ *provider.Reference, _, _ string) error {
	panic("not implemented: PurgeRecycleItem")
}

func (m *mockFS) EmptyRecycle(_ context.Context, _ *provider.Reference) error {
	panic("not implemented: EmptyRecycle")
}

func (m *mockFS) AddGrant(_ context.Context, _ *provider.Reference, _ *provider.Grant) error {
	panic("not implemented: AddGrant")
}

func (m *mockFS) DenyGrant(_ context.Context, _ *provider.Reference, _ *provider.Grantee) error {
	panic("not implemented: DenyGrant")
}

func (m *mockFS) RemoveGrant(_ context.Context, _ *provider.Reference, _ *provider.Grant) error {
	panic("not implemented: RemoveGrant")
}

func (m *mockFS) UpdateGrant(_ context.Context, _ *provider.Reference, _ *provider.Grant) error {
	panic("not implemented: UpdateGrant")
}

func (m *mockFS) ListGrants(_ context.Context, _ *provider.Reference) ([]*provider.Grant, error) {
	panic("not implemented: ListGrants")
}

func (m *mockFS) UnsetArbitraryMetadata(_ context.Context, _ *provider.Reference, _ []string) error {
	panic("not implemented: UnsetArbitraryMetadata")
}

func (m *mockFS) GetLock(ctx context.Context, ref *provider.Reference) (*provider.Lock, error) {
	args := m.Called(ctx, ref)
	return args.Get(0).(*provider.Lock), args.Error(1)
}

func (m *mockFS) SetLock(_ context.Context, _ *provider.Reference, _ *provider.Lock) (*storage.SetLockResult, error) {
	panic("not implemented: SetLock")
}

func (m *mockFS) RefreshLock(_ context.Context, _ *provider.Reference, _ *provider.Lock, _ string) error {
	panic("not implemented: RefreshLock")
}

func (m *mockFS) Unlock(_ context.Context, _ *provider.Reference, _ *provider.Lock) (*storage.UnlockResult, error) {
	panic("not implemented: Unlock")
}

func (m *mockFS) CreateStorageSpace(_ context.Context, _ *provider.CreateStorageSpaceRequest) (*provider.CreateStorageSpaceResponse, error) {
	panic("not implemented: CreateStorageSpace")
}

func (m *mockFS) UpdateStorageSpace(_ context.Context, _ *provider.UpdateStorageSpaceRequest) (*provider.UpdateStorageSpaceResponse, error) {
	panic("not implemented: UpdateStorageSpace")
}

func (m *mockFS) DeleteStorageSpace(_ context.Context, _ *provider.DeleteStorageSpaceRequest) (*storage.DeleteStorageSpaceResult, error) {
	panic("not implemented: DeleteStorageSpace")
}

func (m *mockFS) CreateHome(_ context.Context) error        { panic("not implemented: CreateHome") }
func (m *mockFS) GetHome(_ context.Context) (string, error) { panic("not implemented: GetHome") }

// mockPublisher captures published events for inspection in tests.
type mockPublisher struct {
	events []interface{}
}

func (p *mockPublisher) Publish(_ string, msg interface{}, _ ...goevents.PublishOption) error {
	p.events = append(p.events, msg)
	return nil
}

// newTestStore creates a FileStore with JWT transfer options rooted at root.
func newTestStore(t *testing.T, root string) *FileStore {
	t.Helper()
	log := zerolog.Nop()
	store := NewFileStore(root, TokenOptions{
		DownloadEndpoint:     "http://dl",
		DataGatewayEndpoint:  "http://gw",
		TransferSharedSecret: "secret",
		TransferExpires:      3600,
	}, &log)
	require.NoError(t, store.Setup())
	return store
}

// newTestCoordinator builds a coordinator backed by a real FileStore at root.
// Returns the coordinator and the mockFS so callers can set up expectations.
func newTestCoordinator(t *testing.T, root string, async bool, pub events.Publisher) (Coordinator, *mockFS) {
	t.Helper()
	log := zerolog.Nop()
	store := newTestStore(t, root)
	fs := &mockFS{}
	coord, err := NewCoordinator(fs, store, pub, async, "test-mount", "test-group", 1, &log, "")
	require.NoError(t, err)
	return coord, fs
}

// newTestCoordinatorWithStore is like newTestCoordinator but also returns the FileStore.
func newTestCoordinatorWithStore(t *testing.T, root string, async bool, pub events.Publisher) (Coordinator, *mockFS, *FileStore) {
	t.Helper()
	log := zerolog.Nop()
	store := newTestStore(t, root)
	fs := &mockFS{}
	coord, err := NewCoordinator(fs, store, pub, async, "test-mount", "test-group", 1, &log, "")
	require.NoError(t, err)
	return coord, fs, store
}

// newPopulatedSession creates a persisted session with .bin and .info files on disk.
func newPopulatedSession(t *testing.T, store *FileStore, dir, filename, nodeID, spaceID string, nodeExists bool) Session {
	t.Helper()
	ctx := context.Background()
	s := store.New(ctx)
	s.SetMetadata("filename", filename)
	s.SetStorageValue("NodeName", filename)
	s.SetMetadata("dir", dir)
	s.SetStorageValue("Dir", dir)
	s.SetStorageValue("NodeId", nodeID)
	s.SetStorageValue("SpaceRoot", spaceID)
	s.SetMetadata("providerID", "test-provider")
	s.SetStorageValue("SpaceOwnerOrManager", "owner1")
	if nodeExists {
		s.SetStorageValue("NodeExists", "true")
	}
	s.SetExecutant(&userpb.User{
		Id: &userpb.UserId{OpaqueId: "user1", Idp: "idp"},
	})
	require.NoError(t, s.TouchBin())
	require.NoError(t, s.Persist(ctx))
	return s
}
