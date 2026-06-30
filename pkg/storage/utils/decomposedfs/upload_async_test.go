package decomposedfs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	cs3permissions "github.com/cs3org/go-cs3apis/cs3/permissions/v1beta1"
	v1beta11 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	ruser "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/events/stream"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/cache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/aspects"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/lookup"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/options"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/permissions"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/permissions/mocks"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/timemanager"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/tree"
	treemocks "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/tree/mocks"
	"github.com/owncloud/reva/v2/pkg/storagespace"
	"github.com/owncloud/reva/v2/pkg/store"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/owncloud/reva/v2/tests/helpers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"google.golang.org/grpc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Async file uploads", Ordered, func() {
	var (
		ref = &provider.Reference{
			ResourceId: &provider.ResourceId{
				SpaceId: "u-s-e-r-id",
			},
			Path: "/foo",
		}

		rootRef = &provider.Reference{
			ResourceId: &provider.ResourceId{
				SpaceId:  "u-s-e-r-id",
				OpaqueId: "u-s-e-r-id",
			},
			Path: "/",
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

		pub      chan interface{}
		con      chan interface{}
		uploadID string

		fs                   storage.FS
		o                    *options.Options
		lu                   *lookup.Lookup
		pmock                *mocks.PermissionsChecker
		cs3permissionsclient *mocks.CS3PermissionsClient
		permissionsSelector  pool.Selectable[cs3permissions.PermissionsAPIClient]
		bs                   *treemocks.Blobstore

		succeedPostprocessing = func(uploadID string) {
			// finish postprocessing
			con <- events.PostprocessingFinished{
				UploadID: uploadID,
				Outcome:  events.PPOutcomeContinue,
			}
			// wait for upload to be ready
			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue())
			Expect(ev.Failed).To(BeFalse())
			Expect(ev.ResourceID).ToNot(BeNil())
			Expect(ev.ResourceID.OpaqueId).ToNot(BeEmpty())
			Expect(ev.ResourceID.OpaqueId).ToNot(Equal(ev.ResourceID.SpaceId), "ResourceID.OpaqueId should be the file node ID, not the space ID")
		}

		failPostprocessing = func(uploadID string, outcome events.PostprocessingOutcome) {
			// finish postprocessing
			con <- events.PostprocessingFinished{
				UploadID: uploadID,
				Outcome:  outcome,
			}
			// wait for upload to be ready
			ev, ok := (<-pub).(events.UploadReady)
			Expect(ok).To(BeTrue())
			Expect(ev.Failed).To(BeTrue())
		}

		fileStatus = func() (bool, string, int) {
			// check processing status
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(BeElementOf([2]int{0, 1}), "should not have more than one child")

			item := resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			return len(resources) == 1, utils.ReadPlainFromOpaque(item.Opaque, "status"), int(item.GetSize())
		}
		// tusUpload writes content via the coordinator TUS path (GetUpload →
		// WriteChunk → FinishUpload) instead of the legacy fs.Upload path.
		// This is needed because coordinator.InitiateUpload calls TouchFile, so
		// the node already exists; FinishUploadDecomposed (legacy path) would
		// try to create it again and fail with EEXIST.
		tusUpload = func(id string, content []byte) {
			ds, ok := fs.(tusd.DataStore)
			Expect(ok).To(BeTrue(), "fs must implement tusd.DataStore")
			up, err := ds.GetUpload(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			_, err = up.WriteChunk(ctx, 0, bytes.NewReader(content))
			Expect(err).ToNot(HaveOccurred())
			Expect(up.FinishUpload(ctx)).To(Succeed())
		}

		parentSize = func() int {
			parentInfo, err := fs.GetMD(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			return int(parentInfo.Size)
		}
		revisionCount = func() int {
			revisions, err := fs.ListRevisions(ctx, ref)
			Expect(err).ToNot(HaveOccurred())
			return len(revisions)
		}
	)

	BeforeEach(func() {
		zl := zerolog.New(os.Stdout).Level(zerolog.DebugLevel)
		ctx = appctx.WithLogger(ruser.ContextSetUser(context.Background(), user), &zl)

		// setup test
		tmpRoot, err := helpers.TempDir("reva-unit-tests-*-root")
		Expect(err).ToNot(HaveOccurred())

		o, err = options.New(map[string]interface{}{
			"root":                tmpRoot,
			"asyncfileuploads":    true,
			"treetime_accounting": true,
			"treesize_accounting": true,
		})
		Expect(err).ToNot(HaveOccurred())

		lu = lookup.New(metadata.NewXattrsBackend(o.Root, cache.Config{}), o, &timemanager.Manager{})
		pmock = &mocks.PermissionsChecker{}

		cs3permissionsclient = &mocks.CS3PermissionsClient{}
		pool.RemoveSelector("PermissionsSelector" + "any")
		permissionsSelector = pool.GetSelector[cs3permissions.PermissionsAPIClient](
			"PermissionsSelector",
			"any",
			func(cc grpc.ClientConnInterface) cs3permissions.PermissionsAPIClient {
				return cs3permissionsclient
			},
		)
		bs = &treemocks.Blobstore{}

		// create space uses CheckPermission endpoint
		cs3permissionsclient.On("CheckPermission", mock.Anything, mock.Anything, mock.Anything).Return(&cs3permissions.CheckPermissionResponse{
			Status: &v1beta11.Status{Code: v1beta11.Code_CODE_OK},
		}, nil).Times(1)

		// for this test we don't care about permissions
		pmock.On("AssemblePermissions", mock.Anything, mock.Anything).
			Return(&provider.ResourcePermissions{
				Stat:               true,
				GetQuota:           true,
				InitiateFileUpload: true,
				ListContainer:      true,
				ListFileVersions:   true,
			}, nil)

		// setup fs
		pub, con = make(chan interface{}), make(chan interface{})
		tree := tree.New(lu, bs, o, store.Create(), &zerolog.Logger{})

		aspects := aspects.Aspects{
			Lookup:      lu,
			Tree:        tree,
			Permissions: permissions.NewPermissions(pmock, permissionsSelector),
			EventStream: stream.Chan{pub, con},
			Trashbin:    &DecomposedfsTrashbin{},
		}
		dfs, err := New(o, aspects, &zerolog.Logger{})
		Expect(err).ToNot(HaveOccurred())
		// Wire the coordinator so postprocessing events are consumed during tests.
		fs, err = dfs.(*Decomposedfs).UploadCoordinator(aspects.EventStream, &zerolog.Logger{})
		Expect(err).ToNot(HaveOccurred())

		resp, err := fs.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{Owner: user, Type: "personal"})
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.Status.Code).To(Equal(v1beta11.Code_CODE_OK))
		resID, err := storagespace.ParseID(resp.StorageSpace.Id.OpaqueId)
		Expect(err).ToNot(HaveOccurred())
		ref.ResourceId = &resID

		bs.On("UploadFromReader", mock.AnythingOfType("*node.Node"), mock.Anything, mock.AnythingOfType("int64")).
			Return(nil)

		// start upload of a file
		uploadIds, err := fs.InitiateUpload(ctx, ref, 10, map[string]string{})
		Expect(err).ToNot(HaveOccurred())
		Expect(len(uploadIds)).To(Equal(2))
		Expect(uploadIds["simple"]).ToNot(BeEmpty())
		Expect(uploadIds["tus"]).ToNot(BeEmpty())

		uploadID = uploadIds["simple"]
		tusUpload(uploadID, firstContent)

		// wait for bytes received event
		_, ok := (<-pub).(events.BytesReceived)
		Expect(ok).To(BeTrue())

		// blobstore not called yet
		bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)
	})

	AfterEach(func() {
		if o.Root != "" {
			os.RemoveAll(o.Root)
		}
		close(pub)
		close(con)
	})

	When("the uploaded file is new", func() {
		It("succeeds eventually", func() {
			// node is created
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))

			item := resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			Expect(utils.ReadPlainFromOpaque(item.Opaque, "status")).To(Equal("processing"))

			succeedPostprocessing(uploadID)

			// blobstore called now
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)

			// node ready
			resources, err = fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))

			item = resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			Expect(utils.ReadPlainFromOpaque(item.Opaque, "status")).To(BeEmpty())

		})

		It("deletes node and bytes when instructed", func() {
			// node is created
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))

			item := resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			Expect(utils.ReadPlainFromOpaque(item.Opaque, "status")).To(Equal("processing"))

			// bytes are in dedicated path
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).To(BeNil())

			failPostprocessing(uploadID, events.PPOutcomeDelete)

			// blobstore still not called now
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)

			// node gone
			resources, err = fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(0))

			// bytes gone
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).ToNot(BeNil())
		})

		It("releases the quota and removes the node when the node metadata is unreadable", func() {
			// node is created and the optimistic size has been propagated
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))
			Expect(parentSize()).To(Equal(len(firstContent)))

			// simulate an orphaned node: the node file is still there but its
			// metadata is gone, e.g. because an ancestor was trashed while the
			// upload was in flight. Reading the node now fails. Purge instead of
			// removing the file directly, so the cached attributes go as well.
			nodePath := lu.InternalPath(ref.GetResourceId().GetSpaceId(), resources[0].GetId().GetOpaqueId())
			Expect(lu.MetadataBackend().Purge(ctx, nodePath)).To(Succeed())
			_, err = node.ReadNode(ctx, lu, ref.GetResourceId().GetSpaceId(), resources[0].GetId().GetOpaqueId(), false, nil, true)
			Expect(err).To(HaveOccurred(), "node should be unreadable after purging its metadata")

			// No UploadReady event is published for an orphaned session: there is
			// no node left to report on. Wait for the bytes to be cleaned up
			// instead of for an event that will never arrive.
			con <- events.PostprocessingFinished{
				UploadID: uploadID,
				Outcome:  events.PPOutcomeContinue,
			}
			Eventually(func() bool {
				_, err := os.Stat(filepath.Join(o.Root, "uploads", uploadID))
				return err != nil
			}).Should(BeTrue(), "the upload bytes should be cleaned up")

			// the blob was never written
			bs.AssertNumberOfCalls(GinkgoT(), "Upload", 0)

			// the orphaned node is gone ...
			_, err = os.Stat(nodePath)
			Expect(err).ToNot(BeNil())

			// ... and most importantly the quota has been released
			Eventually(parentSize).Should(Equal(0))
		})

		It("deletes node and keeps the bytes when instructed", func() {
			// node is created
			resources, err := fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(1))

			item := resources[0]
			Expect(item.Path).To(Equal(ref.Path))
			Expect(utils.ReadPlainFromOpaque(item.Opaque, "status")).To(Equal("processing"))

			// bytes are in dedicated path
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).To(BeNil())

			failPostprocessing(uploadID, events.PPOutcomeAbort)

			// blobstore still not called now
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 0)

			// node gone
			resources, err = fs.ListFolder(ctx, rootRef, []string{}, []string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(resources)).To(Equal(0))

			// bytes are still here
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).To(BeNil())
		})
	})

	When("the uploaded file creates a new version", func() {
		JustBeforeEach(func() {
			succeedPostprocessing(uploadID)

			// make sure there is no version yet
			revs, err := fs.ListRevisions(ctx, ref)
			Expect(err).To(BeNil())
			Expect(len(revs)).To(Equal(0))

			// upload again
			uploadIds, err := fs.InitiateUpload(ctx, ref, 10, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(uploadIds)).To(Equal(2))
			Expect(uploadIds["simple"]).ToNot(BeEmpty())
			Expect(uploadIds["tus"]).ToNot(BeEmpty())

			uploadID = uploadIds["simple"]
			tusUpload(uploadID, firstContent)

			// wait for bytes received event
			_, ok := (<-pub).(events.BytesReceived)
			Expect(ok).To(BeTrue())

			// version is not yet created at this point — it will be created when
			// CommitUpload runs on PostprocessingFinished, not at InitiateUpload time.
			revs, err = fs.ListRevisions(ctx, ref)
			Expect(err).To(BeNil())
			Expect(len(revs)).To(Equal(0))

			// at this stage: blobstore called once for the original file
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)

		})

		It("succeeds eventually, creating a new version", func() {
			succeedPostprocessing(uploadID)

			// version still existing
			revs, err := fs.ListRevisions(ctx, ref)
			Expect(err).To(BeNil())
			Expect(len(revs)).To(Equal(1))

			// blobstore now called twice - for original file and new version
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 2)

			// bytes are gone from upload path
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).ToNot(BeNil())
		})

		It("removes new version and restores old one when instructed", func() {
			_, status, _ := fileStatus()
			Expect(status).To(Equal("processing"))

			failPostprocessing(uploadID, events.PPOutcomeDelete)

			_, status, _ = fileStatus()
			Expect(status).To(Equal(""))

			// version gone now
			revs, err := fs.ListRevisions(ctx, ref)
			Expect(err).To(BeNil())
			Expect(len(revs)).To(Equal(0))

			// bytes are removed from upload path
			_, err = os.Stat(filepath.Join(o.Root, "uploads", uploadID))
			Expect(err).ToNot(BeNil())

			// blobstore still called only once for the original file
			bs.AssertNumberOfCalls(GinkgoT(), "UploadFromReader", 1)
		})

	})
	When("a second upload is attempted while the first is still in postprocessing", func() {
		// The coordinator serializes uploads via MarkProcessing: a second FinishUpload
		// call while the first session holds the processing slot returns ResourceProcessing
		// and the second session is cleaned up immediately.

		It("rejects the second FinishUpload with ResourceProcessing", func() {
			// First upload is in postprocessing (BytesReceived consumed in BeforeEach).
			// Initiate a second upload.
			uploadIds, err := fs.InitiateUpload(ctx, ref, 20, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			secondUploadID := uploadIds["simple"]

			// Write bytes for the second upload.
			ds, ok := fs.(tusd.DataStore)
			Expect(ok).To(BeTrue())
			up, err := ds.GetUpload(ctx, secondUploadID)
			Expect(err).ToNot(HaveOccurred())
			_, err = up.WriteChunk(ctx, 0, bytes.NewReader(secondContent))
			Expect(err).ToNot(HaveOccurred())

			// FinishUpload must fail because the node is already processing.
			err = up.FinishUpload(ctx)
			Expect(err).To(HaveOccurred())
			_, isProcessing := err.(interface{ IsResourceProcessing() bool })
			_ = isProcessing
			Expect(err.Error()).To(ContainSubstring("resource is processing"))
		})

		It("cleans up the rejected session's bin and info files", func() {
			uploadIds, err := fs.InitiateUpload(ctx, ref, 20, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			secondUploadID := uploadIds["simple"]

			ds, ok := fs.(tusd.DataStore)
			Expect(ok).To(BeTrue())
			up, err := ds.GetUpload(ctx, secondUploadID)
			Expect(err).ToNot(HaveOccurred())
			_, err = up.WriteChunk(ctx, 0, bytes.NewReader(secondContent))
			Expect(err).ToNot(HaveOccurred())
			_ = up.FinishUpload(ctx) // expect failure; error checked in other test

			// Bin file must be removed.
			_, statErr := os.Stat(filepath.Join(o.Root, "uploads", secondUploadID))
			Expect(statErr).ToNot(BeNil(), "bin file should be cleaned up after rejection")

			// Info file must be removed.
			_, statErr = os.Stat(filepath.Join(o.Root, "uploads", secondUploadID+".info"))
			Expect(statErr).ToNot(BeNil(), "info file should be cleaned up after rejection")
		})
	})

	When("uploads are processed sequentially (second after first completes)", func() {
		var secondUploadID string

		JustBeforeEach(func() {
			// Complete the first upload's postprocessing before starting the second.
			succeedPostprocessing(uploadID)

			// Second upload.
			uploadIds, err := fs.InitiateUpload(ctx, ref, 20, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			secondUploadID = uploadIds["simple"]
			tusUpload(secondUploadID, secondContent)

			// wait for bytes received event
			_, ok := (<-pub).(events.BytesReceived)
			Expect(ok).To(BeTrue())
		})

		It("succeeds and creates a revision", func() {
			succeedPostprocessing(secondUploadID)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(secondContent)))
			Expect(parentSize()).To(Equal(len(secondContent)))
			Expect(revisionCount()).To(Equal(1))
		})

		It("reverts to previous content when second upload is deleted", func() {
			failPostprocessing(secondUploadID, events.PPOutcomeDelete)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(firstContent)))
			Expect(parentSize()).To(Equal(len(firstContent)))
			Expect(revisionCount()).To(Equal(0))
		})

		It("reverts to previous content when second upload is aborted and keeps bin", func() {
			failPostprocessing(secondUploadID, events.PPOutcomeAbort)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(firstContent)))
			Expect(parentSize()).To(Equal(len(firstContent)))

			// bytes kept
			_, statErr := os.Stat(filepath.Join(o.Root, "uploads", secondUploadID))
			Expect(statErr).To(BeNil())
		})
	})
})
