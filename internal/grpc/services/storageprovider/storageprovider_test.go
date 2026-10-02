package storageprovider

import (
	"context"
	"testing"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/stretchr/testify/assert"
)

// fakeInitiateUploadFS is a storage.FS test double that only implements the
// two methods InitiateFileUpload's happy-path-to-InitiateUpload traversal
// needs; every other method panics if called (embedded nil interface).
type fakeInitiateUploadFS struct {
	storage.FS
	initiateUploadErr error
}

func (f *fakeInitiateUploadFS) GetMD(_ context.Context, _ *provider.Reference, _, _ []string) (*provider.ResourceInfo, error) {
	return nil, errtypes.NotFound("no such resource")
}

func (f *fakeInitiateUploadFS) InitiateUpload(_ context.Context, _ *provider.Reference, _ int64, _ map[string]string) (map[string]string, error) {
	return nil, f.initiateUploadErr
}

func TestInitiateFileUpload_LostCASRaceStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode rpc.Code
	}{
		{"Aborted (lost CAS race)", errtypes.Aborted("parent already has a child"), rpc.Code_CODE_ABORTED},
		{"AlreadyExists (concurrent create)", errtypes.AlreadyExists("child already exists"), rpc.Code_CODE_ALREADY_EXISTS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{
				conf:    &config{},
				Storage: &fakeInitiateUploadFS{initiateUploadErr: tt.err},
			}

			resp, err := s.InitiateFileUpload(context.Background(), &provider.InitiateFileUploadRequest{
				Ref: &provider.Reference{Path: "/foo"},
			})

			assert.NoError(t, err)
			assert.Equal(t, tt.wantCode, resp.Status.Code,
				"a lost CAS race must not be reported as CODE_INTERNAL (message: %s)", resp.Status.Message)
		})
	}
}
