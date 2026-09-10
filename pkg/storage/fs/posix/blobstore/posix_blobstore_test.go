package blobstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	posixblobstore "github.com/owncloud/reva/v2/pkg/storage/fs/posix/blobstore"
	"github.com/owncloud/reva/v2/pkg/storage/fs/posix/lookup"
	"github.com/owncloud/reva/v2/pkg/storage/fs/posix/options"
	"github.com/owncloud/reva/v2/pkg/storage/fs/posix/timemanager"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/usermapper"
	"github.com/owncloud/reva/v2/tests/helpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Posix blobstore", func() {
	const (
		spaceID = "1284d238-aa92-42ce-bdc4-0b0000009157"
		nodeID  = "4c510ada-c86b-4815-8820-42cdf82c3d51"
	)

	var (
		ctx context.Context

		tmpRoot  string
		nodePath string
		data     []byte

		// a file outside of the storage tree that the service account can read but
		// the user must never be able to reach
		secretPath string
		secretData []byte

		lu *lookup.Lookup
		bs *posixblobstore.Blobstore
		n  *node.Node
	)

	// swapInSymlink atomically replaces the already assimilated node with a symlink
	// pointing at secretPath, the way an attacker with out-of-band write access to the
	// tree would. Renaming over the target is what makes this reach the blobstore: the
	// tree only ever sees a MOVED_TO and the cached id -> path mapping stays valid.
	swapInSymlink := func() {
		staging := filepath.Join(filepath.Dir(nodePath), "staging")
		Expect(os.Symlink(secretPath, staging)).To(Succeed())
		Expect(os.Rename(staging, nodePath)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		data = []byte("the blob the user uploaded")
		secretData = []byte("the secret the user must not read")

		var err error
		tmpRoot, err = helpers.TempDir("reva-unit-tests-*-root")
		Expect(err).ToNot(HaveOccurred())

		secretPath = filepath.Join(GinkgoT().TempDir(), "secret.txt")
		Expect(os.WriteFile(secretPath, secretData, 0600)).To(Succeed())

		o, err := options.New(map[string]interface{}{"root": tmpRoot})
		Expect(err).ToNot(HaveOccurred())
		lu = lookup.New(metadata.NewMessagePackBackend(o.Root, o.FileMetadataCache), &usermapper.NullMapper{}, o, &timemanager.Manager{})

		bs, err = posixblobstore.New(tmpRoot)
		Expect(err).ToNot(HaveOccurred())

		// in the posix driver the node is the file itself, so lay it down in the tree
		// and cache the id -> path mapping the way assimilation would
		nodePath = filepath.Join(tmpRoot, "users", "username", "blob.txt")
		Expect(os.MkdirAll(filepath.Dir(nodePath), 0700)).To(Succeed())
		Expect(os.WriteFile(nodePath, data, 0600)).To(Succeed())
		Expect(lu.CacheID(ctx, spaceID, nodeID, nodePath)).To(Succeed())

		n = node.New(spaceID, nodeID, "", "blob.txt", int64(len(data)), "", provider.ResourceType_RESOURCE_TYPE_FILE, &userpb.UserId{OpaqueId: "someone"}, lu)
		Expect(n.InternalPath()).To(Equal(nodePath))
	})

	AfterEach(func() {
		if tmpRoot != "" {
			os.RemoveAll(tmpRoot)
		}
	})

	Describe("Download", func() {
		It("reads a regular file", func() {
			reader, err := bs.Download(n)
			Expect(err).ToNot(HaveOccurred())
			defer reader.Close()

			Expect(io.ReadAll(reader)).To(Equal(data))
		})

		It("does not read through a symlink", func() {
			swapInSymlink()

			reader, err := bs.Download(n)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, syscall.ELOOP)).To(BeTrue())
			Expect(reader).To(BeNil())
		})
	})

	Describe("Upload", func() {
		var source string

		BeforeEach(func() {
			source = filepath.Join(GinkgoT().TempDir(), "source")
			Expect(os.WriteFile(source, []byte("new content"), 0600)).To(Succeed())
		})

		It("writes a regular file", func() {
			Expect(bs.Upload(n, source)).To(Succeed())
			Expect(os.ReadFile(nodePath)).To(Equal([]byte("new content")))
		})

		It("does not write through a symlink", func() {
			swapInSymlink()

			err := bs.Upload(n, source)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, syscall.ELOOP)).To(BeTrue())
			Expect(os.ReadFile(secretPath)).To(Equal(secretData))
		})
	})

	Describe("UploadFromReader", func() {
		It("writes a regular file", func() {
			content := []byte("new content")
			Expect(bs.UploadFromReader(n, bytes.NewReader(content), int64(len(content)))).To(Succeed())
			Expect(os.ReadFile(nodePath)).To(Equal(content))
		})

		It("does not write through a symlink", func() {
			swapInSymlink()

			content := []byte("new content")
			err := bs.UploadFromReader(n, bytes.NewReader(content), int64(len(content)))
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, syscall.ELOOP)).To(BeTrue())
			Expect(os.ReadFile(secretPath)).To(Equal(secretData))
		})
	})
})
