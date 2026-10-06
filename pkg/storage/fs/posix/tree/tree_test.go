package tree_test

import (
	"context"
	"crypto/rand"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/google/uuid"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	helpers "github.com/owncloud/reva/v2/pkg/storage/fs/posix/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	"github.com/shirou/gopsutil/process"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func generateRandomString(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	charsetLength := len(charset)

	randomBytes := make([]byte, length)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	for i := 0; i < length; i++ {
		randomBytes[i] = charset[int(randomBytes[i])%charsetLength]
	}

	return string(randomBytes), nil
}

var (
	env *helpers.TestEnv

	root string
)

var _ = SynchronizedBeforeSuite(func() {
	if runtime.GOOS != "linux" {
		Skip("posix/tree tests require inotifywait (Linux only)")
	}

	var err error
	env, err = helpers.NewTestEnv(nil)
	Expect(err).ToNot(HaveOccurred())

	Eventually(func() bool {
		// Get all running processes
		processes, err := process.Processes()
		if err != nil {
			panic("could not get processes: " + err.Error())
		}

		// Search for the process named "inotifywait"
		for _, p := range processes {
			name, err := p.Name()
			if err != nil {
				log.Println(err)
				continue
			}

			if strings.Contains(name, "inotifywait") {
				// Give it some time to setup the watches
				time.Sleep(2 * time.Second)
				return true
			}
		}
		return false
	}).Should(BeTrue())
}, func() {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if env != nil {
		env.Cleanup()
	}
})

var _ = Describe("Tree", func() {
	var (
		subtree string
	)

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(15 * time.Second)

		var err error
		subtree, err = generateRandomString(10)
		Expect(err).ToNot(HaveOccurred())
		subtree = "/" + subtree
		root = env.Root + "/users/" + env.Owner.Username + subtree
		Expect(os.Mkdir(root, 0700)).To(Succeed())

		Eventually(func(g Gomega) {
			n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
				ResourceId: env.SpaceRootRes,
				Path:       subtree,
			})
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(n.Exists).To(BeTrue())
		}).Should(Succeed())
	})

	Describe("assimilation", func() {
		Describe("of files", func() {
			It("handles new files", func() {
				_, err := os.Create(root + "/assimilated.txt")
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/assimilated.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).ProbeEvery(200 * time.Millisecond).Should(Succeed())
			})

			It("handles changed files", func() {
				// Create empty file
				_, err := os.Create(root + "/changed.txt")
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/changed.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).ProbeEvery(200 * time.Millisecond).Should(Succeed())

				// Change file content
				Expect(os.WriteFile(root+"/changed.txt", []byte("hello world"), 0600)).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/changed.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.Blobsize).To(Equal(int64(11)))
				}).Should(Succeed())
			})

			It("handles deleted files", func() {
				_, err := os.Create(root + "/deleted.txt")
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/deleted.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
				}).Should(Succeed())

				Expect(os.Remove(root + "/deleted.txt")).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/deleted.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeFalse())
				}).Should(Succeed())
			})

			It("handles moved files", func() {
				// Create empty file
				_, err := os.Create(root + "/original.txt")
				Expect(err).ToNot(HaveOccurred())

				fileID := ""
				// Wait for the file to be indexed
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/original.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
					fileID = n.ID
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).Should(Succeed())

				// Move file
				Expect(os.Rename(root+"/original.txt", root+"/moved.txt")).To(Succeed())

				// Wait for the file to be indexed
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/original.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeFalse())
				}).Should(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/moved.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).To(Equal(fileID))
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).Should(Succeed())
			})

			It("handles id clashes", func() {
				// Create empty file
				_, err := os.Create(root + "/original.txt")
				Expect(err).ToNot(HaveOccurred())

				fileID := ""
				// Wait for the file to be indexed
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/original.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
					fileID = n.ID
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).Should(Succeed())

				// cp file
				cmd := exec.Command("cp", "-a", root+"/original.txt", root+"/moved.txt")
				err = cmd.Run()
				Expect(err).ToNot(HaveOccurred())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/moved.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.ID).ToNot(Equal(fileID))
					g.Expect(n.Blobsize).To(Equal(int64(0)))
				}).Should(Succeed())
			})
		})

		Describe("of directories", func() {
			It("handles new directories", func() {
				Expect(os.Mkdir(root+"/assimilated", 0700)).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/assimilated",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
					g.Expect(n.ID).ToNot(BeEmpty())
				}).Should(Succeed())
			})

			It("handles files in directories", func() {
				Expect(os.Mkdir(root+"/assimilated", 0700)).To(Succeed())
				time.Sleep(100 * time.Millisecond) // Give it some time to settle down
				Expect(os.WriteFile(root+"/assimilated/file.txt", []byte("hello world"), 0600)).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/assimilated",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
				}).Should(Succeed())
			})

			It("handles deleted directories", func() {
				Expect(os.Mkdir(root+"/deleted", 0700)).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/deleted",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
					g.Expect(n.ID).ToNot(BeEmpty())
				}).Should(Succeed())

				Expect(os.Remove(root + "/deleted")).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/deleted",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeFalse())
				}).Should(Succeed())
			})

			It("handles moved directories", func() {
				Expect(os.Mkdir(root+"/original", 0700)).To(Succeed())
				time.Sleep(100 * time.Millisecond) // Give it some time to settle down
				Expect(os.WriteFile(root+"/original/file.txt", []byte("hello world"), 0600)).To(Succeed())

				dirId := ""
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/original",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
					g.Expect(n.ID).ToNot(BeEmpty())
					g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
					dirId = n.ID
				}).Should(Succeed())

				Expect(os.Rename(root+"/original", root+"/moved")).To(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/original",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeFalse())
				}).Should(Succeed())

				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/moved",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n).ToNot(BeNil())
					g.Expect(n.Exists).To(BeTrue())
					g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
					g.Expect(n.ID).To(Equal(dirId))
					g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
				}).Should(Succeed())
			})
		})

		Describe("of non-regular files", func() {
			var outside string

			BeforeEach(func() {
				outside = GinkgoT().TempDir() + "/secret.txt"
				Expect(os.WriteFile(outside, []byte("secret"), 0600)).To(Succeed())
			})

			// cachedID reports whether the item has been assimilated, i.e. whether it made
			// it into the id cache
			cachedID := func(name string) error {
				_, _, err := env.Lookup.IDsForPath(env.Ctx, root+"/"+name)
				return err
			}

			// waitForScanner blocks until a plain file created after the item under test has
			// been assimilated. By then the scanner has worked through the earlier event.
			waitForScanner := func() {
				_, err := os.Create(root + "/sentinel.txt")
				Expect(err).ToNot(HaveOccurred())

				Eventually(func() error {
					return cachedID("sentinel.txt")
				}).ProbeEvery(200 * time.Millisecond).Should(Succeed())
			}

			It("skips symlinks", func() {
				Expect(os.Symlink(outside, root+"/link.txt")).To(Succeed())
				waitForScanner()

				Consistently(func() error {
					return cachedID("link.txt")
				}, 2*time.Second, 200*time.Millisecond).ShouldNot(Succeed())
			})

			It("skips fifos without stalling the scanner", func() {
				Expect(syscall.Mkfifo(root+"/fifo", 0600)).To(Succeed())
				waitForScanner()

				Consistently(func() error {
					return cachedID("fifo")
				}, 2*time.Second, 200*time.Millisecond).ShouldNot(Succeed())
			})

			It("walks past them when warming up the id cache", func() {
				Expect(os.Symlink(outside, root+"/link.txt")).To(Succeed())
				Expect(syscall.Mkfifo(root+"/fifo", 0600)).To(Succeed())

				Expect(env.Tree.WarmupIDCache(env.Root, true, false)).To(Succeed())

				Expect(cachedID("link.txt")).ToNot(Succeed())
				Expect(cachedID("fifo")).ToNot(Succeed())
			})
		})
	})

	Describe("propagation", func() {
		PIt("propagates new files in a directory", func() {
			Expect(os.Mkdir(root+"/assimilated", 0700)).To(Succeed())
			time.Sleep(100 * time.Millisecond) // Give it some time to settle down
			Expect(os.WriteFile(root+"/assimilated/file.txt", []byte("hello world"), 0600)).To(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree + "/assimilated",
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
			}).Should(Succeed())

			Expect(os.WriteFile(root+"/assimilated/file2.txt", []byte("hello world"), 0600)).To(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree + "/assimilated",
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(22)))
			}).Should(Succeed())
		})

		It("propagates new files in a directory to the parent", func() {
			Expect(env.Tree.WarmupIDCache(env.Root, false, true)).To(Succeed())
			Expect(os.Mkdir(root+"/assimilated", 0700)).To(Succeed())
			time.Sleep(100 * time.Millisecond) // Give it some time to settle down
			Expect(os.WriteFile(root+"/assimilated/file.txt", []byte("hello world"), 0600)).To(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree + "/assimilated",
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
			}).Should(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree,
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(11)))
			}).Should(Succeed())

			Expect(os.WriteFile(root+"/assimilated/file2.txt", []byte("hello world"), 0600)).To(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree + "/assimilated",
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(22)))
			}).Should(Succeed())

			Eventually(func(g Gomega) {
				n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
					ResourceId: env.SpaceRootRes,

					Path: subtree,
				})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(n).ToNot(BeNil())
				g.Expect(n.Type(env.Ctx)).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				g.Expect(n.ID).ToNot(BeEmpty())
				g.Expect(n.GetTreeSize(env.Ctx)).To(Equal(uint64(22)))
			}).Should(Succeed())
		})

	})

	Describe("InitNewNode", func() {
		var (
			nodePath string
			newNode  *node.Node
		)

		BeforeEach(func() {
			parent, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
				ResourceId: env.SpaceRootRes,
				Path:       subtree,
			})
			Expect(err).ToNot(HaveOccurred())
			newNode = node.New(parent.SpaceID, uuid.New().String(), parent.ID, "new.txt", 0, "", provider.ResourceType_RESOURCE_TYPE_FILE, nil, env.Lookup)
			newNode.SpaceRoot = parent.SpaceRoot
			nodePath = filepath.Join(root, "new.txt")
		})

		cachedPath := func(nodeID string) (string, bool) {
			return env.Lookup.IDCache.Get(env.Ctx, newNode.SpaceID, nodeID)
		}

		It("creates the file and caches its id", func() {
			unlock, err := env.Tree.InitNewNode(env.Ctx, newNode, 0)
			Expect(err).ToNot(HaveOccurred())
			// Checked under the lock: once released, the assimilation may claim the file,
			// as nothing writes its id attribute here.
			defer func() { Expect(unlock()).To(Succeed()) }()

			Expect(nodePath).To(BeAnExistingFile())
			p, ok := cachedPath(newNode.ID)
			Expect(ok).To(BeTrue())
			Expect(p).To(Equal(nodePath))
		})

		Context("when the name is taken", func() {
			var existingID string

			BeforeEach(func() {
				Expect(os.WriteFile(nodePath, []byte("existing"), 0600)).To(Succeed())
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/new.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeTrue())
					existingID = n.ID
				}).Should(Succeed())
			})

			It("leaves the file and its cache entry alone, and releases the lock", func() {
				unlock, err := env.Tree.InitNewNode(env.Ctx, newNode, 0)
				Expect(err).To(BeAssignableToTypeOf(errtypes.AlreadyExists("")))
				Expect(unlock).To(BeNil())

				Expect(os.ReadFile(nodePath)).To(Equal([]byte("existing")))
				_, id, ok := env.Lookup.IDCache.GetByPath(env.Ctx, nodePath)
				Expect(ok).To(BeTrue())
				Expect(id).To(Equal(existingID))
				_, ok = cachedPath(newNode.ID)
				Expect(ok).To(BeFalse())
				Expect(env.Lookup.MetadataBackend().LockfilePath(nodePath)).ToNot(BeAnExistingFile())
			})
		})

		Context("when the quota is exceeded", func() {
			var originalCheckQuota = node.CheckQuota

			BeforeEach(func() {
				node.CheckQuota = func(context.Context, *node.Node, bool, uint64, uint64) (bool, error) {
					return false, errtypes.InsufficientStorage("quota exceeded")
				}
			})
			AfterEach(func() {
				node.CheckQuota = originalCheckQuota
			})

			It("removes the file and its cache entry, and releases the lock", func() {
				unlock, err := env.Tree.InitNewNode(env.Ctx, newNode, 1)
				Expect(err).To(BeAssignableToTypeOf(errtypes.InsufficientStorage("")))
				Expect(unlock).To(BeNil())

				Expect(nodePath).ToNot(BeAnExistingFile())
				_, ok := cachedPath(newNode.ID)
				Expect(ok).To(BeFalse())
				_, _, ok = env.Lookup.IDCache.GetByPath(env.Ctx, nodePath)
				Expect(ok).To(BeFalse())
				Expect(env.Lookup.MetadataBackend().LockfilePath(nodePath)).ToNot(BeAnExistingFile())
			})
		})
	})

	// Nothing has created the file: PrepareUpload does, under the id minted at initiate.
	Describe("PrepareUpload of a new file", func() {
		var (
			nodePath    string
			placeholder string
			createRef   *provider.Reference
			info        storage.UploadInfo
		)

		BeforeEach(func() {
			parent, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
				ResourceId: env.SpaceRootRes,
				Path:       subtree,
			})
			Expect(err).ToNot(HaveOccurred())
			placeholder = uuid.New().String()
			createRef = &provider.Reference{ResourceId: &provider.ResourceId{SpaceId: parent.SpaceID, OpaqueId: placeholder}}
			info = storage.UploadInfo{NodeExisted: false, Size: 42, ParentID: parent.ID, Name: "new.txt"}
			nodePath = filepath.Join(root, "new.txt")

			env.Permissions.On("AssemblePermissions", mock.Anything, mock.Anything, mock.Anything).
				Return(&provider.ResourcePermissions{InitiateFileUpload: true, Stat: true}, nil).Once()
		})

		idAtPath := func() string {
			n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
				ResourceId: env.SpaceRootRes,
				Path:       subtree + "/new.txt",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(n.Exists).To(BeTrue())
			return n.ID
		}

		It("creates the file under the placeholder id, and the assimilation keeps it", func() {
			_, err := env.Fs.PrepareUpload(env.Ctx, createRef, "session-new", info)
			Expect(err).ToNot(HaveOccurred())

			Expect(nodePath).To(BeAnExistingFile())
			n, err := env.Lookup.NodeFromID(env.Ctx, createRef.ResourceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(n.Exists).To(BeTrue())
			Expect(n.Name).To(Equal("new.txt"))
			Expect(n.ParentID).To(Equal(info.ParentID))
			// posix reports no blob id (the file is the blob), so the batch shows in the mark.
			id, err := n.ProcessingID(env.Ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal("session-new"))

			// The watcher sees the create too: it must adopt our id, not mint its own. Our
			// batch would overwrite a minted id on disk, so the cache is where one shows.
			Consistently(func(g Gomega) {
				g.Expect(idAtPath()).To(Equal(placeholder))
				_, cachedID, ok := env.Lookup.IDCache.GetByPath(env.Ctx, nodePath)
				g.Expect(ok).To(BeTrue())
				g.Expect(cachedID).To(Equal(placeholder))
			}, 3*time.Second, 200*time.Millisecond).Should(Succeed())
		})

		Context("when the name is taken", func() {
			var existingID string

			BeforeEach(func() {
				Expect(os.WriteFile(nodePath, []byte("existing"), 0600)).To(Succeed())
				Eventually(func(g Gomega) {
					n, err := env.Lookup.NodeFromResource(env.Ctx, &provider.Reference{
						ResourceId: env.SpaceRootRes,
						Path:       subtree + "/new.txt",
					})
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(n.Exists).To(BeTrue())
					existingID = n.ID
				}).Should(Succeed())
			})

			It("returns AlreadyExists and leaves the file alone", func() {
				_, err := env.Fs.PrepareUpload(env.Ctx, createRef, "session-new", info)
				Expect(err).To(BeAssignableToTypeOf(errtypes.AlreadyExists("")))
				Expect(err.Error()).ToNot(ContainSubstring(env.Root), "the error exposes the storage path")

				Expect(os.ReadFile(nodePath)).To(Equal([]byte("existing")))
				Expect(idAtPath()).To(Equal(existingID))
				_, ok := env.Lookup.IDCache.Get(env.Ctx, createRef.ResourceId.SpaceId, placeholder)
				Expect(ok).To(BeFalse())
			})
		})
	})
})
