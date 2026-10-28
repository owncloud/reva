package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bluele/gcache"
	gatewayv1beta1 "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/auth/scope"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/permission"
	"github.com/owncloud/reva/v2/pkg/token/manager/jwt"
	_ "github.com/owncloud/reva/v2/pkg/token/manager/loader"
	cs3mocks "github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
	"github.com/stretchr/testify/mock"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
)

const testJWTSecret = "vault-test-secret"

// useGateway replaces the gateway client and the VaultMode cache for one test.
func useGateway(t *testing.T, gwc gatewayv1beta1.GatewayAPIClient) {
	t.Helper()
	initVaultModeCache(10, time.Minute)
	oldGetter, oldCache := getGatewayClient, vaultModeCache
	getGatewayClient = func(string) (gatewayv1beta1.GatewayAPIClient, error) { return gwc, nil }
	vaultModeCache = gcache.New(10).LRU().Build()
	t.Cleanup(func() {
		getGatewayClient, vaultModeCache = oldGetter, oldCache
	})
}

func vaultModeRequest(userID string) interface{} {
	return mock.MatchedBy(func(r *permissions.CheckPermissionRequest) bool {
		return r.GetPermission() == permission.VaultMode && r.GetSubjectRef().GetUserId().GetOpaqueId() == userID
	})
}

func checkPermissionResponse(code rpc.Code) *permissions.CheckPermissionResponse {
	return &permissions.CheckPermissionResponse{Status: &rpc.Status{Code: code}}
}

func TestHasVaultMode(t *testing.T) {
	tests := []struct {
		name       string
		res        *permissions.CheckPermissionResponse
		err        error
		want       bool
		wantErr    bool
		wantCached bool
	}{
		{"ok grants and is cached", checkPermissionResponse(rpc.Code_CODE_OK), nil, true, false, true},
		{"permission denied denies and is cached", checkPermissionResponse(rpc.Code_CODE_PERMISSION_DENIED), nil, false, false, true},
		{"internal status fails closed and is not cached", checkPermissionResponse(rpc.Code_CODE_INTERNAL), nil, false, true, false},
		{"transport error fails closed and is not cached", nil, errors.New("unavailable"), false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwc := &cs3mocks.GatewayAPIClient{}
			gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(tt.res, tt.err)
			useGateway(t, gwc)
			u := &userpb.User{Id: &userpb.UserId{OpaqueId: "alice"}}

			got, err := hasVaultMode(context.Background(), u, "")
			if (err != nil) != tt.wantErr {
				t.Fatalf("hasVaultMode() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("hasVaultMode() = %v, want %v", got, tt.want)
			}

			// a second call must not reach the gateway when the first answer was cached
			got, _ = hasVaultMode(context.Background(), u, "")
			if got != tt.want {
				t.Errorf("second hasVaultMode() = %v, want %v", got, tt.want)
			}
			wantCalls := 2
			if tt.wantCached {
				wantCalls = 1
			}
			gwc.AssertNumberOfCalls(t, "CheckPermission", wantCalls)
		})
	}
}

func TestUnaryInterceptorVaultModeRequired(t *testing.T) {
	alice := &userpb.User{Id: &userpb.UserId{OpaqueId: "alice", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	carol := &userpb.User{Id: &userpb.UserId{OpaqueId: "carol", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	service := &userpb.User{Id: &userpb.UserId{OpaqueId: "service", Type: userpb.UserType_USER_TYPE_SERVICE}}

	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(checkPermissionResponse(rpc.Code_CODE_OK), nil)
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("carol")).Return(checkPermissionResponse(rpc.Code_CODE_PERMISSION_DENIED), nil)
	useGateway(t, gwc)

	interceptor, err := NewUnary(map[string]interface{}{
		"token_managers":      map[string]interface{}{"jwt": map[string]interface{}{"secret": testJWTSecret}},
		"vault_mode_required": true,
	}, nil, noop.NewTracerProvider())
	if err != nil {
		t.Fatalf("NewUnary() error = %v", err)
	}

	mgr, err := jwt.New(map[string]interface{}{"secret": testJWTSecret})
	if err != nil {
		t.Fatalf("jwt.New() error = %v", err)
	}
	ownerScope, err := scope.AddOwnerScope(nil)
	if err != nil {
		t.Fatalf("AddOwnerScope() error = %v", err)
	}

	tests := []struct {
		name        string
		user        *userpb.User
		wantHandled bool
	}{
		{"user with vault mode is allowed", alice, true},
		{"user without vault mode is denied", carol, false},
		{"service account is not checked", service, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tkn, err := mgr.MintToken(context.Background(), tt.user, ownerScope)
			if err != nil {
				t.Fatalf("MintToken() error = %v", err)
			}
			ctx := ctxpkg.ContextSetToken(context.Background(), tkn)

			handled := false
			handler := func(ctx context.Context, req interface{}) (interface{}, error) {
				handled = true
				return &provider.StatResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil
			}
			res, err := interceptor(ctx, &provider.StatRequest{}, &grpc.UnaryServerInfo{FullMethod: "/cs3.storage.provider.v1beta1.ProviderAPI/Stat"}, handler)
			if err != nil {
				t.Fatalf("interceptor() error = %v", err)
			}
			if handled != tt.wantHandled {
				t.Fatalf("handler called = %v, want %v", handled, tt.wantHandled)
			}
			if !tt.wantHandled && res.(*provider.StatResponse).GetStatus().GetCode() != rpc.Code_CODE_PERMISSION_DENIED {
				t.Errorf("status = %v, want PERMISSION_DENIED", res.(*provider.StatResponse).GetStatus().GetCode())
			}
		})
	}
	gwc.AssertNotCalled(t, "CheckPermission", mock.Anything, vaultModeRequest("service"))
}
