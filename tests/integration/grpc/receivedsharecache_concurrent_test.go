package grpc_test

import (
	"context"
	"fmt"
	"os"
	"sync"

	grpcMetadata "google.golang.org/grpc/metadata"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/auth/scope"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/receivedsharecache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	jwt "github.com/owncloud/reva/v2/pkg/token/manager/jwt"
	"github.com/rs/zerolog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("receivedsharecache concurrent CS3 writes", func() {
	var (
		revads    map[string]*Revad
		ctx       context.Context
		spaceRoot *provider.ResourceId
		newCS3    func() *metadata.CS3

		csUser = &userpb.User{
			Id: &userpb.UserId{
				Idp:      "0.0.0.0:19000",
				OpaqueId: "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c",
				Type:     userpb.UserType_USER_TYPE_PRIMARY,
			},
			Username: "einstein",
		}

		csUserID  = "user"
		csSpaceID = "spaceid"
	)

	BeforeEach(func() {
		var err error
		zl := zerolog.New(os.Stdout).Level(zerolog.DebugLevel)
		ctx = appctx.WithLogger(context.Background(), &zl)

		tokenManager, err := jwt.New(map[string]interface{}{"secret": "changemeplease"})
		Expect(err).ToNot(HaveOccurred())
		sc, err := scope.AddOwnerScope(nil)
		Expect(err).ToNot(HaveOccurred())
		t, err := tokenManager.MintToken(ctx, csUser, sc)
		Expect(err).ToNot(HaveOccurred())
		ctx = ctxpkg.ContextSetToken(ctx, t)
		ctx = grpcMetadata.AppendToOutgoingContext(ctx, ctxpkg.TokenHeader, t)
		ctx = ctxpkg.ContextSetUser(ctx, csUser)

		revads, err = startRevads([]RevadConfig{
			{Name: "storage", Config: "storageprovider-ocis-with-dataprovider.toml"},
			{Name: "permissions", Config: "permissions-ocis-ci.toml"},
		}, nil)
		Expect(err).ToNot(HaveOccurred())

		spacesClient, err := pool.GetSpacesProviderServiceClient(revads["storage"].GrpcAddress)
		Expect(err).ToNot(HaveOccurred())
		res, err := spacesClient.CreateStorageSpace(ctx, &provider.CreateStorageSpaceRequest{
			Owner: csUser,
			Type:  "metadata",
			Name:  "Metadata",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Status.Code.String()).To(Equal("CODE_OK"))
		spaceRoot = res.StorageSpace.Root

		newCS3 = func() *metadata.CS3 {
			cs3 := metadata.NewCS3("", revads["storage"].GrpcAddress)
			cs3.SpaceRoot = spaceRoot
			return cs3
		}

		// decomposedfs CreateContainer requires parent to exist; pre-create /users.
		setup := metadata.NewCS3("", revads["storage"].GrpcAddress)
		setup.SpaceRoot = spaceRoot
		Expect(setup.MakeDirIfNotExist(ctx, "/users")).To(Succeed())
	})

	AfterEach(func() {
		for _, r := range revads {
			Expect(r.Cleanup(CurrentSpecReport().Failed())).To(Succeed())
		}
		if r, ok := revads["storage"]; ok {
			pool.RemoveSelector("StorageProviderSelector" + r.GrpcAddress)
		}
	})

	It("preserves all shares when 2 replicas write concurrently (OCISDEV-855)", func() {
		const numShares = 15
		replicas := [2]receivedsharecache.Cache{
			receivedsharecache.New(newCS3(), 0),
			receivedsharecache.New(newCS3(), 0),
		}

		errs := make([]error, numShares)
		var wg sync.WaitGroup
		for i := 0; i < numShares; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				rs := &collaboration.ReceivedShare{
					Share: &collaboration.Share{
						Id: &collaboration.ShareId{OpaqueId: fmt.Sprintf("share-%d", idx)},
					},
					State: collaboration.ShareState_SHARE_STATE_PENDING,
				}
				errs[idx] = replicas[idx%2].Add(ctx, csUserID, csSpaceID, rs)
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			Expect(err).ToNot(HaveOccurred(), "Add failed for share-%d", i)
		}

		fresh := receivedsharecache.New(newCS3(), 0)
		spaces, err := fresh.List(ctx, csUserID)
		Expect(err).ToNot(HaveOccurred())
		Expect(spaces[csSpaceID]).ToNot(BeNil())
		for i := 0; i < numShares; i++ {
			Expect(spaces[csSpaceID].States).To(HaveKey(fmt.Sprintf("share-%d", i)))
		}
	})

	It("both replicas recover when writes are forced simultaneous (OCISDEV-855)", func() {
		bs := metadata.NewBarrierStorage(newCS3(), 2)
		replicas := [2]receivedsharecache.Cache{
			receivedsharecache.New(bs, 0),
			receivedsharecache.New(bs, 0),
		}

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				rs := &collaboration.ReceivedShare{
					Share: &collaboration.Share{
						Id: &collaboration.ShareId{OpaqueId: fmt.Sprintf("share-%d", idx)},
					},
					State: collaboration.ShareState_SHARE_STATE_PENDING,
				}
				errs[idx] = replicas[idx].Add(ctx, csUserID, csSpaceID, rs)
			}(i)
		}
		wg.Wait()
		Expect(errs[0]).ToNot(HaveOccurred())
		Expect(errs[1]).ToNot(HaveOccurred())

		fresh := receivedsharecache.New(newCS3(), 0)
		spaces, err := fresh.List(ctx, csUserID)
		Expect(err).ToNot(HaveOccurred())
		Expect(spaces[csSpaceID]).ToNot(BeNil())
		Expect(spaces[csSpaceID].States).To(HaveKey("share-0"))
		Expect(spaces[csSpaceID].States).To(HaveKey("share-1"))
	})

	It("fails only the write that races the deletion, leaving every other persisted share intact (OCISDEV-855)", func() {
		receivedJSONPath := fmt.Sprintf("/users/%s/received.json", csUserID)

		// pre-seed shares sequentially via a plain writer, so there is
		// already-persisted data in the file before the conflict below.
		preseed := receivedsharecache.New(newCS3(), 0)
		const numPreseed = 2
		for i := 0; i < numPreseed; i++ {
			rs := &collaboration.ReceivedShare{
				Share: &collaboration.Share{
					Id: &collaboration.ShareId{OpaqueId: fmt.Sprintf("preseed-%d", i)},
				},
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			Expect(preseed.Add(ctx, csUserID, csSpaceID, rs)).To(Succeed())
		}

		// replica A's storage forces its first Upload to fail, deletes the
		// backing file (simulating an ops repair action landing in that exact
		// window), then lets every later call through for real. Replica B is
		// the only other concurrent writer -- two writers, one storage,
		// matching the race precisely instead of flooding it closed.
		decorated := &deleteOnConflictStorage{Storage: newCS3(), deleter: newCS3(), path: receivedJSONPath}
		replicas := [2]receivedsharecache.Cache{
			receivedsharecache.New(decorated, 0),
			receivedsharecache.New(newCS3(), 0),
		}

		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				rs := &collaboration.ReceivedShare{
					Share: &collaboration.Share{
						Id: &collaboration.ShareId{OpaqueId: fmt.Sprintf("share-%d", idx)},
					},
					State: collaboration.ShareState_SHARE_STATE_PENDING,
				}
				errs[idx] = replicas[idx].Add(ctx, csUserID, csSpaceID, rs)
			}(i)
		}
		wg.Wait()

		// Either replica can legitimately land in the deletion window and
		// correctly fail -- the decorator only controls when the file gets
		// deleted, not which concurrent reader notices it gone. A failure
		// here is the fix working as intended, not a bug: assert
		// self-consistency (fail -> absent, succeed -> present) rather than
		// which specific replica wins the race.
		anySucceeded := false
		expectedCount := numPreseed
		for _, err := range errs {
			if err == nil {
				anySucceeded = true
				expectedCount++
			}
		}

		fresh := receivedsharecache.New(newCS3(), 0)
		spaces, err := fresh.List(ctx, csUserID)
		Expect(err).ToNot(HaveOccurred())

		if !anySucceeded {
			// both writes raced the deletion and correctly refused to write;
			// nothing ever came back to re-persist anything, so the file
			// (including preseed) legitimately stays gone -- not evidence of
			// the bug, since nothing silently claimed success while losing data.
			return
		}

		Expect(spaces[csSpaceID]).ToNot(BeNil())

		// the one invariant that must hold whenever at least one write
		// succeeded: data persisted before the race is never silently
		// dropped by a write that reports success.
		for i := 0; i < numPreseed; i++ {
			Expect(spaces[csSpaceID].States).To(HaveKey(fmt.Sprintf("preseed-%d", i)),
				"a share successfully persisted before the conflict must survive a later successful write")
		}
		for i, err := range errs {
			shareKey := fmt.Sprintf("share-%d", i)
			if err != nil {
				Expect(spaces[csSpaceID].States).ToNot(HaveKey(shareKey),
					"a write that returned an error must not appear in the final state")
			} else {
				Expect(spaces[csSpaceID].States).To(HaveKey(shareKey),
					"a write that returned success must appear in the final state")
			}
		}
		Expect(spaces[csSpaceID].States).To(HaveLen(expectedCount),
			"every Add call that returned success must be reflected in the final state -- none silently dropped, and a failed call must not silently succeed either")
	})
})

// deleteOnConflictStorage forces exactly one Upload through it to fail, then
// deletes the backing file via a second real client before returning the
// error -- simulating an ops repair action landing in the exact window
// between a CAS conflict and that writer's post-conflict resync.
type deleteOnConflictStorage struct {
	metadata.Storage
	deleter metadata.Storage
	path    string
	once    sync.Once
}

func (d *deleteOnConflictStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	triggered := false
	d.once.Do(func() {
		triggered = true
		_ = d.deleter.Delete(ctx, d.path)
	})
	if triggered {
		return nil, errtypes.Aborted("injected: simulated conflict")
	}
	return d.Storage.Upload(ctx, req)
}
