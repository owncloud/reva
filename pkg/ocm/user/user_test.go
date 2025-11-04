package user

import (
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
)

func TestLocalUserFederatedID(t *testing.T) {
	tests := []struct {
		name         string
		id           *userpb.UserId
		domain       string
		wantOpaqueId string
		wantIdp      string
	}{
		{
			name:         "local same idp",
			id:           &userpb.UserId{Type: userpb.UserType_USER_TYPE_FEDERATED, Idp: "https://ocis.cloud.io", OpaqueId: "id"},
			domain:       "ocis.cloud.io",
			wantOpaqueId: "id@ocis.cloud.io",
			wantIdp:      "https://ocis.cloud.io",
		},
		{
			name:         "remote different idp",
			id:           &userpb.UserId{Type: userpb.UserType_USER_TYPE_FEDERATED, Idp: "https://ocis.cloud.io", OpaqueId: "id"},
			domain:       "idp.cloud.io",
			wantOpaqueId: "id@ocis.cloud.io",
			wantIdp:      "https://idp.cloud.io",
		},
		{
			name:         "opaque contains idp, protocol in domain",
			id:           &userpb.UserId{Type: userpb.UserType_USER_TYPE_FEDERATED, Idp: "https://ocis.cloud.io", OpaqueId: "id"},
			domain:       "https://ocis.cloud.io",
			wantOpaqueId: "id@ocis.cloud.io",
			wantIdp:      "https://ocis.cloud.io",
		},
		{
			name:         "opaque contains idp, protocol in domain",
			id:           &userpb.UserId{Type: userpb.UserType_USER_TYPE_FEDERATED, Idp: "https://ocis.cloud.io", OpaqueId: "id"},
			domain:       "https://ocis.cloud.io",
			wantOpaqueId: "id@ocis.cloud.io",
			wantIdp:      "https://ocis.cloud.io",
		},
		{
			name:         "opaque contains idp, protocol in domain",
			id:           &userpb.UserId{Type: userpb.UserType_USER_TYPE_FEDERATED, Idp: "ocis.cloud.io", OpaqueId: "id"},
			domain:       "",
			wantOpaqueId: "id@ocis.cloud.io",
			wantIdp:      "https://ocis.cloud.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FederatedID(tt.id, tt.domain)
			if got == nil {
				t.Fatalf("got=nil")
			}
			if got.Type != userpb.UserType_USER_TYPE_FEDERATED {
				t.Fatalf("type=%v want=%v", got.Type, userpb.UserType_USER_TYPE_FEDERATED)
			}
			if got.OpaqueId != tt.wantOpaqueId {
				t.Fatalf("OpaqueId got=%q want=%q", got.OpaqueId, tt.wantOpaqueId)
			}
			if got.Idp != tt.wantIdp {
				t.Fatalf("Idp got=%q want=%q", got.Idp, tt.wantIdp)
			}
		})
	}
}
