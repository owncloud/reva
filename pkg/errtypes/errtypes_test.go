package errtypes_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/owncloud/reva/v2/pkg/errtypes"
)

// TestNewErrtypeFromHTTPStatusCode_UnmappedStatusKeptInMessage proves that an
// HTTP status code with no explicit case still surfaces in the resulting
// error's message, instead of being discarded in a generic InternalError.
func TestNewErrtypeFromHTTPStatusCode_UnmappedStatusKeptInMessage(t *testing.T) {
	err := errtypes.NewErrtypeFromHTTPStatusCode(http.StatusHTTPVersionNotSupported, "upstream unreachable")
	if err == nil {
		t.Fatal("expected an error for an unmapped status code, got nil")
	}
	if !strings.Contains(err.Error(), "505") {
		t.Errorf("expected the unmapped status code 505 to appear in the error message, got %q", err.Error())
	}
}

// TestNewErrtypeFromHTTPStatusCode_GatewayStatusesAreUnavailable proves that
// 502/503/504 map to the typed, retry-worthy Unavailable instead of the
// permanent-failure InternalError bucket.
func TestNewErrtypeFromHTTPStatusCode_GatewayStatusesAreUnavailable(t *testing.T) {
	for _, code := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		err := errtypes.NewErrtypeFromHTTPStatusCode(code, "upstream unreachable")
		if _, ok := err.(errtypes.IsUnavailable); !ok {
			t.Errorf("status %d: expected errtypes.IsUnavailable, got %T: %v", code, err, err)
		}
	}
}
