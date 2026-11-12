// Copyright 2018-2026 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package vault

import (
	"context"
	"errors"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/permission"
	vaultpkg "github.com/owncloud/reva/v2/pkg/vault"
	cs3mocks "github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const statMethod = "/cs3.storage.provider.v1beta1.ProviderAPI/Stat"

var (
	alice   = &userpb.User{Id: &userpb.UserId{OpaqueId: "alice", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	carol   = &userpb.User{Id: &userpb.UserId{OpaqueId: "carol", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	dave    = &userpb.User{Id: &userpb.UserId{OpaqueId: "dave", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	service = &userpb.User{Id: &userpb.UserId{OpaqueId: "service", Type: userpb.UserType_USER_TYPE_SERVICE}}
)

func vaultModeRequest(userID string) interface{} {
	return mock.MatchedBy(func(r *permissions.CheckPermissionRequest) bool {
		return r.GetPermission() == permission.VaultMode && r.GetSubjectRef().GetUserId().GetOpaqueId() == userID
	})
}

// gateway returns a client in which alice holds the VaultMode permission, carol does not,
// and the check for dave fails.
func gateway() *cs3mocks.GatewayAPIClient {
	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).
		Return(&permissions.CheckPermissionResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil)
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("carol")).
		Return(&permissions.CheckPermissionResponse{Status: &rpc.Status{Code: rpc.Code_CODE_PERMISSION_DENIED}}, nil)
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("dave")).
		Return(nil, errors.New("unavailable"))
	return gwc
}

func clientFor(gwc *cs3mocks.GatewayAPIClient) vaultpkg.ClientFunc {
	return func() (vaultpkg.PermissionClient, error) { return gwc, nil }
}

func userContext(u *userpb.User, mfa bool) context.Context {
	ctx := context.Background()
	if u != nil {
		ctx = ctxpkg.ContextSetUser(ctx, u)
	}
	if mfa {
		ctx = ctxpkg.SetMFA(ctx)
	}
	return ctx
}

func TestUnary(t *testing.T) {
	both := map[string]interface{}{"mfa_required": true, "vault_mode_required": true}
	tests := []struct {
		name        string
		conf        map[string]interface{}
		user        *userpb.User
		mfa         bool
		wantHandled bool
		wantMsg     string
	}{
		{"user with MFA and vault mode is allowed", both, alice, true, true, ""},
		{"user without vault mode is denied", both, carol, true, false, vaultModeRequiredMsg},
		{"failed vault mode check is denied", both, dave, true, false, vaultModeRequiredMsg},
		{"user without MFA is denied before the vault mode check", both, alice, false, false, mfaRequiredMsg},
		{"service account is not checked", both, service, false, true, ""},
		{"request without a user passes", both, nil, false, true, ""},
		{"MFA only: user without vault mode is allowed", map[string]interface{}{"mfa_required": true}, carol, true, true, ""},
		{"vault mode only: user without MFA is allowed", map[string]interface{}{"vault_mode_required": true}, alice, false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwc := gateway()
			interceptor, prio, err := newUnary(tt.conf, clientFor(gwc))
			if err != nil {
				t.Fatalf("newUnary() error = %v", err)
			}
			if prio != defaultPriority {
				t.Errorf("priority = %d, want %d", prio, defaultPriority)
			}

			handled := false
			handler := func(ctx context.Context, req interface{}) (interface{}, error) {
				handled = true
				return &provider.StatResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil
			}
			res, err := interceptor(userContext(tt.user, tt.mfa), &provider.StatRequest{}, &grpc.UnaryServerInfo{FullMethod: statMethod}, handler)
			if err != nil {
				t.Fatalf("interceptor() error = %v", err)
			}
			if handled != tt.wantHandled {
				t.Fatalf("handler called = %v, want %v", handled, tt.wantHandled)
			}
			st := res.(*provider.StatResponse).GetStatus()
			if !tt.wantHandled {
				if st.GetCode() != rpc.Code_CODE_PERMISSION_DENIED || st.GetMessage() != tt.wantMsg {
					t.Errorf("status = %v %q, want PERMISSION_DENIED %q", st.GetCode(), st.GetMessage(), tt.wantMsg)
				}
			}
			gwc.AssertNotCalled(t, "CheckPermission", mock.Anything, vaultModeRequest("service"))
		})
	}
}

func TestUnaryUnknownRequestType(t *testing.T) {
	interceptor, _, err := newUnary(map[string]interface{}{"vault_mode_required": true}, clientFor(gateway()))
	if err != nil {
		t.Fatalf("newUnary() error = %v", err)
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	}
	_, err = interceptor(userContext(carol, true), &permissions.CheckPermissionRequest{}, &grpc.UnaryServerInfo{FullMethod: "/unknown"}, handler)
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("error code = %v, want PermissionDenied", status.Code(err))
	}
}

type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s serverStream) Context() context.Context { return s.ctx }

func TestStream(t *testing.T) {
	both := map[string]interface{}{"mfa_required": true, "vault_mode_required": true}
	tests := []struct {
		name        string
		user        *userpb.User
		mfa         bool
		wantHandled bool
		wantMsg     string
	}{
		{"user with MFA and vault mode is allowed", alice, true, true, ""},
		{"user without vault mode is denied", carol, true, false, vaultModeRequiredMsg},
		{"user without MFA is denied", alice, false, false, mfaRequiredMsg},
		{"service account is not checked", service, false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor, _, err := newStream(both, clientFor(gateway()))
			if err != nil {
				t.Fatalf("newStream() error = %v", err)
			}
			handled := false
			handler := func(srv interface{}, ss grpc.ServerStream) error {
				handled = true
				return nil
			}
			err = interceptor(nil, serverStream{ctx: userContext(tt.user, tt.mfa)}, &grpc.StreamServerInfo{FullMethod: statMethod}, handler)
			if handled != tt.wantHandled {
				t.Fatalf("handler called = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantHandled {
				if err != nil {
					t.Errorf("interceptor() error = %v", err)
				}
				return
			}
			if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied || st.Message() != tt.wantMsg {
				t.Errorf("error = %v, want PermissionDenied %q", err, tt.wantMsg)
			}
		})
	}
}

func TestInterceptorsHaveTheirOwnCache(t *testing.T) {
	granting := &cs3mocks.GatewayAPIClient{}
	granting.On("CheckPermission", mock.Anything, vaultModeRequest("carol")).
		Return(&permissions.CheckPermissionResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil)
	conf := map[string]interface{}{"vault_mode_required": true}

	first, _, err := newUnary(conf, clientFor(granting))
	if err != nil {
		t.Fatalf("newUnary() error = %v", err)
	}
	second, _, err := newUnary(conf, clientFor(gateway()))
	if err != nil {
		t.Fatalf("newUnary() error = %v", err)
	}

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return &provider.StatResponse{Status: &rpc.Status{Code: rpc.Code_CODE_OK}}, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: statMethod}
	res, _ := first(userContext(carol, false), &provider.StatRequest{}, info, handler)
	if code := res.(*provider.StatResponse).GetStatus().GetCode(); code != rpc.Code_CODE_OK {
		t.Fatalf("first interceptor: status = %v, want OK", code)
	}
	res, _ = second(userContext(carol, false), &provider.StatRequest{}, info, handler)
	if code := res.(*provider.StatResponse).GetStatus().GetCode(); code != rpc.Code_CODE_PERMISSION_DENIED {
		t.Errorf("second interceptor: status = %v, want PERMISSION_DENIED: the interceptors share the cache", code)
	}
}

func TestParseConfigDefaults(t *testing.T) {
	c, err := parseConfig(map[string]interface{}{"priority": 7, "vault_mode_cache_seconds": 5})
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if c.Priority != 7 || c.VaultModeCacheSeconds != 5 || c.VaultModeCacheSize != defaultVaultModeCacheSize {
		t.Errorf("parseConfig() = %+v", c)
	}
	if c.MFARequired || c.VaultModeRequired {
		t.Errorf("parseConfig() enables checks by default: %+v", c)
	}
}
