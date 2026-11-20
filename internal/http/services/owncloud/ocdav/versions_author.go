package ocdav

import (
	"context"

	gateway "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"

	"github.com/owncloud/reva/v2/internal/http/services/owncloud/ocdav/propfind"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/utils"
)

// versionAuthors resolves the author of file versions to a name for the
// duration of one request, asking the user provider once per distinct user.
type versionAuthors struct {
	client gateway.GatewayAPIClient
	users  map[string]*userpb.User
}

// opaqueFor returns the opaque to put on a version's ResourceInfo, or nil when
// the version has no recorded author. A user that can no longer be resolved
// (e.g. deleted) leaves the properties unset rather than failing the listing.
func (a *versionAuthors) opaqueFor(ctx context.Context, v *typespb.Opaque) *typespb.Opaque {
	id := &userpb.UserId{}
	if !utils.ExistsInOpaque(v, storage.RevisionAuthorOpaqueKey) ||
		utils.ReadJSONFromOpaque(v, storage.RevisionAuthorOpaqueKey, id) != nil {
		return nil
	}
	key := id.GetIdp() + "!" + id.GetOpaqueId()
	u, ok := a.users[key]
	if !ok {
		res, err := a.client.GetUser(ctx, &userpb.GetUserRequest{UserId: id})
		switch {
		case err != nil:
			appctx.GetLogger(ctx).Error().Err(err).Msg("could not resolve version author")
		case res.GetStatus().GetCode() == rpc.Code_CODE_OK:
			u = res.GetUser()
		}
		a.users[key] = u // also caches a miss
	}
	if u == nil {
		return nil
	}
	o := utils.AppendPlainToOpaque(nil, propfind.OpaqueVersionAuthorUsername, u.GetUsername())
	return utils.AppendPlainToOpaque(o, propfind.OpaqueVersionAuthorDisplayName, u.GetDisplayName())
}
