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

// CountingUploadStorage counts Upload calls and delegates them.
// When FailPath is set, each Upload to that exact path fails with
// errtypes.PermissionDenied and is not counted.
// Set FailPath only while no other goroutine uses the storage.
type CountingUploadStorage struct {
	metadata.Storage
	FailPath string
	Uploads  int32
}

// Upload counts the call and delegates to the wrapped Storage, or fails for FailPath.
func (c *CountingUploadStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if c.FailPath != "" && req.Path == c.FailPath {
		return nil, errtypes.PermissionDenied("injected upload failure for " + req.Path)
	}
	atomic.AddInt32(&c.Uploads, 1)
	return c.Storage.Upload(ctx, req)
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

// NotModifiedStorage returns errtypes.NotModified when the etag of the file is
// in IfNoneMatch, like the CS3 metadata storage. metadata.Disk ignores
// IfNoneMatch and always sends the full file.
type NotModifiedStorage struct {
	metadata.Storage
}

// Download delegates to the wrapped Storage and returns errtypes.NotModified when the etag matches.
func (n *NotModifiedStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	res, err := n.Storage.Download(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, etag := range req.IfNoneMatch {
		if etag != "" && etag == res.Etag {
			return nil, errtypes.NotModified("not modified")
		}
	}
	return res, nil
}

// ToggleNotFoundDownloadStorage fails each Download with errtypes.NotFound
// while Fail is true. Use it to simulate a temporary NotFound after the cache
// has read the file.
type ToggleNotFoundDownloadStorage struct {
	metadata.Storage
	Fail atomic.Bool
}

// Download fails with errtypes.NotFound while Fail is true, else it delegates.
func (t *ToggleNotFoundDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if t.Fail.Load() {
		return nil, errtypes.NotFound("injected temporary not found: " + req.Path)
	}
	return t.Storage.Download(ctx, req)
}
