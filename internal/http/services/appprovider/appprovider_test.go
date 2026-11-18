package appprovider

import (
	"context"
	"testing"

	providerv1beta1 "github.com/cs3org/go-cs3apis/cs3/auth/provider/v1beta1"
	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
)

func TestGetViewModeFromPublicScope(t *testing.T) {
	tests := []struct {
		name     string
		role     providerv1beta1.Role
		download bool
		want     gateway.OpenInAppRequest_ViewMode
	}{
		{"viewer with download", providerv1beta1.Role_ROLE_VIEWER, true, gateway.OpenInAppRequest_VIEW_MODE_READ_ONLY},
		{"viewer without download", providerv1beta1.Role_ROLE_VIEWER, false, gateway.OpenInAppRequest_VIEW_MODE_VIEW_ONLY},
		{"editor", providerv1beta1.Role_ROLE_EDITOR, true, gateway.OpenInAppRequest_VIEW_MODE_READ_WRITE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctxpkg.ContextSetScopes(context.Background(), map[string]*providerv1beta1.Scope{
				"publicshare:abc": {Role: tt.role},
			})
			res := &provider.ResourceInfo{PermissionSet: &provider.ResourcePermissions{InitiateFileDownload: tt.download}}

			got, err := getViewModeFromPublicScope(ctx, res)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetViewModeFromPublicScopeNoPublicShare(t *testing.T) {
	got, err := getViewModeFromPublicScope(context.Background(), &provider.ResourceInfo{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != gateway.OpenInAppRequest_VIEW_MODE_INVALID {
		t.Errorf("got %v, want VIEW_MODE_INVALID", got)
	}
}
