package decomposedfs_test

import (
	"context"
	"fmt"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata/prefixes"
	helpers "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/testhelpers"
	"github.com/owncloud/reva/v2/pkg/utils"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Revision author", func() {
	for _, backend := range []string{"xattrs", "messagepack"} {
		backend := backend
		Describe("with the "+backend+" metadata backend", Ordered, func() { revisionAuthorSpecs(backend) })
	}
})

func revisionAuthorSpecs(backend string) {
	var (
		env        *helpers.TestEnv
		ref        *provider.Reference
		alice, bob *userpb.User
		carol      *userpb.User
		fileCount  int
	)

	as := func(u *userpb.User) context.Context { return ctxpkg.ContextSetUser(env.Ctx, u) }

	upload := func(u *userpb.User, session string, existed bool, mtime time.Time) {
		_, err := env.Fs.PrepareUpload(as(u), ref, session, storage.UploadInfo{NodeExisted: existed, Size: 10, MTime: mtime})
		Expect(err).ToNot(HaveOccurred())
		// what the commit does, so the next upload and a restore are allowed
		Expect(env.Fs.MarkProcessing(as(u), ref, false, session)).To(Succeed())
	}

	revisionAuthors := func() map[string]string {
		revs, err := env.Fs.ListRevisions(env.Ctx, ref)
		Expect(err).ToNot(HaveOccurred())
		authors := map[string]string{}
		for _, r := range revs {
			id := &userpb.UserId{}
			if utils.ExistsInOpaque(r.Opaque, storage.RevisionAuthorOpaqueKey) {
				Expect(utils.ReadJSONFromOpaque(r.Opaque, storage.RevisionAuthorOpaqueKey, id)).To(Succeed())
			}
			authors[r.Key] = id.GetOpaqueId()
		}
		return authors
	}

	nodeAuthor := func() string {
		n, err := env.Lookup.NodeFromResource(env.Ctx, ref)
		Expect(err).ToNot(HaveOccurred())
		v, _ := n.XattrString(env.Ctx, prefixes.AuthorIDAttr)
		return v
	}

	// one env per backend: the msgpack backend cannot create a second one in the same process
	BeforeAll(func() {
		var err error
		env, err = helpers.NewTestEnv(map[string]interface{}{"metadata_backend": backend})
		Expect(err).ToNot(HaveOccurred())
		env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).
			Return(&provider.ResourcePermissions{
				InitiateFileUpload: true, Stat: true, ListFileVersions: true, RestoreFileVersion: true,
			}, nil)
	})

	AfterAll(func() {
		if env != nil {
			env.Cleanup()
		}
	})

	// every scenario gets its own file
	BeforeEach(func() {
		fileCount++
		ref = &provider.Reference{ResourceId: env.SpaceRootRes, Path: fmt.Sprintf("/dir1/author-%d.txt", fileCount)}
		_, err := env.Fs.TouchFile(env.Ctx, ref, false, "")
		Expect(err).ToNot(HaveOccurred())

		mk := func(id string) *userpb.User {
			return &userpb.User{Id: &userpb.UserId{OpaqueId: id, Idp: "idp", Type: userpb.UserType_USER_TYPE_PRIMARY}}
		}
		alice, bob, carol = mk("alice"), mk("bob"), mk("carol")
	})

	It("keeps the author of every archived version, and of the current content", func() {
		t0 := time.Now().Add(-3 * time.Hour)
		upload(alice, "s1", false, t0)
		upload(bob, "s2", true, t0.Add(time.Hour))
		upload(carol, "s3", true, t0.Add(2*time.Hour))

		Expect(nodeAuthor()).To(Equal("carol"))
		Expect(revisionAuthors()).To(HaveLen(2))
		Expect(revisionAuthors()).To(ConsistOf("alice", "bob"))
	})

	It("moves the author with the content on restore", func() {
		t0 := time.Now().Add(-3 * time.Hour)
		upload(alice, "s1", false, t0)
		upload(bob, "s2", true, t0.Add(time.Hour))

		var aliceRev string
		for k, a := range revisionAuthors() {
			if a == "alice" {
				aliceRev = k
			}
		}
		Expect(aliceRev).ToNot(BeEmpty())

		_, err := env.Fs.RestoreRevision(as(carol), ref, aliceRev)
		Expect(err).ToNot(HaveOccurred())

		Expect(nodeAuthor()).To(Equal("alice")) // content author, not the restoring user
		Expect(revisionAuthors()).To(ConsistOf("bob"))
	})

	It("does not attribute restored content to the author it replaced, when the revision has none", func() {
		t0 := time.Now().Add(-3 * time.Hour)
		upload(alice, "s1", false, t0)
		upload(bob, "s2", true, t0.Add(time.Hour))

		// a revision written before authors were recorded
		revs, err := env.Fs.ListRevisions(env.Ctx, ref)
		Expect(err).ToNot(HaveOccurred())
		revPath := env.Lookup.InternalPath(env.SpaceRootRes.SpaceId, revs[0].Key)
		for _, a := range []string{prefixes.AuthorIDAttr, prefixes.AuthorIDPAttr, prefixes.AuthorTypeAttr} {
			Expect(env.Lookup.MetadataBackend().Remove(env.Ctx, revPath, a, true)).To(Succeed())
		}
		Expect(nodeAuthor()).To(Equal("bob"))

		_, err = env.Fs.RestoreRevision(as(carol), ref, revs[0].Key)
		Expect(err).ToNot(HaveOccurred())
		Expect(nodeAuthor()).To(BeEmpty())
	})
}
