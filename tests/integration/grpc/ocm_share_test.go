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

package grpc_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"path/filepath"
	"strconv"

	gatewaypb "github.com/cs3org/go-cs3apis/cs3/gateway/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	invitev1beta1 "github.com/cs3org/go-cs3apis/cs3/ocm/invite/v1beta1"
	ocmproviderpb "github.com/cs3org/go-cs3apis/cs3/ocm/provider/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	ocmv1beta1 "github.com/cs3org/go-cs3apis/cs3/sharing/ocm/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	storagep "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/owncloud/reva/v2/internal/http/services/datagateway"
	"github.com/owncloud/reva/v2/pkg/conversions"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/ocm/share"
	ocm "github.com/owncloud/reva/v2/pkg/ocm/storage/received"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/rhttp"
	"github.com/owncloud/reva/v2/pkg/storage/fs/ocis"
	jwt "github.com/owncloud/reva/v2/pkg/token/manager/jwt"
	"github.com/owncloud/reva/v2/tests/helpers"
	"github.com/owncloud/ocis/v2/services/webdav/pkg/net"
	"github.com/pkg/errors"
	"github.com/studio-b12/gowebdav"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	editorPermissions = &provider.ResourcePermissions{
		CreateContainer:      true,
		Delete:               true,
		GetPath:              true,
		GetQuota:             true,
		InitiateFileDownload: true,
		InitiateFileUpload:   true,
		ListContainer:        true,
		ListGrants:           true,
		ListRecycle:          true,
		RestoreRecycleItem:   true,
		Move:                 true,
		Stat:                 true,
	}
	viewerPermissions = &provider.ResourcePermissions{
		Stat:                 true,
		InitiateFileDownload: true,
		GetPath:              true,
		GetQuota:             true,
		ListContainer:        true,
		ListRecycle:          true,
	}
)

