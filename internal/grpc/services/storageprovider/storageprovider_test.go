package storageprovider

import (
	"context"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockStorage struct {
	storage.FS
	mock.Mock
}

func (m *mockStorage) CreateStorageSpace(ctx context.Context, req *provider.CreateStorageSpaceRequest) (*provider.CreateStorageSpaceResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*provider.CreateStorageSpaceResponse), args.Error(1)
}

func (m *mockStorage) CreateHome(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestCreateStorageSpaceFallback(t *testing.T) {
	m := &mockStorage{}
	s := &Service{
		Storage: m,
	}

	user := &userpb.User{Id: &userpb.UserId{OpaqueId: "user1"}}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)

	req := &provider.CreateStorageSpaceRequest{
		Type:  "personal",
		Owner: user,
	}

	// Mock CreateStorageSpace returning NotSupported
	m.On("CreateStorageSpace", mock.Anything, req).Return(nil, errtypes.NotSupported("test"))
	// Mock CreateHome returning AlreadyExists
	m.On("CreateHome", mock.Anything).Return(errtypes.AlreadyExists("test"))

	res, err := s.CreateStorageSpace(ctx, req)

	assert.NoError(t, err)
	assert.Equal(t, rpc.Code_CODE_ALREADY_EXISTS, res.Status.Code)
	m.AssertExpectations(t)
}
