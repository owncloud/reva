package decomposedfs_test

import (
	"context"
	"fmt"
	"testing"

	grouppb "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	userv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/google/uuid"
	"github.com/owncloud/reva/v2/pkg/conversions"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/aspects"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/permissions"
	helpers "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/testhelpers"
	"google.golang.org/grpc"
)

// benchPermissionsClient grants every space permission except "Drives.List", so that
// ListStorageSpaces has to evaluate the node permissions like for a regular user.
type benchPermissionsClient struct{}

func (benchPermissionsClient) CheckPermission(_ context.Context, in *cs3permissions.CheckPermissionRequest, _ ...grpc.CallOption) (*cs3permissions.CheckPermissionResponse, error) {
	code := rpcv1beta1.Code_CODE_OK
	if in.GetPermission() == "Drives.List" {
		code = rpcv1beta1.Code_CODE_PERMISSION_DENIED
	}
	return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: code}}, nil
}

// benchPermissionsSelector hands out benchPermissionsClient. It is passed to the
// permissions directly instead of being registered in the global selector pool,
// so the benchmark leaves the selector the other tests use untouched.
type benchPermissionsSelector struct{}

func (benchPermissionsSelector) Next(...pool.Option) (cs3permissions.PermissionsAPIClient, error) {
	return benchPermissionsClient{}, nil
}

// withRealPermissions replaces the mocked node permissions with the real implementation.
func withRealPermissions(a *aspects.Aspects) {
	a.Permissions = permissions.NewPermissions(node.NewPermissions(a.Lookup), benchPermissionsSelector{})
}

// setupListSpacesBench creates a storage with one personal space for the owner and
// numProjects project spaces the owner is a manager of (as their creator). Every project space gets
// membersPerSpace additional user grants and one group grant to mimic real spaces.
// When withContent is set a folder is created in every project space, which propagates
// a tree mtime to the space root like for spaces that are in use.
func setupListSpacesBench(b *testing.B, backend string, numProjects, membersPerSpace int, withContent bool) *helpers.TestEnv {
	b.Helper()
	env, err := helpers.NewTestEnv(map[string]interface{}{
		"metadata_backend": backend,
		// the metadata cache keys are relative to the storage root and the in-memory
		// store is shared within the process, so isolate every env in its own database
		"filemetadatacache": map[string]interface{}{"cache_database": uuid.NewString()},
	}, withRealPermissions)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(env.Cleanup)

	viewerPerms := conversions.NewViewerRole().CS3ResourcePermissions()
	for i := 0; i < numProjects; i++ {
		res, err := env.Fs.CreateStorageSpace(env.Ctx, &provider.CreateStorageSpaceRequest{
			Type: "project",
			Name: fmt.Sprintf("project %d", i),
		})
		if err != nil {
			b.Fatal(err)
		}
		ref := &provider.Reference{ResourceId: res.GetStorageSpace().GetRoot()}
		if withContent {
			if _, err := env.Fs.CreateDir(env.Ctx, &provider.Reference{ResourceId: ref.GetResourceId(), Path: "./folder"}); err != nil {
				b.Fatal(err)
			}
		}
		for m := 0; m < membersPerSpace; m++ {
			if err := env.Fs.AddGrant(env.Ctx, ref, &provider.Grant{
				Grantee: &provider.Grantee{
					Type: provider.GranteeType_GRANTEE_TYPE_USER,
					Id:   &provider.Grantee_UserId{UserId: &userv1beta1.UserId{Idp: "idp", OpaqueId: uuid.NewString(), Type: userv1beta1.UserType_USER_TYPE_PRIMARY}},
				},
				Permissions: viewerPerms,
			}); err != nil {
				b.Fatal(err)
			}
		}
		if err := env.Fs.AddGrant(env.Ctx, ref, &provider.Grant{
			Grantee: &provider.Grantee{
				Type: provider.GranteeType_GRANTEE_TYPE_GROUP,
				Id:   &provider.Grantee_GroupId{GroupId: &grouppb.GroupId{OpaqueId: uuid.NewString()}},
			},
			Permissions: viewerPerms,
		}); err != nil {
			b.Fatal(err)
		}
	}
	return env
}

// BenchmarkListStorageSpaces measures listing the spaces of a user the way the
// spaces registry does it for GET /graph/v1.0/me/drives (filtered by user, restricted).
func BenchmarkListStorageSpaces(b *testing.B) {
	for _, backend := range []string{"xattrs", "messagepack"} {
		for _, numProjects := range []int{10, 100} {
			for _, withContent := range []bool{false, true} {
				b.Run(fmt.Sprintf("%s/projects=%d/content=%t", backend, numProjects, withContent), func(b *testing.B) {
					// set up inside b.Run so that only the cases selected by -bench are set up.
					// b.Loop runs this function once and excludes everything before it from the timing.
					env := setupListSpacesBench(b, backend, numProjects, 10, withContent)
					filters := []*provider.ListStorageSpacesRequest_Filter{{
						Type: provider.ListStorageSpacesRequest_Filter_TYPE_USER,
						Term: &provider.ListStorageSpacesRequest_Filter_User{User: env.Owner.GetId()},
					}}
					ctx := ctxpkg.ContextSetUser(context.Background(), env.Owner)

					spaces, err := env.Fs.ListStorageSpaces(ctx, filters, false)
					if err != nil {
						b.Fatal(err)
					}
					if len(spaces) != numProjects+1 {
						b.Fatalf("expected %d spaces, got %d", numProjects+1, len(spaces))
					}

					b.ReportAllocs()
					for b.Loop() {
						if _, err := env.Fs.ListStorageSpaces(ctx, filters, false); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
