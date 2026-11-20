// Package helpers holds metadata.Storage test doubles shared by the jsoncs3
// share manager's cache test suites (providercache, sharecache,
// receivedsharecache), which all exercise the same CAS-retry contract.
package helpers

import (
	"context"
	"sync/atomic"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// ErrOnceUploadStorage fails Upload once with a configured error, then delegates.
type ErrOnceUploadStorage struct {
	metadata.Storage
	Err     error
	Uploads int32
}

// Upload fails with the configured error on the first call, then delegates to the wrapped Storage.
func (e *ErrOnceUploadStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if atomic.AddInt32(&e.Uploads, 1) == 1 {
		return nil, e.Err
	}
	return e.Storage.Upload(ctx, req)
}

// ErrOnceDownloadStorage fails Download once with a configured error, then delegates.
type ErrOnceDownloadStorage struct {
	metadata.Storage
	Err       error
	Downloads int32
}

// Download fails with the configured error on the first call, then delegates to the wrapped Storage.
func (e *ErrOnceDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if atomic.AddInt32(&e.Downloads, 1) == 1 {
		return nil, e.Err
	}
	return e.Storage.Download(ctx, req)
}

// AlwaysAbortedUploadStorage fails every Upload with errtypes.Aborted, never delegating.
type AlwaysAbortedUploadStorage struct {
	metadata.Storage
	Uploads int32
}

// Upload always fails with errtypes.Aborted.
func (a *AlwaysAbortedUploadStorage) Upload(_ context.Context, _ metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&a.Uploads, 1)
	return nil, errtypes.Aborted("injected")
}
