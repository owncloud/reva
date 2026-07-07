package ocmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	ocmcore "github.com/cs3org/go-cs3apis/cs3/ocm/core/v1beta1"
	invitepb "github.com/cs3org/go-cs3apis/cs3/ocm/invite/v1beta1"
	ocmprovider "github.com/cs3org/go-cs3apis/cs3/ocm/provider/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	jsonauth "github.com/owncloud/reva/v2/pkg/ocm/provider/authorizer/json"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	cs3mocks "github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

// validShareBody returns a well-formed createShareRequest JSON body for the given sender.
func validShareBody(sender string) string {
	return `{
		"shareWith": "einstein@localhost:9200",
		"name": "test-share.pdf",
		"providerId": "provider-123",
		"owner": "` + sender + `",
		"sender": "` + sender + `",
		"shareType": "user",
		"resourceType": "file",
		"protocol": {
			"webdav": {
				"sharedSecret": "shared-secret-value",
				"permissions": ["read"],
				"uri": "https://unknown.example/dav/files"
			}
		}
	}`
}

// newTestHandler wires gc into a sharesHandler without calling init().
func newTestHandler(gc *cs3mocks.GatewayAPIClient) *sharesHandler {
	pool.RemoveSelector("GatewaySelector" + "any")
	sel := pool.GetSelector[gateway.GatewayAPIClient](
		"GatewaySelector",
		"any",
		func(cc grpc.ClientConnInterface) gateway.GatewayAPIClient { return gc },
	)
	return &sharesHandler{gatewaySelector: sel, allowHTTP: true}
}

// trustedProvidersFile writes a providers.json containing only trustedDomain and returns its path.
func trustedProvidersFile(t *testing.T, trustedDomain string) string {
	t.Helper()
	type svc struct {
		Endpoint struct {
			Type struct{ Name string } `json:"type"`
			Path string                `json:"path"`
		} `json:"endpoint"`
		Host string `json:"host"`
	}
	type prov struct {
		Domain   string `json:"domain"`
		Services []svc  `json:"services"`
	}
	providers := []prov{{
		Domain: trustedDomain,
		Services: []svc{{
			Endpoint: struct {
				Type struct{ Name string } `json:"type"`
				Path string                `json:"path"`
			}{Type: struct{ Name string }{"OCM"}, Path: "https://" + trustedDomain + "/ocm/"},
			Host: trustedDomain,
		}},
	}}
	b, _ := json.Marshal(providers)
	f := filepath.Join(t.TempDir(), "providers.json")
	_ = os.WriteFile(f, b, 0600)
	return f
}

// setupInfoByDomain registers the real GetInfoByDomain behaviour on gc.
func setupInfoByDomain(gc *cs3mocks.GatewayAPIClient, t *testing.T, providersFile string) {
	t.Helper()
	gc.EXPECT().GetInfoByDomain(mock.Anything, mock.Anything).
		RunAndReturn(realGetInfoByDomain(t, providersFile))
}

// doShareRequest fires a POST /shares with the given body and returns the recorder.
func doShareRequest(t *testing.T, h *sharesHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/shares", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	h.CreateShare(w, req)
	return w
}

func realGetInfoByDomain(t *testing.T, providersFile string) func(context.Context, *ocmprovider.GetInfoByDomainRequest, ...grpc.CallOption) (*ocmprovider.GetInfoByDomainResponse, error) {
	t.Helper()
	auth, err := jsonauth.New(map[string]interface{}{"providers": providersFile})
	if err != nil {
		t.Fatalf("jsonauth.New: %v", err)
	}
	return func(ctx context.Context, req *ocmprovider.GetInfoByDomainRequest, _ ...grpc.CallOption) (*ocmprovider.GetInfoByDomainResponse, error) {
		info, err := auth.GetInfoByDomain(ctx, req.Domain)
		if err != nil {
			return &ocmprovider.GetInfoByDomainResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_NOT_FOUND, Message: err.Error()},
			}, nil
		}
		return &ocmprovider.GetInfoByDomainResponse{
			Status:       &rpc.Status{Code: rpc.Code_CODE_OK},
			ProviderInfo: info,
		}, nil
	}
}

