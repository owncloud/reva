// Package helpers holds metadata.Storage test doubles shared by the jsoncs3
// share manager's cache test suites (providercache, sharecache,
// receivedsharecache), which all exercise the same CAS-retry contract.
package helpers

import (
	"context"
	"sync/atomic"

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
