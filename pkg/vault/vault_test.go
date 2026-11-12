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
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"github.com/owncloud/reva/v2/pkg/permission"
	"github.com/owncloud/reva/v2/pkg/utils"
	cs3mocks "github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

func vaultModeRequest(userID string) interface{} {
	return mock.MatchedBy(func(r *permissions.CheckPermissionRequest) bool {
		return r.GetPermission() == permission.VaultMode && r.GetSubjectRef().GetUserId().GetOpaqueId() == userID
	})
}

func checkPermissionResponse(code rpc.Code) *permissions.CheckPermissionResponse {
	return &permissions.CheckPermissionResponse{Status: &rpc.Status{Code: code}}
}

func staticClient(c PermissionClient) ClientFunc {
	return func() (PermissionClient, error) { return c, nil }
}

func TestIsVaultSpace(t *testing.T) {
	tests := []struct {
		name    string
		spaceID string
		want    bool
	}{
		{"vault space", utils.VaultStorageProviderID + "$space-id", true},
		{"regular space", "storage-id$space-id", false},
		{"unparsable space id fails closed", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsVaultSpace(tt.spaceID); got != tt.want {
				t.Errorf("IsVaultSpace(%q) = %v, want %v", tt.spaceID, got, tt.want)
			}
		})
	}
}