// TestCreateShare_ProviderGate_OCISDEV756 verifies untrusted providers are rejected before share creation.
func TestCreateShare_ProviderGate_OCISDEV756(t *testing.T) {
	const trustedDomain = "trusted-partner.example"
	providersFile := trustedProvidersFile(t, trustedDomain)

	t.Run("untrusted provider must be rejected HTTP 401 (OCISDEV-756)", func(t *testing.T) {
		gc := &cs3mocks.GatewayAPIClient{}
		setupInfoByDomain(gc, t, providersFile)
		gc.On("GetUser", mock.Anything, mock.Anything).Maybe().Return(
			&userpb.GetUserResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				User:   &userpb.User{Id: &userpb.UserId{OpaqueId: "einstein"}},
			}, nil)
		gc.On("CreateOCMCoreShare", mock.Anything, mock.Anything).Maybe().Return(
			&ocmcore.CreateOCMCoreShareResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				Id:     "share-id",
			}, nil)

		w := doShareRequest(t, newTestHandler(gc), validShareBody("alice@unknown.example"))

		if w.Code != http.StatusUnauthorized {
			t.Errorf("unknown provider: got HTTP %d, want 401", w.Code)
		}
		gc.AssertNotCalled(t, "CreateOCMCoreShare", mock.Anything, mock.Anything)
	})

	t.Run("trusted domain but no invite relationship must be rejected HTTP 401", func(t *testing.T) {
		gc := &cs3mocks.GatewayAPIClient{}
		setupInfoByDomain(gc, t, providersFile)
		gc.On("GetUser", mock.Anything, mock.Anything).Return(
			&userpb.GetUserResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				User:   &userpb.User{Id: &userpb.UserId{OpaqueId: "einstein"}},
			}, nil)
		gc.On("GetAcceptedUser", mock.Anything, mock.Anything).Return(
			&invitepb.GetAcceptedUserResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_NOT_FOUND, Message: "no invite relationship"},
			}, nil)
		gc.On("CreateOCMCoreShare", mock.Anything, mock.Anything).Maybe().Return(
			&ocmcore.CreateOCMCoreShareResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				Id:     "share-id",
			}, nil)

		w := doShareRequest(t, newTestHandler(gc), validShareBody("alice@"+trustedDomain))

		if w.Code != http.StatusUnauthorized {
			t.Errorf("no invite relationship: got HTTP %d, want 401", w.Code)
		}
		gc.AssertNotCalled(t, "CreateOCMCoreShare", mock.Anything, mock.Anything)
	})

	t.Run("trusted provider with invite relationship proceeds to persistence", func(t *testing.T) {
		gc := &cs3mocks.GatewayAPIClient{}
		setupInfoByDomain(gc, t, providersFile)
		gc.On("GetUser", mock.Anything, mock.Anything).Return(
			&userpb.GetUserResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				User:   &userpb.User{Id: &userpb.UserId{OpaqueId: "einstein"}},
			}, nil)
		gc.On("GetAcceptedUser", mock.Anything, mock.Anything).Return(
			&invitepb.GetAcceptedUserResponse{
				Status:     &rpc.Status{Code: rpc.Code_CODE_OK},
				RemoteUser: &userpb.User{Id: &userpb.UserId{OpaqueId: "alice", Idp: "trusted-partner.example"}},
			}, nil)
		gc.On("CreateOCMCoreShare", mock.Anything, mock.Anything).Return(
			&ocmcore.CreateOCMCoreShareResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				Id:     "share-id",
			}, nil)

		w := doShareRequest(t, newTestHandler(gc), validShareBody("alice@"+trustedDomain))

		gc.AssertCalled(t, "CreateOCMCoreShare", mock.Anything, mock.Anything)
		_ = w
	})
}
