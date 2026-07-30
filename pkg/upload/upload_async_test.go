// Copyright 2018-2024 CERN
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

// Async upload tests at the coordinator level. These are the driver-agnostic
// successor to decomposedfs/upload_async_test.go: the same scenarios, driven
// through pkg/upload instead of the driver's own upload machinery, with
// decomposedfs behind the CommitUpload/PrepareUpload/RollbackUpload seam.

package upload_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	v1beta11 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"

	"github.com/owncloud/reva/v2/pkg/appctx"
	ruser "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/events/stream"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/cache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/aspects"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/lookup"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/options"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/permissions"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/permissions/mocks"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/timemanager"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/tree"
	treemocks "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/tree/mocks"
	"github.com/owncloud/reva/v2/pkg/storagespace"
	"github.com/owncloud/reva/v2/pkg/store"
	pkgupload "github.com/owncloud/reva/v2/pkg/upload"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/owncloud/reva/v2/tests/helpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Async uploads via the coordinator", func() {
	var (
		ref = &provider.Reference{
			ResourceId: &provider.ResourceId{SpaceId: "u-s-e-r-id"},
			Path:       "/foo",
		}
		rootRef = &provider.Reference{
			ResourceId: &provider.ResourceId{SpaceId: "u-s-e-r-id", OpaqueId: "u-s-e-r-id"},
			Path:       "/",
		}

		user = &userpb.User{
			Id: &userpb.UserId{
				Idp:      "idp",
				OpaqueId: "u-s-e-r-id",
				Type:     userpb.UserType_USER_TYPE_PRIMARY,
			},
			Username: "username",
		}

		firstContent  = []byte("0123456789")
		secondContent = []byte("01234567890123456789")

		ctx context.Context

		pub, con chan interface{}
		uploadID string

		fs       storage.FS
		coord    pkgupload.Coordinator
		evstream stream.Chan
		o        *options.Options
		bs       *treemocks.Blobstore

		// Set in the outer BeforeEach and overridable by an inner one, then acted on
		// in JustBeforeEach, which runs after both.
		startAsync bool
		mountID    string

		// uploadedInfo is what the last coord.Upload reported. PUT handlers turn it
		// into response headers, so it is part of the contract, not a by-product.
		uploadedInfo *provider.ResourceInfo

		// initiateAndUpload runs a full upload through the coordinator and returns
		// the session id, without asserting anything about what it published.
		initiateAndUpload = func(content []byte) string {
			ids, err := coord.InitiateUpload(ctx, ref, int64(len(content)), map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(ids["simple"]).ToNot(BeEmpty())

			uploadedInfo, err = coord.Upload(ctx, storage.UploadRequest{
				Ref:    &provider.Reference{Path: "/" + ids["simple"]},
				Body:   io.NopCloser(bytes.NewReader(content)),
				Length: int64(len(content)),
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			return ids["simple"]
		}

		// upload stages an upload and leaves it awaiting postprocessing. Only valid
		// on the async path: inline uploads publish UploadReady, not BytesReceived.
		upload = func(content []byte) string {
			id := initiateAndUpload(content)

			ev, ok := (<-pub).(events.BytesReceived)
			Expect(ok).To(BeTrue(), "expected BytesReceived: the upload must not commit before postprocessing")
			Expect(ev.UploadID).To(Equal(id))
			Expect(ev.URL).ToNot(BeEmpty(), "postprocessing needs a URL to fetch the staged bytes from")

			return id
		}

		succeedPostprocessing = func(id string) {
			con <- events.PostprocessingFinished{UploadID: id, Outcome: events.PPOutcomeContinue}
			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue())
			Expect(ev.Failed).To(BeFalse())
			Expect(ev.ResourceID).ToNot(BeNil())
			Expect(ev.ResourceID.OpaqueId).ToNot(BeEmpty())
			Expect(ev.ResourceID.OpaqueId).ToNot(Equal(ev.ResourceID.SpaceId),
				"ResourceID.OpaqueId should be the file node ID, not the space ID")
		}

		failPostprocessing = func(id string, outcome events.PostprocessingOutcome) {
			con <- events.PostprocessingFinished{UploadID: id, Outcome: outcome}
			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue())
			Expect(ev.Failed).To(BeTrue())
		}

		// fileStatus reports whether the file exists, its processing status and size.
		fileStatus = func() (bool, string, int) {
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(BeElementOf([2]int{0, 1}), "should not have more than one child")
			if len(resources) == 0 {
				return false, "", 0
			}
			item := resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			return true, utils.ReadPlainFromOpaque(item.Opaque, "status"), int(item.GetSize())
		}
		parentSize = func() int {
			info, err := fs.GetMD(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			return int(info.Size)
		}
		revisionCount = func() int {
			revisions, err := fs.ListRevisions(ctx, ref)
			Expect(err).ToNot(HaveOccurred())
			return len(revisions)
		}
		stagedBytesExist = func(id string) bool {
			_, err := os.Stat(filepath.Join(o.Root, "uploads", id))
			return err == nil
		}
	)

	BeforeEach(func() {
		zl := zerolog.New(os.Stdout).Level(zerolog.ErrorLevel)
		ctx = appctx.WithLogger(ruser.ContextSetUser(context.Background(), user), &zl)

		tmpRoot, err := helpers.TempDir("reva-unit-tests-*-root")
		Expect(err).ToNot(HaveOccurred())

		// asyncfileuploads stays false: the driver must not start its own
		// postprocessing consumer, because the coordinator now owns that role.
		o, err = options.New(map[string]interface{}{
			"root":                tmpRoot,
			"treetime_accounting": true,
			"treesize_accounting": true,
		})
		Expect(err).ToNot(HaveOccurred())

		lu := lookup.New(metadata.NewXattrsBackend(o.Root, cache.Config{}), o, &timemanager.Manager{})
		pmock := &mocks.PermissionsChecker{}

		cs3permissionsclient := &mocks.CS3PermissionsClient{}
		pool.RemoveSelector("PermissionsSelector" + "any")
		permissionsSelector := pool.GetSelector[cs3permissions.PermissionsAPIClient](
			"PermissionsSelector", "any",
			func(cc grpc.ClientConnInterface) cs3permissions.PermissionsAPIClient {
				return cs3permissionsclient
			},
		)
		bs = &treemocks.Blobstore{}

		cs3permissionsclient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(
			&cs3permissions.CheckPermissionResponse{Status: &v1beta11.Status{Code: v1beta11.Code_CODE_OK}}, nil).Times(1)

		pmock.On("AssemblePermissions", mock.Anything, mock.Anything).
			Return(&provider.ResourcePermissions{
				Stat:               true,
				GetQuota:           true,
				InitiateFileUpload: true,
				ListContainer:      true,
				ListFileVersions:   true,
			}, nil)

		pub, con = make(chan interface{}), make(chan interface{})
		evstream = stream.Chan{pub, con}
		t := tree.New(lu, bs, o, store.Create(), &zerolog.Logger{})

		fs, err = decomposedfs.New(o, aspects.Aspects{
			Lookup:      lu,
			Tree:        t,
			Permissions: permissions.NewPermissions(pmock, permissionsSelector),
			EventStream: evstream,
			Trashbin:    &decomposedfs.DecomposedfsTrashbin{},
		}, &zerolog.Logger{})
		Expect(err).ToNot(HaveOccurred())

		// The coordinator stages sessions under the same root the driver reads.
		sessionStore := pkgupload.NewFileStore(o.Root, pkgupload.TokenOptions{
			DownloadEndpoint:     "http://localhost:9200/data/",
			DataGatewayEndpoint:  "http://localhost:9200/data/",
			TransferSharedSecret: "changemeplease",
			TransferExpires:      86400,
		}, &zl)
		Expect(sessionStore.Setup()).To(Succeed())
		coord = pkgupload.NewCoordinator(fs, sessionStore, filepath.Join(o.Root, "uploads"), evstream)

		resp, err := fs.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{Owner: user, Type: "personal"})
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.Status.Code).To(Equal(v1beta11.Code_CODE_OK))
		resID, err := storagespace.ParseID(resp.StorageSpace.Id.OpaqueId)
		Expect(err).ToNot(HaveOccurred())
		ref.ResourceId = &resID

		// CommitUpload streams the staged bytes, so the blob arrives as a reader.
		bs.On("UploadFromReader", mock.AnythingOfType("*node.Node"), mock.Anything, mock.AnythingOfType("int64")).
			Return(nil).
			Run(func(args mock.Arguments) {
				n := args.Get(0).(*node.Node)
				data, err := io.ReadAll(args.Get(1).(io.Reader))
				Expect(err).ToNot(HaveOccurred())
				Expect(len(data)).To(Equal(int(n.Blobsize)), "the committed blob must match the declared size")
			})

		// Most scenarios test the async path. Inner blocks flip these before
		// JustBeforeEach acts on them.
		startAsync, mountID = true, ""
	})

	JustBeforeEach(func() {
		// Starting the consumer is what switches the coordinator to async uploads;
		// the two are inseparable by design, so there is no separate flag to set.
		if startAsync {
			Expect(coord.StartPostprocessing(evstream, "coordinator-test", mountID, 1)).To(Succeed())
			uploadID = upload(firstContent)
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)
		}
	})

	AfterEach(func() {
		if o.Root != "" {
			os.RemoveAll(o.Root)
		}
		close(pub)
		close(con)
	})

	When("the uploaded file is new", func() {
		It("is visible as processing and commits only after postprocessing succeeds", func() {
			exists, status, _ := fileStatus()
			Expect(exists).To(BeTrue())
			Expect(status).To(Equal("processing"))

			succeedPostprocessing(uploadID)

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
			exists, status, size := fileStatus()
			Expect(exists).To(BeTrue())
			Expect(status).To(BeEmpty())
			Expect(size).To(Equal(len(firstContent)))
			Expect(stagedBytesExist(uploadID)).To(BeFalse(), "staged bytes should be cleaned up")
		})

		It("deletes node and bytes when instructed", func() {
			Expect(stagedBytesExist(uploadID)).To(BeTrue())

			failPostprocessing(uploadID, events.PPOutcomeDelete)

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)
			exists, _, _ := fileStatus()
			Expect(exists).To(BeFalse(), "node should be gone")
			Expect(stagedBytesExist(uploadID)).To(BeFalse(), "bytes should be gone")
		})

		It("removes the node but keeps the bytes when aborted", func() {
			failPostprocessing(uploadID, events.PPOutcomeAbort)

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)
			exists, _, _ := fileStatus()
			Expect(exists).To(BeFalse(), "node should be gone")
			Expect(stagedBytesExist(uploadID)).To(BeTrue(), "an abort keeps the bytes so it can be restarted")
		})
	})

	When("the uploaded file creates a new version", func() {
		var secondUploadID string

		// JustBeforeEach, not BeforeEach: this builds on the upload the outer
		// JustBeforeEach stages, which has not happened yet at BeforeEach time.
		JustBeforeEach(func() {
			succeedPostprocessing(uploadID)
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
			Expect(revisionCount()).To(Equal(0))

			secondUploadID = upload(secondContent)
		})

		It("succeeds eventually, creating a new version", func() {
			succeedPostprocessing(secondUploadID)

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 2)
			Expect(revisionCount()).To(Equal(1))
			_, status, size := fileStatus()
			Expect(status).To(BeEmpty())
			Expect(size).To(Equal(len(secondContent)))
			Expect(parentSize()).To(Equal(len(secondContent)))
		})

		It("removes the new version and restores the old one when instructed", func() {
			failPostprocessing(secondUploadID, events.PPOutcomeDelete)

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
			Expect(revisionCount()).To(Equal(0))
			_, status, size := fileStatus()
			Expect(status).To(BeEmpty())
			Expect(size).To(Equal(len(firstContent)), "the previous content must be restored")
			Expect(parentSize()).To(Equal(len(firstContent)))
		})
	})

	When("two uploads to the same file are processed in parallel", func() {
		var secondUploadID string

		JustBeforeEach(func() {
			succeedPostprocessing(uploadID)
			// Both uploads are staged before either is postprocessed.
			uploadID = upload(firstContent)
			secondUploadID = upload(secondContent)
		})

		It("keeps the processing status until the last upload finished", func() {
			succeedPostprocessing(uploadID)
			_, status, _ := fileStatus()
			Expect(status).To(Equal("processing"), "the second upload is still processing")

			succeedPostprocessing(secondUploadID)
			_, status, _ = fileStatus()
			Expect(status).To(BeEmpty())
		})

		It("ends with the content of the last upload to finish", func() {
			succeedPostprocessing(uploadID)
			succeedPostprocessing(secondUploadID)

			_, _, size := fileStatus()
			Expect(size).To(Equal(len(secondContent)))
			Expect(parentSize()).To(Equal(len(secondContent)))
		})

		It("keeps the first upload when the second is deleted", func() {
			succeedPostprocessing(uploadID)
			failPostprocessing(secondUploadID, events.PPOutcomeDelete)

			exists, _, size := fileStatus()
			Expect(exists).To(BeTrue(), "the first upload must survive the second being deleted")
			Expect(size).To(Equal(len(firstContent)))
			Expect(parentSize()).To(Equal(len(firstContent)))
		})

		It("keeps the second upload when the first is deleted", func() {
			failPostprocessing(uploadID, events.PPOutcomeDelete)
			succeedPostprocessing(secondUploadID)

			exists, _, size := fileStatus()
			Expect(exists).To(BeTrue())
			Expect(size).To(Equal(len(secondContent)))
		})
	})

	// PUT goes through coord.Upload and writes the returned etag/mtime/id straight
	// into response headers. The desktop client stores the etag to detect later
	// changes, so an empty one makes it re-download the file it just sent.
	When("a PUT-style upload reports its result", func() {
		It("carries an etag, mtime and id while still processing", func() {
			Expect(uploadedInfo).ToNot(BeNil())
			Expect(uploadedInfo.GetEtag()).ToNot(BeEmpty(), "PUT sets an ETag header from this")
			Expect(uploadedInfo.GetMtime()).ToNot(BeNil(), "PUT sets Last-Modified from this")
			Expect(uploadedInfo.GetId().GetOpaqueId()).ToNot(BeEmpty(), "PUT sets the file id header from this")
		})

		It("carries an etag for a new version too", func() {
			succeedPostprocessing(uploadID)

			upload(secondContent)
			Expect(uploadedInfo.GetEtag()).ToNot(BeEmpty())
			Expect(uploadedInfo.GetId().GetOpaqueId()).ToNot(BeEmpty())
		})
	})

	When("postprocessing was never started", func() {
		BeforeEach(func() {
			startAsync = false
		})

		// The guarantee that keeps the two switches from drifting apart: with no
		// consumer running, nothing would ever arrive to finish a deferred upload, so
		// the coordinator must commit inline instead of staging and waiting forever.
		It("commits inline instead of waiting for a scan that will never come", func() {
			id := initiateAndUpload(firstContent)

			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue(), "an inline commit announces the file directly, without a scan round trip")
			Expect(ev.Failed).To(BeFalse())

			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
			exists, status, size := fileStatus()
			Expect(exists).To(BeTrue())
			Expect(status).To(BeEmpty(), "the upload must not be left in processing")
			Expect(size).To(Equal(len(firstContent)))
			Expect(stagedBytesExist(id)).To(BeFalse())
		})
	})

	When("the coordinator serves a specific storage", func() {
		BeforeEach(func() {
			mountID = "storage-users-1"
		})

		// Postprocessing broadcasts its results to every storage provider, so each
		// has to recognise its own. Acting on another storage's event would commit an
		// upload this provider knows nothing about.
		It("ignores results belonging to a different storage", func() {
			otherStorage := upload(secondContent)

			// One consumer processes the stream in order, so once the second result
			// has been acted on the first has already been seen and dropped.
			con <- events.PostprocessingFinished{
				UploadID:   otherStorage,
				Outcome:    events.PPOutcomeContinue,
				ResourceID: &provider.ResourceId{StorageId: "some-other-storage"},
			}
			con <- events.PostprocessingFinished{
				UploadID:   uploadID,
				Outcome:    events.PPOutcomeContinue,
				ResourceID: &provider.ResourceId{StorageId: mountID},
			}

			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue())
			Expect(ev.Failed).To(BeFalse())
			Expect(ev.UploadID).To(Equal(uploadID), "the other storage's upload must not be announced")

			// Only the upload addressed to this storage was committed; the other one is
			// left untouched for the provider that actually owns it.
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
			Expect(stagedBytesExist(otherStorage)).To(BeTrue(), "a filtered upload must be left alone, not cleaned up")
		})
	})
})
