// Package cas classifies CAS-conflict errors shared by providercache, sharecache, and receivedsharecache.
package cas

import (
	"time"

	backoff "github.com/cenkalti/backoff/v5"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewBackoff returns the backoff policy shared by every CAS-retry loop.
func NewBackoff() *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Microsecond
	bo.Multiplier = 2.0
	bo.RandomizationFactor = 1.0
	bo.MaxInterval = 50 * time.Millisecond
	return bo
}

// IsConflict reports whether err is a CAS conflict the caller should resync and retry on.
func IsConflict(err error) bool {
	switch err.(type) {
	case errtypes.Aborted:
		// If-Match etag check failed
		return true
	case errtypes.PreconditionFailed:
		// same as Aborted; some server paths return this instead
		return true
	case errtypes.AlreadyExists:
		// If-None-Match=* conflict: cache thought there was no file yet
		return true
	case errtypes.TooEarly:
		// an upload is already in progress for this path
		return true
	default:
		return false
	}
}

// IsSyncTransient reports whether a sync's Download error is worth retrying.
func IsSyncTransient(err error) bool {
	_, isTooEarly := err.(errtypes.IsTooEarly)
	return isTooEarly || IsTransientGRPCStatus(err)
}

// IsTransientGRPCStatus catches raw gRPC transport errors that metadata.CS3 never wraps in errtypes.
func IsTransientGRPCStatus(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case grpccodes.Unavailable, grpccodes.DeadlineExceeded, grpccodes.Canceled, grpccodes.ResourceExhausted:
		return true
	default:
		return false
	}
}
