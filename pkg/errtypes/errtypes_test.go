package errtypes_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/owncloud/reva/v2/pkg/errtypes"
)

func TestUnmappedStatusKeptInMessage(t *testing.T) {
	err := errtypes.NewErrtypeFromHTTPStatusCode(http.StatusInternalServerError, "/path")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(http.StatusInternalServerError)) {
		t.Errorf("error %q does not mention HTTP status code %d", err.Error(), http.StatusInternalServerError)
	}
}
