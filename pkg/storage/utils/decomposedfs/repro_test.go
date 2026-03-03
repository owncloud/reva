package decomposedfs_test

import (
	"context"

	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	helpers "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/testhelpers"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

var _ = Describe("Repro CreateHome", func() {
	var (
		env *helpers.TestEnv
	)
	BeforeEach(func() {
		var err error
		env, err = helpers.NewTestEnv(nil)
		Expect(err).ToNot(HaveOccurred())
		env.PermissionsClient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(
			func(ctx context.Context, in *cs3permissions.CheckPermissionRequest, opts ...grpc.CallOption) *cs3permissions.CheckPermissionResponse {
				return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
			},
			nil)
		env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(func(ctx context.Context, n *node.Node) *provider.ResourcePermissions {
			return node.OwnerPermissions()
		}, nil)
	})

	AfterEach(func() {
		if env != nil {
			env.Cleanup()
		}
	})

	It("returns ALREADY_EXISTS when creating personal space twice with space_id in opaque", func() {
		req := &provider.CreateStorageSpaceRequest{
			Type:  "personal",
			Owner: env.Users[0],
			Name:  "username",
			Opaque: &typesv1beta1.Opaque{
				Map: map[string]*typesv1beta1.OpaqueEntry{
					"space_id": {
						Decoder: "plain",
						Value:   []byte(env.Users[0].Id.OpaqueId),
					},
				},
			},
		}

		// First creation
		resp1, err := env.Fs.CreateStorageSpace(env.Ctx, req)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp1.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

		// Second creation
		_, err = env.Fs.CreateStorageSpace(env.Ctx, req)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("space already exists"))
	})
})
