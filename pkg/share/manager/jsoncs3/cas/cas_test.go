package cas

import (
	"errors"
	"testing"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	grpccodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsConflict(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"aborted":             {errtypes.Aborted("etag changed"), true},
		"precondition failed": {errtypes.PreconditionFailed("etag changed"), true},
		"already exists":      {errtypes.AlreadyExists("file created concurrently"), true},
		"too early":           {errtypes.TooEarly("upload in progress"), true},
		"not found":           {errtypes.NotFound("nope"), false},
		"not modified":        {errtypes.NotModified("nope"), false},
		"generic error":       {errors.New("boom"), false},
		"nil error":           {nil, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsConflict(tc.err); got != tc.want {
				t.Errorf("IsConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsSyncTransient(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"too early":        {errtypes.TooEarly("upload in progress"), true},
		"grpc unavailable": {status.Error(grpccodes.Unavailable, "down"), true},
		"not found":        {errtypes.NotFound("nope"), false},
		"generic error":    {errors.New("boom"), false},
		"nil error":        {nil, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsSyncTransient(tc.err); got != tc.want {
				t.Errorf("IsSyncTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsTransientGRPCStatus(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"unavailable":        {status.Error(grpccodes.Unavailable, "down"), true},
		"deadline exceeded":  {status.Error(grpccodes.DeadlineExceeded, "slow"), true},
		"canceled":           {status.Error(grpccodes.Canceled, "canceled"), true},
		"resource exhausted": {status.Error(grpccodes.ResourceExhausted, "busy"), true},
		"not a grpc status":  {errors.New("boom"), false},
		"other grpc code":    {status.Error(grpccodes.NotFound, "nope"), false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsTransientGRPCStatus(tc.err); got != tc.want {
				t.Errorf("IsTransientGRPCStatus(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDecideNotFoundReset(t *testing.T) {
	tests := map[string]struct {
		resetOnNotFound bool
		hadPriorEtag    bool
		want            bool
	}{
		"cold sync without etag":          {resetOnNotFound: true, hadPriorEtag: false, want: true},
		"sync with etag":                  {resetOnNotFound: true, hadPriorEtag: true, want: true},
		"resync after conflict, no etag":  {resetOnNotFound: false, hadPriorEtag: false, want: true},
		"resync after conflict with etag": {resetOnNotFound: false, hadPriorEtag: true, want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := DecideNotFoundReset(tc.resetOnNotFound, tc.hadPriorEtag); got != tc.want {
				t.Errorf("DecideNotFoundReset(%v, %v) = %v, want %v", tc.resetOnNotFound, tc.hadPriorEtag, got, tc.want)
			}
		})
	}
}
