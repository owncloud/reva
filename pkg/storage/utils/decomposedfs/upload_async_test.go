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
	pkgupload "github.com/owncloud/reva/v2/pkg/upload"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/owncloud/reva/v2/tests/helpers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
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
		coord                pkgupload.Coordinator
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
			up, err := coord.GetUpload(ctx, id)
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
				Delete:             true,
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
		d := dfs.(*Decomposedfs)
		fileStore := pkgupload.NewFileStore(o.Root, pkgupload.TokenOptions{}, &zerolog.Logger{})
		var coordErr error
		coord, coordErr = pkgupload.NewCoordinator(d, fileStore, aspects.EventStream, true, "", "dcfs", 1, &zerolog.Logger{}, "")
		Expect(coordErr).ToNot(HaveOccurred())
		Expect(coord.Start(aspects.EventStream)).To(Succeed())
		fs = d

		resp, err := fs.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{Owner: user, Type: "personal"})
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.Status.Code).To(Equal(v1beta11.Code_CODE_OK))
		resID, err := storagespace.ParseID(resp.StorageSpace.Id.OpaqueId)
		Expect(err).ToNot(HaveOccurred())
		ref.ResourceId = &resID

		bs.On("UploadFromReader", mock.AnythingOfType("*node.Node"), mock.Anything, mock.AnythingOfType("int64")).
			Return(nil)

		// start upload of a file
		uploadIds, err := coord.InitiateUpload(ctx, ref, 10, map[string]string{})
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
			uploadIds, err := coord.InitiateUpload(ctx, ref, 10, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(uploadIds)).To(Equal(2))
			Expect(uploadIds["simple"]).ToNot(BeEmpty())
			Expect(uploadIds["tus"]).ToNot(BeEmpty())

			uploadID = uploadIds["simple"]
			tusUpload(uploadID, firstContent)

			// wait for bytes received event
			_, ok := (<-pub).(events.BytesReceived)
			Expect(ok).To(BeTrue())

			// version already created — PrepareUpload runs in finishUpload before postprocessing.
			revs, err = fs.ListRevisions(ctx, ref)
			Expect(err).To(BeNil())
			Expect(len(revs)).To(Equal(1))

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

		// TODO: re-enable once RollbackUpload is implemented — PrepareUpload now runs before
		// postprocessing, so PPOutcomeDelete must call RollbackUpload to remove the version
		// and restore old xattrs.
		PIt("removes new version and restores old one when instructed", func() {
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
	When("a second upload is started while the first is still in postprocessing", func() {
		var secondUploadID string

		JustBeforeEach(func() {
			uploadIds, err := coord.InitiateUpload(ctx, ref, 20, map[string]string{})
			Expect(err).ToNot(HaveOccurred())
			secondUploadID = uploadIds["simple"]
			tusUpload(secondUploadID, secondContent)

			// wait for the second BytesReceived
			_, ok := (<-pub).(events.BytesReceived)
			Expect(ok).To(BeTrue())
		})

		It("both uploads succeed and the last committed content wins", func() {
			// Complete first, then second — second wins.
			succeedPostprocessing(uploadID)
			succeedPostprocessing(secondUploadID)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(secondContent)))
		})

		// TODO: re-enable once RollbackUpload is implemented — PPOutcomeDelete must undo
		// PrepareUpload's xattr writes to restore the previous content's metadata.
		PIt("second upload failing leaves the first upload's content", func() {
			succeedPostprocessing(uploadID)
			failPostprocessing(secondUploadID, events.PPOutcomeDelete)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(firstContent)))
		})
	})

	When("uploads are processed sequentially (second after first completes)", func() {
		var secondUploadID string

		JustBeforeEach(func() {
			// Complete the first upload's postprocessing before starting the second.
			succeedPostprocessing(uploadID)

			// Second upload.
			uploadIds, err := coord.InitiateUpload(ctx, ref, 20, map[string]string{})
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

		// TODO: re-enable once RollbackUpload is implemented.
		PIt("reverts to previous content when second upload is deleted", func() {
			failPostprocessing(secondUploadID, events.PPOutcomeDelete)

			_, status, size := fileStatus()
			Expect(status).To(Equal(""))
			Expect(size).To(Equal(len(firstContent)))
			Expect(parentSize()).To(Equal(len(firstContent)))
			Expect(revisionCount()).To(Equal(0))
		})

		// TODO: re-enable once RollbackUpload is implemented.
		PIt("reverts to previous content when second upload is aborted and keeps bin", func() {
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
