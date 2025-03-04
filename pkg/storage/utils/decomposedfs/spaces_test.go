// Copyright 2018-2022 CERN
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

package decomposedfs_test

import (
	"context"

	userv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	ctxpkg "github.com/cs3org/owncloud/v2/pkg/ctx"
	"github.com/cs3org/owncloud/v2/pkg/storage/utils/decomposedfs/node"
	helpers "github.com/cs3org/owncloud/v2/pkg/storage/utils/decomposedfs/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

var _ = Describe("Spaces", func() {

	Describe("Create Space", func() {
		var (
			env *helpers.TestEnv
		)
		BeforeEach(func() {
			var err error
			env, err = helpers.NewTestEnv(nil)
			Expect(err).ToNot(HaveOccurred())
			env.PermissionsClient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(
				func(ctx context.Context, in *cs3permissions.CheckPermissionRequest, opts ...grpc.CallOption) *cs3permissions.CheckPermissionResponse {
					if in.Permission == "Drives.DeletePersonal" && ctxpkg.ContextMustGetUser(ctx).Id.GetOpaqueId() == env.DeleteHomeSpacesUser.Id.OpaqueId {
						return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
					}
					if in.Permission == "Drives.DeleteProject" && ctxpkg.ContextMustGetUser(ctx).Id.GetOpaqueId() == env.DeleteAllSpacesUser.Id.OpaqueId {
						return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
					}
					if (in.Permission == "Drives.Create" || in.Permission == "Drives.List") && ctxpkg.ContextMustGetUser(ctx).Id.GetOpaqueId() == helpers.OwnerID {
						return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
					}
					// any other user
					return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_PERMISSION_DENIED}}
				},
				nil)
			env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(func(ctx context.Context, n *node.Node) *provider.ResourcePermissions {
				if ctxpkg.ContextMustGetUser(ctx).Id.GetOpaqueId() == "25b69780-5f39-43be-a7ac-a9b9e9fe4230" {
					return node.OwnerPermissions() // id of owner/admin
				}
				return node.NoPermissions()
			}, nil)
		})

		AfterEach(func() {
			if env != nil {
				env.Cleanup()
			}
		})

		Context("during login", func() {
			It("space is created", func() {
				resp, err := env.Fs.ListStorageSpaces(env.Ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(resp)).To(Equal(1))
				Expect(string(resp[0].Opaque.GetMap()["spaceAlias"].Value)).To(Equal("personal/username"))
				Expect(resp[0].Name).To(Equal("username"))
				Expect(resp[0].SpaceType).To(Equal("personal"))
			})
		})
		Context("when creating a space", func() {
			It("project space is created", func() {
				env.Owner = nil
				resp, err := env.Fs.CreateStorageSpace(env.Ctx, &provider.CreateStorageSpaceRequest{Name: "Mission to Mars", Type: "project"})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(resp.StorageSpace).ToNot(Equal(nil))
				Expect(string(resp.StorageSpace.Opaque.Map["spaceAlias"].Value)).To(Equal("project/mission-to-mars"))
				Expect(resp.StorageSpace.Name).To(Equal("Mission to Mars"))
				Expect(resp.StorageSpace.SpaceType).To(Equal("project"))
			})
		})

		Context("needs to check node permissions", func() {
			It("returns false on requesting for other user with canlistallspaces und no unrestricted privilege", func() {
				resp := env.Fs.MustCheckNodePermissions(env.Ctx, false)
				Expect(resp).To(Equal(true))
			})
			It("returns true on requesting unrestricted as non-admin", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.Users[0])
				resp := env.Fs.MustCheckNodePermissions(ctx, true)
				Expect(resp).To(Equal(true))
			})
			It("returns true on requesting for own spaces", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.Users[0])
				resp := env.Fs.MustCheckNodePermissions(ctx, false)
				Expect(resp).To(Equal(true))
			})
			It("returns false on unrestricted", func() {
				resp := env.Fs.MustCheckNodePermissions(env.Ctx, true)
				Expect(resp).To(Equal(false))
			})
		})

		Context("can delete homespace", func() {
			It("fails on trying to delete a homespace as non-admin", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.Users[1])
				resp, err := env.Fs.ListStorageSpaces(env.Ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(resp)).To(Equal(1))
				Expect(string(resp[0].Opaque.GetMap()["spaceAlias"].Value)).To(Equal("personal/username"))
				Expect(resp[0].Name).To(Equal("username"))
				Expect(resp[0].SpaceType).To(Equal("personal"))
				err = env.Fs.DeleteStorageSpace(ctx, &provider.DeleteStorageSpaceRequest{
					Id: resp[0].GetId(),
				})
				Expect(err).To(HaveOccurred())
			})
			It("succeeds on trying to delete homespace as user with 'delete-all-home-spaces' permission", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.DeleteHomeSpacesUser)
				resp, err := env.Fs.ListStorageSpaces(env.Ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(resp)).To(Equal(1))
				Expect(string(resp[0].Opaque.GetMap()["spaceAlias"].Value)).To(Equal("personal/username"))
				Expect(resp[0].Name).To(Equal("username"))
				Expect(resp[0].SpaceType).To(Equal("personal"))
				err = env.Fs.DeleteStorageSpace(ctx, &provider.DeleteStorageSpaceRequest{
					Id: resp[0].GetId(),
				})
				Expect(err).To(Not(HaveOccurred()))
			})
			It("fails on trying to delete homespace as user with 'delete-all-spaces' permission", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.DeleteAllSpacesUser)
				resp, err := env.Fs.ListStorageSpaces(env.Ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(resp)).To(Equal(1))
				Expect(string(resp[0].Opaque.GetMap()["spaceAlias"].Value)).To(Equal("personal/username"))
				Expect(resp[0].Name).To(Equal("username"))
				Expect(resp[0].SpaceType).To(Equal("personal"))
				err = env.Fs.DeleteStorageSpace(ctx, &provider.DeleteStorageSpaceRequest{
					Id: resp[0].GetId(),
				})
				Expect(err).To(HaveOccurred())
			})
		})

		Context("can delete (purge) project spaces", func() {
			var delReq *provider.DeleteStorageSpaceRequest
			BeforeEach(func() {
				resp, err := env.Fs.CreateStorageSpace(env.Ctx, &provider.CreateStorageSpaceRequest{Name: "Mission to Venus", Type: "project"})
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(resp.StorageSpace).ToNot(Equal(nil))
				spaceID := resp.StorageSpace.GetId()
				err = env.Fs.DeleteStorageSpace(env.Ctx, &provider.DeleteStorageSpaceRequest{
					Id: spaceID,
				})
				Expect(err).To(Not(HaveOccurred()))
				delReq = &provider.DeleteStorageSpaceRequest{
					Opaque: &typesv1beta1.Opaque{
						Map: map[string]*typesv1beta1.OpaqueEntry{
							"purge": {
								Decoder: "plain",
								Value:   []byte("true"),
							},
						},
					},
					Id: spaceID,
				}
			})
			It("fails as a unprivileged user", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.Users[1])
				err := env.Fs.DeleteStorageSpace(ctx, delReq)
				Expect(err).To(HaveOccurred())
			})
			It("fails as a user with 'delete-all-home-spaces privilege", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.DeleteHomeSpacesUser)
				err := env.Fs.DeleteStorageSpace(ctx, delReq)
				Expect(err).To(HaveOccurred())
			})
			It("succeeds as a user with 'delete-all-spaces privilege", func() {
				ctx := ctxpkg.ContextSetUser(context.Background(), env.DeleteAllSpacesUser)
				err := env.Fs.DeleteStorageSpace(ctx, delReq)
				Expect(err).To(Not(HaveOccurred()))
			})
			It("succeeds as the space owner", func() {
				err := env.Fs.DeleteStorageSpace(env.Ctx, delReq)
				Expect(err).To(Not(HaveOccurred()))
			})
		})

		Describe("Create Spaces with custom alias template", func() {
			var (
				env *helpers.TestEnv
			)

			BeforeEach(func() {
				var err error
				env, err = helpers.NewTestEnv(map[string]interface{}{
					"personalspacealias_template": "{{.SpaceType}}/{{.Email.Local}}@{{.Email.Domain}}",
					"generalspacealias_template":  "{{.SpaceType}}:{{.SpaceName | replace \" \" \"-\" | upper}}",
				})
				Expect(err).ToNot(HaveOccurred())
				env.PermissionsClient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(&cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}, nil)
				env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(&provider.ResourcePermissions{
					Stat:     true,
					AddGrant: true,
					GetQuota: true,
				}, nil)
			})

			AfterEach(func() {
				if env != nil {
					env.Cleanup()
				}
			})
			Context("during login", func() {
				It("personal space is created with custom alias", func() {
					resp, err := env.Fs.ListStorageSpaces(env.Ctx, nil, false)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(resp)).To(Equal(1))
					Expect(string(resp[0].Opaque.GetMap()["spaceAlias"].Value)).To(Equal("personal/username@_unknown"))
				})
			})
			Context("creating a space", func() {
				It("project space is created with custom alias", func() {
					env.Owner = nil
					resp, err := env.Fs.CreateStorageSpace(env.Ctx, &provider.CreateStorageSpaceRequest{Name: "Mission to Venus", Type: "project"})
					Expect(err).ToNot(HaveOccurred())
					Expect(resp.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
					Expect(resp.StorageSpace).ToNot(Equal(nil))
					Expect(string(resp.StorageSpace.Opaque.Map["spaceAlias"].Value)).To(Equal("project:MISSION-TO-VENUS"))

				})
			})
		})
	})

	Describe("Update Space", func() {
		var (
			env     *helpers.TestEnv
			spaceid *provider.StorageSpaceId

			manager  = &userv1beta1.User{Id: &userv1beta1.UserId{OpaqueId: "manager"}}
			editor   = &userv1beta1.User{Id: &userv1beta1.UserId{OpaqueId: "editor"}}
			viewer   = &userv1beta1.User{Id: &userv1beta1.UserId{OpaqueId: "viewer"}}
			nomember = &userv1beta1.User{Id: &userv1beta1.UserId{OpaqueId: "nomember"}}
			admin    = &userv1beta1.User{Id: &userv1beta1.UserId{OpaqueId: "admin"}}
		)
		BeforeEach(func() {
			var err error
			env, err = helpers.NewTestEnv(nil)
			Expect(err).ToNot(HaveOccurred())

			// space permissions
			env.PermissionsClient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(
				func(ctx context.Context, in *cs3permissions.CheckPermissionRequest, opts ...grpc.CallOption) *cs3permissions.CheckPermissionResponse {
					switch ctxpkg.ContextMustGetUser(ctx).GetId().GetOpaqueId() {
					case manager.GetId().GetOpaqueId():
						switch in.Permission {
						case "Drives.Create":
							return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
						default:
							return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_PERMISSION_DENIED}}
						}
					case admin.GetId().GetOpaqueId():
						return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_OK}}
					default:
						return &cs3permissions.CheckPermissionResponse{Status: &rpcv1beta1.Status{Code: rpcv1beta1.Code_CODE_PERMISSION_DENIED}}
					}
				}, nil)

			env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).Return(
				func(ctx context.Context, n *node.Node) *provider.ResourcePermissions {
					switch ctxpkg.ContextMustGetUser(ctx).GetId().GetOpaqueId() {
					case manager.GetId().GetOpaqueId():
						return node.OwnerPermissions() // id of owner/admin
					case editor.GetId().GetOpaqueId():
						return &provider.ResourcePermissions{InitiateFileUpload: true} // mock editor
					case viewer.GetId().GetOpaqueId():
						return &provider.ResourcePermissions{Stat: true} // mock viewer
					default:
						return node.NoPermissions()
					}
				}, nil)

			resp, err := env.Fs.CreateStorageSpace(ctxpkg.ContextSetUser(context.Background(), manager), &provider.CreateStorageSpaceRequest{Name: "Mission to Venus", Type: "project"})
			Expect(err).ToNot(HaveOccurred())

			spaceid = resp.GetStorageSpace().GetId()
		})

		AfterEach(func() {
			if env != nil {
				env.Cleanup()
			}
		})

		DescribeTable("update project spaces",
			func(details func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest), expectedStatusCode rpcv1beta1.Code) {
				u, req := details()
				r, err := env.Fs.UpdateStorageSpace(ctxpkg.ContextSetUser(context.Background(), u), req)
				Expect(err).ToNot(HaveOccurred())
				Expect(r.Status.Code).To(Equal(expectedStatusCode))
			},

			Entry("Manager can change everything but quota",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return manager, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Name: "new space name",
							Id:   spaceid,
							Opaque: &typesv1beta1.Opaque{
								Map: map[string]*typesv1beta1.OpaqueEntry{
									"description": {
										Decoder: "plain",
										Value:   []byte("new description"),
									},
									"spacealias": {
										Decoder: "plain",
										Value:   []byte("new alias"),
									},
									"image": {
										Decoder: "plain",
										Value:   []byte("a$b!c"),
									},
									"readme": {
										Decoder: "plain",
										Value:   []byte("f$g!h"),
									},
								},
							},
						},
					}
				},
				rpcv1beta1.Code_CODE_OK,
			),
			Entry("Manager cannot change quota, even with large request",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return manager, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Name:  "new space name",
							Id:    spaceid,
							Quota: &provider.Quota{QuotaMaxBytes: uint64(1000)},
							Opaque: &typesv1beta1.Opaque{
								Map: map[string]*typesv1beta1.OpaqueEntry{
									"description": {
										Decoder: "plain",
										Value:   []byte("new description"),
									},
									"spacealias": {
										Decoder: "plain",
										Value:   []byte("new alias"),
									},
									"image": {
										Decoder: "plain",
										Value:   []byte("a$b!c"),
									},
									"readme": {
										Decoder: "plain",
										Value:   []byte("f$g!h"),
									},
								},
							},
						},
					}
				},
				rpcv1beta1.Code_CODE_PERMISSION_DENIED,
			),
			Entry("Editor cannot change quota",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return editor, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Id:    spaceid,
							Quota: &provider.Quota{QuotaMaxBytes: uint64(1000)},
						},
					}
				},
				rpcv1beta1.Code_CODE_PERMISSION_DENIED,
			),
			Entry("Editor cannot change name",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return editor, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Name: "new spacename",
							Id:   spaceid,
						},
					}
				},
				rpcv1beta1.Code_CODE_PERMISSION_DENIED,
			),
			Entry("Admin can change quota",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return admin, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Id:    spaceid,
							Quota: &provider.Quota{QuotaMaxBytes: uint64(1000)},
						},
					}
				},
				rpcv1beta1.Code_CODE_OK,
			),
			Entry("Admin can change name and description",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return admin, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Name: "new spacename",
							Id:   spaceid,
							Opaque: &typesv1beta1.Opaque{
								Map: map[string]*typesv1beta1.OpaqueEntry{
									"description": {
										Decoder: "plain",
										Value:   []byte("new description"),
									},
								},
							},
						},
					}
				},
				rpcv1beta1.Code_CODE_OK,
			),
			Entry("Viewer gets OK when he changes nothing",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return viewer, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Id: spaceid,
						},
					}
				},
				rpcv1beta1.Code_CODE_OK,
			),
			Entry("NoMember will not find the space",
				func() (*userv1beta1.User, *provider.UpdateStorageSpaceRequest) {
					return nomember, &provider.UpdateStorageSpaceRequest{
						StorageSpace: &provider.StorageSpace{
							Id: spaceid,
						},
					}
				},
				rpcv1beta1.Code_CODE_NOT_FOUND,
			),
		)
	})
})