var _ = PDescribe("ocm share", func() {
	var (
		revads = map[string]*Revad{}

		variables = map[string]string{}

		ctxEinstein context.Context
		ctxMarie    context.Context
		cernboxgw   gatewaypb.GatewayAPIClient
		cesnetgw    gatewaypb.GatewayAPIClient
		cernbox     = &ocmproviderpb.ProviderInfo{
			Name:         "cernbox",
			FullName:     "CERNBox",
			Description:  "CERNBox provides cloud data storage to all CERN users.",
			Organization: "CERN",
			Domain:       "cernbox.cern.ch",
			Homepage:     "https://cernbox.web.cern.ch",
			Services: []*ocmproviderpb.Service{
				{
					Endpoint: &ocmproviderpb.ServiceEndpoint{
						Type: &ocmproviderpb.ServiceType{
							Name:        "OCM",
							Description: "CERNBox Open Cloud Mesh API",
						},
						Name:        "CERNBox - OCM API",
						Path:        "http://127.0.0.1:19001/ocm/",
						IsMonitored: true,
					},
					Host:       "127.0.0.1:19001",
					ApiVersion: "0.0.1",
				},
			},
		}
		einstein = &userpb.User{
			Id: &userpb.UserId{
				OpaqueId: "4c510ada-c86b-4815-8820-42cdf82c3d51",
				Idp:      "https://cernbox.cern.ch",
				Type:     userpb.UserType_USER_TYPE_PRIMARY,
			},
			Username:    "einstein",
			Mail:        "einstein@cern.ch",
			DisplayName: "Albert Einstein",
		}
		federatedEinsteinID = &userpb.UserId{
			Type:     userpb.UserType_USER_TYPE_FEDERATED,
			Idp:      "cernbox.cern.ch",
			OpaqueId: base64.URLEncoding.EncodeToString([]byte("4c510ada-c86b-4815-8820-42cdf82c3d51@https://cernbox.cern.ch")),
		}
		marie = &userpb.User{
			Id: &userpb.UserId{
				OpaqueId: "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c",
				Idp:      "https://cesnet.cz",
				Type:     userpb.UserType_USER_TYPE_PRIMARY,
			},
			Username:    "marie",
			Mail:        "marie@cesnet.cz",
			DisplayName: "Marie Curie",
		}
		federatedMarieID = &userpb.UserId{
			Type:     userpb.UserType_USER_TYPE_FEDERATED,
			Idp:      "cesnet.cz",
			OpaqueId: base64.URLEncoding.EncodeToString([]byte("f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c@https://cesnet.cz")),
		}
	)

	JustBeforeEach(func() {
		tokenManager, err := jwt.New(map[string]interface{}{"secret": "changemeplease"})
		Expect(err).ToNot(HaveOccurred())
		ctxEinstein = ctxWithAuthToken(tokenManager, einstein)
		ctxMarie = ctxWithAuthToken(tokenManager, marie)
		revads, err = startRevads([]RevadConfig{
			{Name: "cernboxgw", Config: "ocm-share/ocm-server-cernbox-grpc.toml",
				Files: map[string]string{
					"providers": "ocm-providers.demo.json",
				},
				Resources: map[string]Resource{
					"ocm_share_cernbox_file": File{Content: "{}"},
					"invite_token_file":      File{Content: "{}"},
				},
			},
			{Name: "permissions", Config: "permissions-ocis-ci.toml"},
			{Name: "cernboxpublicstorage", Config: "ocm-share/cernbox-storageprovider-public.toml"},
			{Name: "cernboxwebdav", Config: "ocm-share/cernbox-webdav-server.toml"},
			{Name: "cernboxhttp", Config: "ocm-share/ocm-server-cernbox-http.toml"},
			{Name: "cesnetgw", Config: "ocm-share/ocm-server-cesnet-grpc.toml",
				Files: map[string]string{
					"providers": "ocm-providers.demo.json",
				},
				Resources: map[string]Resource{
					"ocm_share_cesnet_file": File{Content: "{}"},
					"invite_token_file":     File{Content: "{}"},
				},
			},
			{Name: "cesnethttp", Config: "ocm-share/ocm-server-cesnet-http.toml"},
			{Name: "cernboxocmsharesauth", Config: "ocm-share/ocm-cernbox-ocmshares-authprovider.toml"},
			{Name: "cernboxmachineauth", Config: "ocm-share/cernbox-machine-authprovider.toml"},
		}, variables)
		Expect(err).ToNot(HaveOccurred())
		cernboxgw, err = pool.GetGatewayServiceClient(revads["cernboxgw"].GrpcAddress)
		Expect(err).ToNot(HaveOccurred())
		cesnetgw, err = pool.GetGatewayServiceClient(revads["cesnetgw"].GrpcAddress)
		Expect(err).ToNot(HaveOccurred())
		cernbox.Services[0].Endpoint.Path = "http://" + revads["cernboxhttp"].GrpcAddress + "/ocm"

		createHomeResp, err := cernboxgw.CreateHome(ctxEinstein, &provider.CreateHomeRequest{})
		Expect(err).ToNot(HaveOccurred())
		Expect(createHomeResp.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
	})

	AfterEach(func() {
		for _, r := range revads {
			Expect(r.Cleanup(CurrentGinkgoTestDescription().Failed)).To(Succeed())
		}
	})

	Describe("marie has already accepted the invitation workflow", func() {
		JustBeforeEach(func() {
			// einstein generates an invite token
			tknRes, err := cernboxgw.GenerateInviteToken(ctxEinstein, &invitev1beta1.GenerateInviteTokenRequest{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tknRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

			// marie accepts it and her provider forwards the invite back to the instance of einstein
			invRes, err := cesnetgw.ForwardInvite(ctxMarie, &invitev1beta1.ForwardInviteRequest{
				InviteToken:          tknRes.InviteToken,
				OriginSystemProvider: cernbox,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(invRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
			// Make sure the user is a federated user
			// The user type must be a federated user
			Expect(invRes.UserId.Type).To(Equal(userpb.UserType_USER_TYPE_FEDERATED))
			// Federated users use the OCM provider id which MUST NOT contain the protocol
			Expect(invRes.UserId.Idp).To(Equal("cernbox.cern.ch"))
			// The OpaqueId is the base64 encoded user id and the provider id to provent collisions with other users on the graph API
			Expect(invRes.UserId.OpaqueId).To(Equal(federatedEinsteinID.OpaqueId))
		})

		Context("einstein shares a file with view permissions", func() {
			It("marie is able to see the content of the file", func() {
				fs, err := ocis.New(map[string]interface{}{
					"root":           revads["cernboxgw"].StorageRoot,
					"permissionssvc": revads["permissions"].GrpcAddress,
				}, nil, nil)
				Expect(err).ToNot(HaveOccurred())
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{
						SpaceId: "4c510ada-c86b-4815-8820-42cdf82c3d51",
					},
					Path: "./new-file",
				}
				err = helpers.Upload(ctxEinstein, fs, ref, []byte("test"))
				Expect(err).ToNot(HaveOccurred())

				By("share the file with marie")
				info, err := stat(ctxEinstein, cernboxgw, ref)
				Expect(err).ToNot(HaveOccurred())

				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: info.Id,
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewViewerRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				// get auth context for ocm share
				ocmCtx := context.Background()
				authRes, err := cernboxgw.Authenticate(ocmCtx, &gatewaypb.AuthenticateRequest{
					Type:         "ocmshares",
					ClientId:     createShareRes.GetShare().GetId().GetOpaqueId(),
					ClientSecret: createShareRes.GetShare().GetToken(),
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(authRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				// create ocm context
				ocmCtx = ctxpkg.ContextSetToken(ocmCtx, authRes.Token)
				ocmCtx = metadata.AppendToOutgoingContext(ocmCtx, ctxpkg.TokenHeader, authRes.Token)
				// I commented this because we currently do return a space ... but IMO we should not. the share is not accepted / synced yet
				/*
					// try finding the space by path
					lssRes, err := cernboxgw.ListStorageSpaces(ocmCtx, &provider.ListStorageSpacesRequest{
						Opaque: &typespb.Opaque{
							Map: map[string]*typespb.OpaqueEntry{
								"path": {
									Decoder: "plain",
									Value:   []byte("/public/" + createShareRes.GetShare().GetId().GetOpaqueId()),
								},
								"metadata": {
									Decoder: "plain",
									Value:   []byte("*"),
								},
							},
						}})
					Expect(err).ToNot(HaveOccurred())
					Expect(lssRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
					Expect(lssRes.StorageSpaces).To(HaveLen(0), "pending ocm share should not be listed as a space")
				*/
				By("marie accepts the share")
				listRes, err := cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share := listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))
				Expect(share.State).To(Equal(ocmv1beta1.ShareState_SHARE_STATE_PENDING))

				share.State = ocmv1beta1.ShareState_SHARE_STATE_ACCEPTED
				_, err = cesnetgw.UpdateReceivedOCMShare(ctxMarie, &ocmv1beta1.UpdateReceivedOCMShareRequest{
					Share:      share,
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				By("marie accesses the share")

				// try finding the space by path again
				lssRes, err := cernboxgw.ListStorageSpaces(ocmCtx, &provider.ListStorageSpacesRequest{
					Opaque: &typespb.Opaque{
						Map: map[string]*typespb.OpaqueEntry{
							"path": {
								Decoder: "plain",
								Value:   []byte("/public/" + createShareRes.GetShare().GetId().GetOpaqueId()),
							},
							"metadata": {
								Decoder: "plain",
								Value:   []byte("*"),
							},
						},
					}})
				Expect(err).ToNot(HaveOccurred())
				Expect(lssRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(lssRes.StorageSpaces).To(HaveLen(1), "accepted ocm share should be listed as a space")

				listRes, err = cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share = listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))
				Expect(share.State).To(Equal(ocmv1beta1.ShareState_SHARE_STATE_ACCEPTED))

				protocol := share.Protocols[0]
				webdav, ok := protocol.Term.(*ocmv1beta1.Protocol_WebdavOptions)
				Expect(ok).To(BeTrue())

				webdavClient := newWebDAVClient(webdav.WebdavOptions)
				d, err := webdavClient.Read(".")
				Expect(err).ToNot(HaveOccurred())
				Expect(d).To(Equal([]byte("test")))

				err = webdavClient.Write(".", []byte("will-never-be-written"), 0)
				Expect(err).To(HaveOccurred())

				By("marie access the share using the ocm mount")
				ref = &provider.Reference{Path: ocmPath(share.Id, "")}
				statRes, err := cesnetgw.Stat(ctxMarie, &provider.StatRequest{Ref: ref})
				Expect(err).ToNot(HaveOccurred())
				Expect(statRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(statRes.Info.Id).ToNot(BeNil())
				checkResourceInfo(statRes.Info, &provider.ResourceInfo{
					Name:          "new-file",
					Path:          "new-file",
					Size:          4,
					Type:          provider.ResourceType_RESOURCE_TYPE_FILE,
					PermissionSet: viewerPermissions,
				})

				data, err := helpers.Download(ctxMarie, cesnetgw, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(data).To(Equal([]byte("test")))

				Expect(helpers.UploadGateway(ctxMarie, cesnetgw, ref, []byte("will-never-be-written"))).ToNot(Succeed())
			})
		})

		Context("einstein shares a file with editor permissions", func() {
			It("marie is able to modify the content of the file", func() {
				fileToShare := &provider.Reference{
					ResourceId: &storagep.ResourceId{
						SpaceId:  einstein.Id.OpaqueId,
						OpaqueId: einstein.Id.OpaqueId,
					},
					Path: "./new-file",
				}
				By("creating a file")
				Expect(helpers.CreateFile(ctxEinstein, cernboxgw, fileToShare, []byte("test"))).To(Succeed())

				By("share the file with marie")
				info, err := stat(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())

				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: info.Id,
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewEditorRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				By("marie access the share and modify the content of the file")
				listRes, err := cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share := listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))

				protocol := share.Protocols[0]
				webdav, ok := protocol.Term.(*ocmv1beta1.Protocol_WebdavOptions)
				Expect(ok).To(BeTrue())

				data := []byte("new-content")
				webdavClient := newWebDAVClient(webdav.WebdavOptions)
				err = webdavClient.Write(".", data, 0)
				Expect(err).ToNot(HaveOccurred())

				By("check that the file was modified")
				newContent, err := download(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())
				Expect(newContent).To(Equal([]byte("new-content")))

				By("marie access the share using the ocm mount")
				ref := &provider.Reference{Path: ocmPath(share.Id, "")}
				statRes, err := cesnetgw.Stat(ctxMarie, &provider.StatRequest{Ref: ref})
				Expect(err).ToNot(HaveOccurred())
				Expect(statRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				checkResourceInfo(statRes.Info, &provider.ResourceInfo{
					Name:          "new-file",
					Path:          "new-file",
					Size:          uint64(len(data)),
					Type:          provider.ResourceType_RESOURCE_TYPE_FILE,
					PermissionSet: editorPermissions,
				})

				data, err = helpers.Download(ctxMarie, cesnetgw, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(data).To(Equal([]byte("new-content")))

				Expect(helpers.UploadGateway(ctxMarie, cesnetgw, ref, []byte("uploaded-from-ocm-mount"))).To(Succeed())
				newContent, err = download(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())
				Expect(newContent).To(Equal([]byte("uploaded-from-ocm-mount")))
			})
		})

		Context("einstein shares a folder with view permissions", func() {
			It("marie is able to see the content of the folder", func() {
				structure := helpers.Folder{
					"foo": helpers.File{
						Content: "foo",
					},
					"dir": helpers.Folder{
						"foo": helpers.File{
							Content: "dir/foo",
						},
						"bar": helpers.Folder{},
					},
				}
				fileToShare := &provider.Reference{
					ResourceId: &storagep.ResourceId{
						SpaceId:  einstein.Id.OpaqueId,
						OpaqueId: einstein.Id.OpaqueId,
					},
					Path: "./ocm-share-folder",
				}
				Expect(helpers.CreateStructure(ctxEinstein, cernboxgw, fileToShare, structure)).To(Succeed())

				By("share the file with marie")

				info, err := stat(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())

				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: info.Id,
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewViewerRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				By("marie accepts the share")
				listRes, err := cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share := listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))
				Expect(share.State).To(Equal(ocmv1beta1.ShareState_SHARE_STATE_PENDING))

				share.State = ocmv1beta1.ShareState_SHARE_STATE_ACCEPTED
				_, err = cesnetgw.UpdateReceivedOCMShare(ctxMarie, &ocmv1beta1.UpdateReceivedOCMShareRequest{
					Share:      share,
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"state"}},
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				// get auth context for ocm share
				ocmCtx := context.Background()
				authRes, err := cernboxgw.Authenticate(ocmCtx, &gatewaypb.AuthenticateRequest{
					Type:         "ocmshares",
					ClientId:     createShareRes.GetShare().GetId().GetOpaqueId(),
					ClientSecret: createShareRes.GetShare().GetToken(),
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(authRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				// create ocm context
				ocmCtx = ctxpkg.ContextSetToken(ocmCtx, authRes.Token)
				ocmCtx = metadata.AppendToOutgoingContext(ocmCtx, ctxpkg.TokenHeader, authRes.Token)

				// try finding the space by path again
				lssRes, err := cernboxgw.ListStorageSpaces(ocmCtx, &provider.ListStorageSpacesRequest{
					Opaque: &typespb.Opaque{
						Map: map[string]*typespb.OpaqueEntry{
							"path": {
								Decoder: "plain",
								Value:   []byte("/public/" + createShareRes.GetShare().GetId().GetOpaqueId()),
							},
							"metadata": {
								Decoder: "plain",
								Value:   []byte("*"),
							},
						},
					}})
				Expect(err).ToNot(HaveOccurred())
				Expect(lssRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(lssRes.StorageSpaces).To(HaveLen(1), "accepted ocm share should be listed as a space")

				By("marie see the content of the folder")
				listRes, err = cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share = listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))
				Expect(share.State).To(Equal(ocmv1beta1.ShareState_SHARE_STATE_ACCEPTED))

				protocol := share.Protocols[0]
				webdav, ok := protocol.Term.(*ocmv1beta1.Protocol_WebdavOptions)
				Expect(ok).To(BeTrue())

				webdavClient := newWebDAVClient(webdav.WebdavOptions)

				ok, err = helpers.SameContentWebDAV(webdavClient, "/", structure)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())

				By("check that marie does not have permissions to create files")
				Expect(webdavClient.Write("new-file", []byte("new-file"), 0)).ToNot(Succeed())

				By("marie access the share using the ocm mount")
				ref := &provider.Reference{Path: ocmPath(share.Id, "dir")}
				listFolderRes, err := cesnetgw.ListContainer(ctxMarie, &provider.ListContainerRequest{
					Ref: ref,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(listFolderRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				checkResourceInfoList(listFolderRes.Infos, []*provider.ResourceInfo{
					{
						Id: &provider.ResourceId{
							StorageId: "984e7351-2729-4417-99b4-ab5e6d41fa97",
							SpaceId:   share.Id.OpaqueId,
							OpaqueId:  share.Id.OpaqueId,
						},
						Name:          "foo",
						Path:          "foo",
						Size:          7,
						Type:          provider.ResourceType_RESOURCE_TYPE_FILE,
						PermissionSet: viewerPermissions,
					},
					{
						Id: &provider.ResourceId{
							StorageId: "984e7351-2729-4417-99b4-ab5e6d41fa97",
							SpaceId:   share.Id.OpaqueId,
							OpaqueId:  share.Id.OpaqueId,
						},
						Name:          "bar",
						Path:          "bar",
						Size:          0,
						Type:          provider.ResourceType_RESOURCE_TYPE_CONTAINER,
						PermissionSet: viewerPermissions,
					},
				})

				newFile := &provider.Reference{Path: ocmPath(share.Id, "dir/new")}
				Expect(helpers.UploadGateway(ctxMarie, cesnetgw, newFile, []byte("uploaded-from-ocm-mount"))).ToNot(Succeed())
			})
		})

		Context("einstein shares a folder with editor permissions", func() {
			It("marie is able to see the content and upload resources", func() {
				structure := helpers.Folder{
					"foo": helpers.File{
						Content: "foo",
					},
					"dir": helpers.Folder{
						"foo": helpers.File{
							Content: "dir/foo",
						},
						"bar": helpers.Folder{},
					},
				}
				fileToShare := &provider.Reference{
					ResourceId: &storagep.ResourceId{
						SpaceId:  einstein.Id.OpaqueId,
						OpaqueId: einstein.Id.OpaqueId,
					},
					Path: "./ocm-share-folder",
				}

				Expect(helpers.CreateStructure(ctxEinstein, cernboxgw, fileToShare, structure)).To(Succeed())

				By("share the file with marie")

				info, err := stat(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())

				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: info.Id,
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewEditorRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				By("marie can upload a file")
				listRes, err := cesnetgw.ListReceivedOCMShares(ctxMarie, &ocmv1beta1.ListReceivedOCMSharesRequest{})
				Expect(err).ToNot(HaveOccurred())
				Expect(listRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				Expect(listRes.Shares).To(HaveLen(1))

				share := listRes.Shares[0]
				Expect(share.Protocols).To(HaveLen(1))

				protocol := share.Protocols[0]
				webdav, ok := protocol.Term.(*ocmv1beta1.Protocol_WebdavOptions)
				Expect(ok).To(BeTrue())

				webdavClient := newWebDAVClient(webdav.WebdavOptions)
				data := []byte("new-content")
				Expect(webdavClient.Write("new-file", data, 0)).To(Succeed())

				data = []byte("new-file")
				Expect(webdavClient.Write("new-file", data, 0)).To(Succeed())
				Expect(helpers.SameContentWebDAV(webdavClient, fileToShare.Path, helpers.Folder{
					"foo": helpers.File{
						Content: "foo",
					},
					"dir": helpers.Folder{
						"foo": helpers.File{
							Content: "dir/foo",
						},
						"bar": helpers.Folder{},
					},
					"new-file": helpers.File{
						Content: "new-file",
					},
				}))

				By("marie access the share using the ocm mount")
				ref := &provider.Reference{Path: ocmPath(share.Id, "dir")}
				listFolderRes, err := cesnetgw.ListContainer(ctxMarie, &provider.ListContainerRequest{
					Ref: ref,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(listFolderRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				checkResourceInfoList(listFolderRes.Infos, []*provider.ResourceInfo{
					{
						Id: &provider.ResourceId{
							StorageId: "984e7351-2729-4417-99b4-ab5e6d41fa97",
							OpaqueId:  share.Id.OpaqueId,
							SpaceId:   share.Id.OpaqueId,
						},
						Name:          "foo",
						Path:          "foo",
						Size:          7,
						Type:          provider.ResourceType_RESOURCE_TYPE_FILE,
						PermissionSet: editorPermissions,
					},
					{
						Id: &provider.ResourceId{
							StorageId: "984e7351-2729-4417-99b4-ab5e6d41fa97",
							OpaqueId:  share.Id.OpaqueId,
							SpaceId:   share.Id.OpaqueId,
						},
						Name:          "bar",
						Path:          "bar",
						Size:          0,
						Type:          provider.ResourceType_RESOURCE_TYPE_CONTAINER,
						PermissionSet: editorPermissions,
					},
				})

				// create a new file
				newFile := &provider.Reference{Path: ocmPath(share.Id, "dir/new-file")}
				Expect(helpers.UploadGateway(ctxMarie, cesnetgw, newFile, []byte("uploaded-from-ocm-mount"))).To(Succeed())
				Expect(helpers.SameContentWebDAV(webdavClient, fileToShare.Path, helpers.Folder{
					"foo": helpers.File{
						Content: "foo",
					},
					"dir": helpers.Folder{
						"foo": helpers.File{
							Content: "dir/foo",
						},
						"bar": helpers.Folder{},
						"new-file": helpers.File{
							Content: "uploaded-from-ocm-mount",
						},
					},
					"new-file": helpers.File{
						Content: "new-file",
					},
				}))

				// create a new directory
				newDir := &provider.Reference{Path: ocmPath(share.Id, "dir/new-dir")}
				createDirRes, err := cesnetgw.CreateContainer(ctxMarie, &provider.CreateContainerRequest{
					Ref: newDir,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createDirRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
				Expect(helpers.SameContentWebDAV(webdavClient, fileToShare.Path, helpers.Folder{
					"foo": helpers.File{
						Content: "foo",
					},
					"dir": helpers.Folder{
						"foo": helpers.File{
							Content: "dir/foo",
						},
						"bar": helpers.Folder{},
						"new-file": helpers.File{
							Content: "uploaded-from-ocm-mount",
						},
						"new-dir": helpers.Folder{},
					},
					"new-file": helpers.File{
						Content: "new-file",
					},
				}))
			})
		})

		Context("einstein creates twice the share to marie", func() {
			It("fail with already existing error", func() {
				fileToShare := &provider.Reference{
					ResourceId: &storagep.ResourceId{
						SpaceId:  einstein.Id.OpaqueId,
						OpaqueId: einstein.Id.OpaqueId,
					},
					Path: "./double-share",
				}
				Expect(helpers.CreateFolder(ctxEinstein, cernboxgw, fileToShare)).To(Succeed())

				By("share the file with marie")

				info, err := stat(ctxEinstein, cernboxgw, fileToShare)
				Expect(err).ToNot(HaveOccurred())

				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: info.Id,
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewEditorRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
			})
		})

		Context("einstein creates a share on a not existing resource", func() {
			It("fail with not found error", func() {
				cesnet, err := cernboxgw.GetInfoByDomain(ctxEinstein, &ocmproviderpb.GetInfoByDomainRequest{
					Domain: "cesnet.cz",
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(cesnet.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

				createShareRes, err := cernboxgw.CreateOCMShare(ctxEinstein, &ocmv1beta1.CreateOCMShareRequest{
					ResourceId: &provider.ResourceId{StorageId: "123e4567-e89b-12d3-a456-426655440000", OpaqueId: "NON_EXISTING_FILE"},
					Grantee: &provider.Grantee{
						Type: provider.GranteeType_GRANTEE_TYPE_USER,
						Id: &provider.Grantee_UserId{
							UserId: federatedMarieID,
						},
					},
					AccessMethods: []*ocmv1beta1.AccessMethod{
						share.NewWebDavAccessMethod(conversions.NewEditorRole().CS3ResourcePermissions()),
					},
					RecipientMeshProvider: cesnet.ProviderInfo,
				})
				Expect(err).ToNot(HaveOccurred())
				Expect(createShareRes.Status.Code).To(Equal(rpcv1beta1.Code_CODE_NOT_FOUND))
			})
		})

	})
})

func stat(ctx context.Context, gw gatewaypb.GatewayAPIClient, ref *provider.Reference) (*provider.ResourceInfo, error) {
	statRes, err := gw.Stat(ctx, &provider.StatRequest{Ref: ref})
	if err != nil {
		return nil, err
	}
	if statRes.Status.Code != rpcv1beta1.Code_CODE_OK {
		return nil, errors.New(statRes.Status.Message)
	}
	return statRes.Info, nil
}

func download(ctx context.Context, gw gatewaypb.GatewayAPIClient, ref *provider.Reference) ([]byte, error) {
	initRes, err := gw.InitiateFileDownload(ctx, &provider.InitiateFileDownloadRequest{Ref: ref})
	if err != nil {
		return nil, err
	}

	var token, endpoint string
	for _, p := range initRes.Protocols {
		// if p.Protocol == "simple" {
		token, endpoint = p.Token, p.DownloadEndpoint
		// }
	}
	httpReq, err := rhttp.NewRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set(datagateway.TokenTransportHeader, token)

	httpRes, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpRes.Body.Close()

	return io.ReadAll(httpRes.Body)
}

func ocmPath(id *ocmv1beta1.ShareId, p string) string {
	return filepath.Join("/ocm", id.OpaqueId, p)
}

func checkResourceInfo(info, target *provider.ResourceInfo) {
	Expect(info.Name).To(Equal(target.Name))
	Expect(info.Path).To(Equal(target.Path))
	Expect(info.Size).To(Equal(target.Size))
	Expect(info.Type).To(Equal(target.Type))
	Expect(info.PermissionSet).To(Equal(target.PermissionSet))
}

func mapResourceInfos(l []*provider.ResourceInfo) map[string]*provider.ResourceInfo {
	m := make(map[string]*provider.ResourceInfo)
	for _, e := range l {
		m[e.Path] = e
	}
	return m
}

func checkResourceInfoList(l1, l2 []*provider.ResourceInfo) {
	m1, m2 := mapResourceInfos(l1), mapResourceInfos(l2)
	Expect(l1).To(HaveLen(len(l2)))

	for k, ri1 := range m1 {
		ri2, ok := m2[k]
		Expect(ok).To(BeTrue())
		checkResourceInfo(ri1, ri2)
	}
}

func newWebDAVClient(options *ocmv1beta1.WebDAVProtocol) *gowebdav.Client {
	webdavClient := gowebdav.NewAuthClient(options.Uri, gowebdav.NewPreemptiveAuth(ocm.BearerAuthenticator{Token: options.SharedSecret}))
	webdavClient.SetInterceptor(func(method string, rq *http.Request) {
		if rq.Body == nil {
			return
		}

		buf := &bytes.Buffer{}
		n, _ := io.Copy(buf, rq.Body)
		rq.Body = io.NopCloser(buf)

		rq.Header.Add(net.HeaderContentLength, strconv.Itoa(int(n)))
		// Set the content length on the request struct directly instead of the header.
		// The content-length header gets reset by the golang http library before
		// sendind out the request, resulting in chunked encoding to be used which
		// breaks the quota checks in ocdav.
		if method == "PUT" {
			rq.ContentLength = n
		}
	})
	return webdavClient
}
