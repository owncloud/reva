// Copyright 2018-2023 CERN
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

package ocmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	invitepb "github.com/cs3org/go-cs3apis/cs3/ocm/invite/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	cs3mocks "github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

// newInvitesTestHandler wires gc into an invitesHandler without calling init().
func newInvitesTestHandler(gc *cs3mocks.GatewayAPIClient) *invitesHandler {
	pool.RemoveSelector("GatewaySelector" + "any")
	sel := pool.GetSelector[gateway.GatewayAPIClient](
		"GatewaySelector",
		"any",
		func(cc grpc.ClientConnInterface) gateway.GatewayAPIClient { return gc },
	)
	return &invitesHandler{gatewaySelector: sel}
}

// validAcceptInviteBody returns a well-formed acceptInviteRequest JSON body
// for the given recipientProvider domain.
func validAcceptInviteBody(recipientProvider string) string {
	return `{
		"token": "invite-token-123",
		"userID": "attacker",
		"recipientProvider": "` + recipientProvider + `",
		"name": "Evil User",
		"email": "evil@` + recipientProvider + `"
	}`
}

// TestAcceptInvite_ProviderGate_OCISDEV756 is the regression test for OCISDEV-756
// on the invite-accepted endpoint.
//
// GetInfoByDomain is called FIRST — unknown recipientProvider domains must be
// rejected 401 before AcceptInvite is called. Without the GetInfoByDomain
// pre-check in the handler the untrusted case passes through (HTTP 200 or 404)
// and this test FAILS — proving the vulnerability is present.
func TestAcceptInvite_ProviderGate_OCISDEV756(t *testing.T) {
	const trustedDomain = "trusted-partner.example"
	providersFile := trustedProvidersFile(t, trustedDomain)

	t.Run("untrusted recipientProvider must be rejected HTTP 401 (OCISDEV-756)", func(t *testing.T) {
		gc := &cs3mocks.GatewayAPIClient{}
		gc.EXPECT().GetInfoByDomain(mock.Anything, mock.Anything).
			RunAndReturn(realGetInfoByDomain(t, providersFile))
		// Stubs for methods that should NOT be reached.
		gc.On("AcceptInvite", mock.Anything, mock.Anything).Maybe().Return(
			&invitepb.AcceptInviteResponse{
				Status: &rpc.Status{Code: rpc.Code_CODE_OK},
				UserId: &userpb.UserId{OpaqueId: "attacker"},
			}, nil)

		h := newInvitesTestHandler(gc)

		req := httptest.NewRequest(http.MethodPost, "/invite-accepted",
			strings.NewReader(validAcceptInviteBody("evil-attacker.example")))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()

		h.AcceptInvite(w, req)

		// Without the GetInfoByDomain pre-check this is NOT 401 — test FAILS, exposing OCISDEV-756.
		if w.Code != http.StatusUnauthorized {
			t.Errorf("untrusted recipientProvider: got HTTP %d, want 401 — OCISDEV-756 vulnerability present", w.Code)
		}
		gc.AssertNotCalled(t, "AcceptInvite", mock.Anything, mock.Anything)
	})

	t.Run("trusted recipientProvider proceeds to AcceptInvite", func(t *testing.T) {
		gc := &cs3mocks.GatewayAPIClient{}
		gc.EXPECT().GetInfoByDomain(mock.Anything, mock.Anything).
			RunAndReturn(realGetInfoByDomain(t, providersFile))
		gc.On("AcceptInvite", mock.Anything, mock.Anything).Return(
			&invitepb.AcceptInviteResponse{
				Status:      &rpc.Status{Code: rpc.Code_CODE_OK},
				UserId:      &userpb.UserId{OpaqueId: "trusted-user"},
				DisplayName: "Trusted User",
				Email:       "user@trusted-partner.example",
			}, nil)

		h := newInvitesTestHandler(gc)

		req := httptest.NewRequest(http.MethodPost, "/invite-accepted",
			strings.NewReader(validAcceptInviteBody(trustedDomain)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()

		h.AcceptInvite(w, req)

		gc.AssertCalled(t, "AcceptInvite", mock.Anything, mock.Anything)
	})
}