func TestPermissionGranted(t *testing.T) {
	tests := []struct {
		name    string
		status  *rpc.Status
		want    bool
		wantErr bool
	}{
		{"ok grants", &rpc.Status{Code: rpc.Code_CODE_OK}, true, false},
		{"permission denied denies", &rpc.Status{Code: rpc.Code_CODE_PERMISSION_DENIED}, false, false},
		{"not found fails closed", &rpc.Status{Code: rpc.Code_CODE_NOT_FOUND}, false, true},
		{"internal fails closed", &rpc.Status{Code: rpc.Code_CODE_INTERNAL}, false, true},
		{"missing status fails closed", nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PermissionGranted(tt.status)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PermissionGranted() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("PermissionGranted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasVaultMode(t *testing.T) {
	tests := []struct {
		name    string
		res     *permissions.CheckPermissionResponse
		err     error
		want    bool
		wantErr bool
	}{
		{"ok grants", checkPermissionResponse(rpc.Code_CODE_OK), nil, true, false},
		{"permission denied denies", checkPermissionResponse(rpc.Code_CODE_PERMISSION_DENIED), nil, false, false},
		{"internal status fails closed", checkPermissionResponse(rpc.Code_CODE_INTERNAL), nil, false, true},
		{"transport error fails closed", nil, errors.New("unavailable"), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gwc := &cs3mocks.GatewayAPIClient{}
			gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(tt.res, tt.err)

			got, err := HasVaultMode(context.Background(), gwc, &userpb.UserId{OpaqueId: "alice"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("HasVaultMode() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("HasVaultMode() = %v, want %v", got, tt.want)
			}
			gwc.AssertExpectations(t)
		})
	}
}

func TestCheckerCache(t *testing.T) {
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
			c := NewChecker(staticClient(gwc), WithCache(10, time.Minute))
			alice := &userpb.UserId{OpaqueId: "alice"}

			for i := range 2 {
				got, err := c.HasVaultMode(context.Background(), alice)
				if (err != nil) != tt.wantErr {
					t.Fatalf("call %d: HasVaultMode() error = %v, wantErr %v", i, err, tt.wantErr)
				}
				if got != tt.want {
					t.Errorf("call %d: HasVaultMode() = %v, want %v", i, got, tt.want)
				}
			}

			// a second call must not reach the gateway when the first answer was cached
			wantCalls := 2
			if tt.wantCached {
				wantCalls = 1
			}
			gwc.AssertNumberOfCalls(t, "CheckPermission", wantCalls)
		})
	}
}

func TestCheckerCacheExpires(t *testing.T) {
	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(checkPermissionResponse(rpc.Code_CODE_OK), nil)
	c := NewChecker(staticClient(gwc), WithCache(10, 20*time.Millisecond))
	alice := &userpb.UserId{OpaqueId: "alice"}

	_, _ = c.HasVaultMode(context.Background(), alice)
	time.Sleep(50 * time.Millisecond)
	_, _ = c.HasVaultMode(context.Background(), alice)

	gwc.AssertNumberOfCalls(t, "CheckPermission", 2)
}

func TestCheckerWithoutCache(t *testing.T) {
	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(checkPermissionResponse(rpc.Code_CODE_OK), nil)
	c := NewChecker(staticClient(gwc))
	alice := &userpb.UserId{OpaqueId: "alice"}

	_, _ = c.HasVaultMode(context.Background(), alice)
	_, _ = c.HasVaultMode(context.Background(), alice)

	gwc.AssertNumberOfCalls(t, "CheckPermission", 2)
}

func TestCheckersDoNotShareTheCache(t *testing.T) {
	granting := &cs3mocks.GatewayAPIClient{}
	granting.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(checkPermissionResponse(rpc.Code_CODE_OK), nil)
	denying := &cs3mocks.GatewayAPIClient{}
	denying.On("CheckPermission", mock.Anything, vaultModeRequest("alice")).Return(checkPermissionResponse(rpc.Code_CODE_PERMISSION_DENIED), nil)
	alice := &userpb.UserId{OpaqueId: "alice"}

	first := NewChecker(staticClient(granting), WithCache(10, time.Minute))
	second := NewChecker(staticClient(denying), WithCache(10, time.Minute))

	if got, _ := first.HasVaultMode(context.Background(), alice); !got {
		t.Fatalf("first HasVaultMode() = false, want true")
	}
	if got, _ := second.HasVaultMode(context.Background(), alice); got {
		t.Errorf("second HasVaultMode() = true, want false: the checkers share the cache")
	}
}

func TestCheckerClientError(t *testing.T) {
	c := NewChecker(func() (PermissionClient, error) { return nil, errors.New("no gateway") }, WithCache(10, time.Minute))

	got, err := c.HasVaultMode(context.Background(), &userpb.UserId{OpaqueId: "alice"})
	if err == nil || got {
		t.Errorf("HasVaultMode() = %v, %v, want false and an error", got, err)
	}
}

func TestFilterUsers(t *testing.T) {
	// alice and every "ok-" user hold the permission, bob does not, and the check for carol fails
	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, mock.Anything).Return(func(_ context.Context, r *permissions.CheckPermissionRequest, _ ...grpc.CallOption) (*permissions.CheckPermissionResponse, error) {
		switch id := r.GetSubjectRef().GetUserId().GetOpaqueId(); {
		case id == "carol":
			return nil, errors.New("unavailable")
		case id == "bob":
			return checkPermissionResponse(rpc.Code_CODE_PERMISSION_DENIED), nil
		default:
			return checkPermissionResponse(rpc.Code_CODE_OK), nil
		}
	})

	ids := []*userpb.UserId{{OpaqueId: "alice"}, {OpaqueId: "bob"}, {OpaqueId: "carol"}}
	for i := range 50 {
		ids = append(ids, &userpb.UserId{OpaqueId: fmt.Sprintf("ok-%02d", i)})
	}

	var failed []string
	got := NewChecker(staticClient(gwc)).FilterUsers(context.Background(), ids, func(id *userpb.UserId, err error) {
		failed = append(failed, id.GetOpaqueId())
	})

	want := []string{"alice"}
	for i := range 50 {
		want = append(want, fmt.Sprintf("ok-%02d", i))
	}
	gotIDs := make([]string, 0, len(got))
	for _, id := range got {
		gotIDs = append(gotIDs, id.GetOpaqueId())
	}
	if !slices.Equal(gotIDs, want) {
		t.Errorf("FilterUsers() = %v, want %v", gotIDs, want)
	}
	if !slices.Equal(failed, []string{"carol"}) {
		t.Errorf("onError called for %v, want [carol]", failed)
	}
	gwc.AssertNumberOfCalls(t, "CheckPermission", len(ids))
}

func TestFilterUsersConcurrencyLimit(t *testing.T) {
	const limit = 3
	var running, maxRunning atomic.Int32
	gwc := &cs3mocks.GatewayAPIClient{}
	gwc.On("CheckPermission", mock.Anything, mock.Anything).Return(func(_ context.Context, _ *permissions.CheckPermissionRequest, _ ...grpc.CallOption) (*permissions.CheckPermissionResponse, error) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return checkPermissionResponse(rpc.Code_CODE_OK), nil
	})

	ids := make([]*userpb.UserId, 0, 20)
	for i := range 20 {
		ids = append(ids, &userpb.UserId{OpaqueId: fmt.Sprintf("user-%02d", i)})
	}
	got := NewChecker(staticClient(gwc), WithConcurrency(limit)).FilterUsers(context.Background(), ids, nil)

	if len(got) != len(ids) {
		t.Errorf("FilterUsers() returned %d users, want %d", len(got), len(ids))
	}
	if m := maxRunning.Load(); m > limit {
		t.Errorf("%d checks ran at the same time, want at most %d", m, limit)
	}
}
