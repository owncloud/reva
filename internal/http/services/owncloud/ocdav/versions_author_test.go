package ocdav

import (
	"context"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocdav/propfind"
	"github.com/owncloud/reva/v2/pkg/rgrpc/status"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/owncloud/reva/v2/tests/cs3mocks/mocks"
)

func TestVersionAuthorsResolveOncePerUserAndTolerateMisses(t *testing.T) {
	ctx := context.Background()
	gw := mocks.NewGatewayAPIClient(t)
	gw.On("GetUser", mock.Anything, mock.MatchedBy(func(r *userpb.GetUserRequest) bool { return r.UserId.OpaqueId == "carol" })).
		Return(&userpb.GetUserResponse{Status: status.NewOK(ctx), User: &userpb.User{Username: "carol", DisplayName: "Carol King"}}, nil).Once()
	gw.On("GetUser", mock.Anything, mock.MatchedBy(func(r *userpb.GetUserRequest) bool { return r.UserId.OpaqueId == "gone" })).
		Return(&userpb.GetUserResponse{Status: &rpc.Status{Code: rpc.Code_CODE_NOT_FOUND}}, nil).Once()

	a := &versionAuthors{client: gw, users: map[string]*userpb.User{}}
	mk := func(id string) *typespb.Opaque {
		return utils.AppendJSONToOpaque(nil, storage.RevisionAuthorOpaqueKey, &userpb.UserId{OpaqueId: id, Idp: "idp"})
	}

	for i := 0; i < 2; i++ { // second round must be served from the per-request cache
		o := a.opaqueFor(ctx, mk("carol"))
		require.Equal(t, "carol", utils.ReadPlainFromOpaque(o, propfind.OpaqueVersionAuthorUsername))
		require.Equal(t, "Carol King", utils.ReadPlainFromOpaque(o, propfind.OpaqueVersionAuthorDisplayName))
		require.Nil(t, a.opaqueFor(ctx, mk("gone")), "an unresolvable author must not fail the listing")
	}
	require.Nil(t, a.opaqueFor(ctx, nil), "a version without an author has no opaque")
}
