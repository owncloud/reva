package gateway

import (
	"context"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	microstore "go-micro.dev/v4/store"
)

type mockCreatePersonalSpaceCache struct {
	mock.Mock
}

func (m *mockCreatePersonalSpaceCache) GetKey(uid *userpb.UserId) string {
	args := m.Called(uid)
	return args.String(0)
}

func (m *mockCreatePersonalSpaceCache) PushToCache(key string, res interface{}) error {
	args := m.Called(key, res)
	return args.Error(0)
}

func (m *mockCreatePersonalSpaceCache) PullFromCache(key string, res interface{}) error {
	args := m.Called(key, res)
	if args.Error(0) == nil {
		// Mock pulling a successful response
		if r, ok := res.(*provider.CreateHomeResponse); ok {
			r.Status = &rpc.Status{Code: rpc.Code_CODE_OK}
		}
		if r, ok := res.(*provider.CreateStorageSpaceResponse); ok {
			r.Status = &rpc.Status{Code: rpc.Code_CODE_OK}
		}
	}
	return args.Error(0)
}

func (m *mockCreatePersonalSpaceCache) Delete(key string, opts ...microstore.DeleteOption) error {
	args := m.Called(key, opts)
	return args.Error(0)
}

func (m *mockCreatePersonalSpaceCache) List(opts ...microstore.ListOption) ([]string, error) {
	args := m.Called(opts)
	return args.Get(0).([]string), args.Error(1)
}

func (m *mockCreatePersonalSpaceCache) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestCreateHomeCache(t *testing.T) {
	mockCache := &mockCreatePersonalSpaceCache{}
	client := &cachedAPIClient{
		createPersonalSpaceCache: mockCache,
	}

	user := &userpb.User{Id: &userpb.UserId{OpaqueId: "user1"}}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)
	key := "cache_key"

	mockCache.On("GetKey", user.Id).Return(key)
	mockCache.On("PullFromCache", key, mock.Anything).Return(nil)

	res, err := client.CreateHome(ctx, &provider.CreateHomeRequest{})

	assert.NoError(t, err)
	assert.Equal(t, rpc.Code_CODE_ALREADY_EXISTS, res.Status.Code)
	mockCache.AssertExpectations(t)
}

func TestCreateStorageSpaceCache(t *testing.T) {
	mockCache := &mockCreatePersonalSpaceCache{}
	client := &cachedSpacesAPIClient{
		createPersonalSpaceCache: mockCache,
	}

	user := &userpb.User{Id: &userpb.UserId{OpaqueId: "user1"}}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)
	key := "cache_key"

	mockCache.On("GetKey", user.Id).Return(key)
	mockCache.On("PullFromCache", key, mock.Anything).Return(nil)

	// PullFromCache will set the status to OK in our mock, but the cachedSpacesAPIClient should override it to ALREADY_EXISTS
	res, err := client.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{Type: "personal"})

	assert.NoError(t, err)
	assert.Equal(t, rpc.Code_CODE_ALREADY_EXISTS, res.Status.Code)
	mockCache.AssertExpectations(t)
}
