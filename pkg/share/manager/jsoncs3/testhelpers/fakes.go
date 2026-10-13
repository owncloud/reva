// Package helpers holds metadata.Storage test doubles shared by the jsoncs3
// share manager's cache test suites (providercache, sharecache,
// receivedsharecache), which all exercise the same CAS-retry contract.
package helpers

import (
	"context"
	"sync"
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

// ConflictThenNotFoundStorage simulates a conflict whose post-conflict resync
// finds the backing file already deleted.
type ConflictThenNotFoundStorage struct {
	metadata.Storage
	Downloads   int32
	Uploads     int32
	InitialData []byte
	InitialEtag string

	mu      sync.Mutex
	content []byte
}

func (c *ConflictThenNotFoundStorage) MakeDirIfNotExist(_ context.Context, _ string) error {
	return nil
}

func (c *ConflictThenNotFoundStorage) Download(_ context.Context, _ metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if atomic.AddInt32(&c.Downloads, 1) == 1 {
		return &metadata.DownloadResponse{Content: c.InitialData, Etag: c.InitialEtag}, nil
	}
	return nil, errtypes.NotFound("injected: file deleted externally")
}

func (c *ConflictThenNotFoundStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if atomic.AddInt32(&c.Uploads, 1) == 1 {
		return nil, errtypes.Aborted("injected")
	}
	c.mu.Lock()
	c.content = req.Content
	c.mu.Unlock()
	return &metadata.UploadResponse{Etag: "etag2"}, nil
}

// UploadedContent returns whatever the caller eventually uploaded.
func (c *ConflictThenNotFoundStorage) UploadedContent() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content
}
