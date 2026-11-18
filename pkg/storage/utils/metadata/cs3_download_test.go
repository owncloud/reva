// Copyright 2018-2022 CERN
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

package metadata

import (
	"context"
	"net"
	"testing"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
)

// initiateDownloadProvider answers InitiateFileDownload with a fixed response
// or a fixed error. It emulates a storage provider replica.
type initiateDownloadProvider struct {
	provider.UnimplementedProviderAPIServer
	res *provider.InitiateFileDownloadResponse
	err error
}

func (p *initiateDownloadProvider) InitiateFileDownload(_ context.Context, _ *provider.InitiateFileDownloadRequest) (*provider.InitiateFileDownloadResponse, error) {
	return p.res, p.err
}

// startProvider serves srv on a loopback port and returns its address.
func startProvider(t *testing.T, srv provider.ProviderAPIServer) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := grpc.NewServer()
	provider.RegisterProviderAPIServer(s, srv)
	go func() {
		_ = s.Serve(lis)
	}()

	addr := lis.Addr().String()
	t.Cleanup(func() {
		s.Stop()
		// the pool caches selectors globally, keyed by kind+address
		pool.RemoveSelector("StorageProviderSelector" + addr)
	})

	return addr
}

// InitiateFileDownload does not read the file. It only returns the download
// URL. So an error from this call is a connection error, for example from a
// dead replica. Download must not change this error to NotFound. A caller
// that gets NotFound continues as if the file does not exist. For example,
// the settings service then skips a role assignment.
func TestDownloadKeepsConnectionErrorOfInitiateFileDownload(t *testing.T) {
	addr := startProvider(t, &initiateDownloadProvider{
		err: status.Error(codes.Unavailable, "replica is gone"),
	})
	cs3 := NewCS3(addr, addr)

	for name, download := range map[string]func() error{
		"Download": func() error {
			_, err := cs3.Download(context.Background(), DownloadRequest{Path: "/some/file"})
			return err
		},
		"SimpleDownload": func() error {
			_, err := cs3.SimpleDownload(context.Background(), "/some/file")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := download()
			require.Error(t, err)
			_, isNotFound := err.(errtypes.IsNotFound)
			assert.False(t, isNotFound, "a connection error must not become NotFound, got %T: %v", err, err)
			assert.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}

// A non-OK status from InitiateFileDownload must become the matching typed
// error, not a misleading message about the download protocol.
func TestDownloadMapsNonOKStatusOfInitiateFileDownload(t *testing.T) {
	addr := startProvider(t, &initiateDownloadProvider{
		res: &provider.InitiateFileDownloadResponse{
			Status: &rpc.Status{Code: rpc.Code_CODE_PERMISSION_DENIED, Message: "no access"},
		},
	})
	cs3 := NewCS3(addr, addr)

	_, err := cs3.Download(context.Background(), DownloadRequest{Path: "/some/file"})
	require.Error(t, err)
	_, isPermissionDenied := err.(errtypes.IsPermissionDenied)
	assert.True(t, isPermissionDenied, "want PermissionDenied, got %T: %v", err, err)
}
