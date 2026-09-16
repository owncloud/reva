package kiteworks_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http/httptest"
	"os"
	"strings"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage"
	"github.com/owncloud/reva/v2/pkg/storage/fs/kiteworks"
)

type fixture struct {
	ctx         context.Context
	spaceID     string
	fileID      string
	fileContent string // empty on real box; exact expected content in mock mode
}

func skipIfRealBox() {
	if os.Getenv("KITEWORKS") != "" {
		Skip("mock-only test")
	}
}

func firstFileID(items []*provider.ResourceInfo) string {
	for _, item := range items {
		if item.Type == provider.ResourceType_RESOURCE_TYPE_FILE {
			return item.Id.OpaqueId
		}
	}
	return ""
}

func setupDriver() (storage.FS, *fixture, func()) {
	ep := os.Getenv("KITEWORKS")
	if ep == "" {
		srv := httptest.NewServer(mockKiteworksHandler())
		d, err := kiteworks.New(map[string]interface{}{"endpoint": srv.URL}, nil, nil)
		Expect(err).ToNot(HaveOccurred())
		return d, &fixture{
			ctx:         context.Background(),
			spaceID:     "space-1",
			fileID:      "file-1",
			fileContent: "hello kiteworks",
		}, srv.Close
	}

	d, err := kiteworks.New(map[string]interface{}{"endpoint": ep}, nil, nil)
	Expect(err).ToNot(HaveOccurred())

	ctx := ctxpkg.ContextSetToken(context.Background(), os.Getenv("KITEWORKS_TOKEN"))

	spaces, err := d.ListStorageSpaces(ctx, nil, false)
	Expect(err).ToNot(HaveOccurred(), "real-box ListStorageSpaces failed, check token/endpoint")
	Expect(spaces).ToNot(BeEmpty(), "real box has no top-level folders")

	spaceID := spaces[0].Root.OpaqueId

	children, err := d.ListFolder(ctx, &provider.Reference{
		ResourceId: &provider.ResourceId{SpaceId: spaceID, OpaqueId: spaceID},
	}, nil, nil)
	Expect(err).ToNot(HaveOccurred())

	return d, &fixture{ctx: ctx, spaceID: spaceID, fileID: firstFileID(children)}, func() {}
}

var _ = Describe("kiteworks driver", func() {
	var (
		d    storage.FS
		fix  *fixture
		stop func()
	)

	BeforeEach(func() {
		d, fix, stop = setupDriver()
	})

	AfterEach(func() {
		stop()
	})

	Context("read path", func() {
		Describe("ListStorageSpaces", func() {
			It("returns at least one project space", func() {
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).ToNot(BeEmpty())
				Expect(spaces[0].SpaceType).To(Equal("project"))
				Expect(spaces[0].Name).ToNot(BeEmpty())
			})

			It("returns space with root ResourceId storageID=kiteworks", func() {
				spaces, err := d.ListStorageSpaces(fix.ctx, nil, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces[0].Root.StorageId).To(Equal("kiteworks"))
			})
		})

		Describe("GetMD", func() {
			It("returns container info for the space root", func() {
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				ri, err := d.GetMD(fix.ctx, ref, nil, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(ri.Type).To(Equal(provider.ResourceType_RESOURCE_TYPE_CONTAINER))
				Expect(ri.Id.OpaqueId).To(Equal(fix.spaceID))
			})
		})

		Describe("ListFolder", func() {
			It("succeeds for the root space", func() {
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				_, err := d.ListFolder(fix.ctx, ref, nil, nil)
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns expected children in mock mode", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				infos, err := d.ListFolder(fix.ctx, ref, nil, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(infos).To(HaveLen(1))
				Expect(infos[0].Name).To(Equal("hello.txt"))
				Expect(infos[0].Type).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
			})
		})

		Describe("Download", func() {
			It("streams the file content", func() {
				if fix.fileID == "" {
					Skip("no file found in root space")
				}
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.fileID},
				}
				_, rc, err := d.Download(fix.ctx, ref, func(_ *provider.ResourceInfo) bool { return true })
				Expect(err).ToNot(HaveOccurred())
				Expect(rc).ToNot(BeNil())
				defer rc.Close()
				b, err := io.ReadAll(rc)
				Expect(err).ToNot(HaveOccurred())
				if fix.fileContent != "" {
					Expect(string(b)).To(Equal(fix.fileContent))
				} else {
					Expect(len(b)).To(BeNumerically(">", 0))
				}
			})

			It("returns ResourceInfo without a reader when openReaderFunc returns false", func() {
				if fix.fileID == "" {
					Skip("no file found in root space")
				}
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.fileID},
				}
				ri, rc, err := d.Download(fix.ctx, ref, func(_ *provider.ResourceInfo) bool { return false })
				Expect(err).ToNot(HaveOccurred())
				Expect(ri).ToNot(BeNil())
				Expect(rc).To(BeNil())
			})
		})

		Describe("GetPathByID", func() {
			It("returns the path for the space root", func() {
				id := &provider.ResourceId{StorageId: "kiteworks", SpaceId: fix.spaceID, OpaqueId: fix.spaceID}
				path, err := d.GetPathByID(fix.ctx, id)
				Expect(err).ToNot(HaveOccurred())
				Expect(path).ToNot(BeEmpty())
			})
		})

		Describe("GetQuota", func() {
			It("reports the quota of the referenced folder", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				total, used, remaining, err := d.GetQuota(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(total).To(BeEquivalentTo(1073741824))
				Expect(used).To(BeEquivalentTo(14))
				Expect(remaining).To(BeEquivalentTo(1073741810))
			})

			It("reports unlimited remaining when no quota is applied", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: "space-2", OpaqueId: "space-2"},
				}
				total, used, remaining, err := d.GetQuota(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(total).To(BeEquivalentTo(0))
				Expect(used).To(BeEquivalentTo(99))
				Expect(remaining).To(BeEquivalentTo(uint64(math.MaxUint64)))
			})

			It("reports unlimited rather than failing when the lookup is denied", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: "no-quota-perm-1", OpaqueId: "no-quota-perm-1"},
				}
				total, used, remaining, err := d.GetQuota(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(total).To(BeEquivalentTo(0))
				Expect(used).To(BeEquivalentTo(0))
				Expect(remaining).To(BeEquivalentTo(uint64(math.MaxUint64)))
			})
		})
	})

	Context("error propagation", func() {
		It("GetMD propagates non-404 server errors", func() {
			skipIfRealBox()
			ref := &provider.Reference{
				ResourceId: &provider.ResourceId{SpaceId: "error-500", OpaqueId: "error-500"},
			}
			_, err := d.GetMD(fix.ctx, ref, nil, nil)
			Expect(err).To(HaveOccurred())
			Expect(err).ToNot(Satisfy(func(e error) bool { return errors.As(e, new(errtypes.NotFound)) }))
		})

		It("Download resolves root ref where OpaqueId is empty", func() {
			skipIfRealBox()
			ref := &provider.Reference{
				ResourceId: &provider.ResourceId{SpaceId: fix.spaceID},
			}
			ri, rc, err := d.Download(fix.ctx, ref, func(_ *provider.ResourceInfo) bool { return false })
			Expect(err).ToNot(HaveOccurred())
			Expect(ri).ToNot(BeNil())
			Expect(rc).To(BeNil())
		})
	})

	Context("write path", func() {
		Describe("CreateDir", func() {
			It("creates a folder under the space root in mock mode", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
					Path:       "./Documents",
				}
				result, err := d.CreateDir(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.SpaceID).To(Equal(fix.spaceID))
				Expect(result.ResourceID.OpaqueId).To(Equal("new-dir-1"))
				Expect(result.ResourceID.StorageId).To(Equal("kiteworks"))
			})
		})

		Describe("TouchFile", func() {
			It("creates a zero-byte stub and returns a TouchFileResult", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
					Path:       "./newfile.txt",
				}
				result, err := d.TouchFile(fix.ctx, ref, false, "")
				Expect(err).ToNot(HaveOccurred())
				Expect(result.SpaceID).To(Equal(fix.spaceID))
				Expect(result.ResourceID.OpaqueId).To(Equal("touched-1"))
				Expect(result.ResourceID.StorageId).To(Equal("kiteworks"))
			})
		})

		Describe("Delete", func() {
			It("deletes a folder by ID", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "folder-del-1"},
				}
				result, err := d.Delete(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ResourceId.OpaqueId).To(Equal("folder-del-1"))
			})

			It("falls back to file delete when folder delete returns 404", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "file-only-1"},
				}
				result, err := d.Delete(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.ResourceId.OpaqueId).To(Equal("file-only-1"))
			})
		})

		Describe("Move", func() {
			It("renames a file in place", func() {
				skipIfRealBox()
				src := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "src-file-1"},
				}
				dst := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
					Path:       "./renamed.txt",
				}
				result, err := d.Move(fix.ctx, src, dst)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})

			It("moves a file to a different folder", func() {
				skipIfRealBox()
				src := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "src-file-1"},
				}
				dst := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "folder-2"},
					Path:       "./src.txt",
				}
				result, err := d.Move(fix.ctx, src, dst)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})

			It("renames a folder in place", func() {
				skipIfRealBox()
				src := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "src-folder-1"},
				}
				dst := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
					Path:       "./RenamedFolder",
				}
				result, err := d.Move(fix.ctx, src, dst)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})

			It("moves a folder to a different parent", func() {
				skipIfRealBox()
				src := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "src-folder-1"},
				}
				dst := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "folder-2"},
					Path:       "./SrcFolder",
				}
				result, err := d.Move(fix.ctx, src, dst)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})
		})

		Describe("PrepareUpload", func() {
			It("returns VersionCreated=false and marks session for new files", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				result, err := d.PrepareUpload(fix.ctx, ref, "new-sess-1", storage.UploadInfo{NodeExisted: false})
				Expect(err).ToNot(HaveOccurred())
				Expect(result.VersionCreated).To(BeFalse())
			})

			It("returns VersionCreated=true and does not mark session for existing files", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: fix.spaceID},
				}
				result, err := d.PrepareUpload(fix.ctx, ref, "existing-sess-1", storage.UploadInfo{NodeExisted: true})
				Expect(err).ToNot(HaveOccurred())
				Expect(result.VersionCreated).To(BeTrue())
			})
		})

		Describe("CommitUpload", func() {
			It("uploads content for an existing file version", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "ver-file-1"},
				}
				content := "file content here"
				src := storage.UploadSource{
					Body:   io.NopCloser(strings.NewReader(content)),
					Length: int64(len(content)),
				}
				err := d.CommitUpload(fix.ctx, ref, "existing-sess-2", src)
				Expect(err).ToNot(HaveOccurred())
			})

			It("uploads content for a new file and cleans up the placeholder version", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "ver-file-1"},
				}
				_, err := d.PrepareUpload(fix.ctx, ref, "new-sess-2", storage.UploadInfo{NodeExisted: false})
				Expect(err).ToNot(HaveOccurred())

				content := "file content here"
				src := storage.UploadSource{
					Body:   io.NopCloser(strings.NewReader(content)),
					Length: int64(len(content)),
				}
				err = d.CommitUpload(fix.ctx, ref, "new-sess-2", src)
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Describe("RollbackUpload", func() {
			rollback := func(nodeID string, nodeExisted bool) error {
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: nodeID},
				}
				return d.RollbackUpload(fix.ctx, ref, "rollback-sess-1", storage.RollbackInfo{
					NodeExisted: nodeExisted,
					NodeID:      nodeID,
					ParentID:    fix.spaceID,
					Filename:    "rolled-back.txt",
				})
			}

			// error-500 fails every method, so a stray delete would surface as an error
			It("issues no request when the node already had content", func() {
				skipIfRealBox()
				Expect(rollback("error-500", true)).ToNot(HaveOccurred())
			})

			It("deletes the stub it created for a new file", func() {
				skipIfRealBox()
				Expect(rollback("file-only-1", false)).ToNot(HaveOccurred())
			})

			It("treats an already deleted node as success", func() {
				skipIfRealBox()
				Expect(rollback("rollback-gone-1", false)).ToNot(HaveOccurred())
			})

			// a TUS terminate before TouchFile ran leaves the placeholder id from initiate
			It("tolerates a node id kiteworks never knew", func() {
				skipIfRealBox()
				Expect(rollback("6b1e9c0e-3f2a-4d5b-8c7d-1a2b3c4d5e6f", false)).ToNot(HaveOccurred())
			})

			It("propagates errors other than 404", func() {
				skipIfRealBox()
				Expect(rollback("error-500", false)).To(HaveOccurred())
			})

			It("does not abort when the calling context is already cancelled", func() {
				skipIfRealBox()
				ctx, cancel := context.WithCancel(fix.ctx)
				cancel()
				err := d.RollbackUpload(ctx, &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "file-only-1"},
				}, "rollback-sess-2", storage.RollbackInfo{NodeID: "file-only-1"})
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	Context("locking", func() {
		precFailed := func(err error) bool { return errors.As(err, new(errtypes.PreconditionFailed)) }

		Describe("GetLock", func() {
			It("returns nil for an unlocked file", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				lock, err := d.GetLock(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(lock).To(BeNil())
			})

			It("returns a synthetic lock for a file locked by another session", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "ext-locked-file-1"},
				}
				lock, err := d.GetLock(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(lock).ToNot(BeNil())
				Expect(lock.LockId).To(Equal("kw-ext-locked-file-1"))
			})

			It("returns the stored lock after SetLock", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				stored := &provider.Lock{LockId: "my-lock-id", Type: provider.LockType_LOCK_TYPE_EXCL}
				_, err := d.SetLock(fix.ctx, ref, stored)
				Expect(err).ToNot(HaveOccurred())

				got, err := d.GetLock(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(got).ToNot(BeNil())
				Expect(got.LockId).To(Equal("my-lock-id"))
			})
		})

		Describe("SetLock", func() {
			It("locks an unlocked file and returns the space ID", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				result, err := d.SetLock(fix.ctx, ref, &provider.Lock{LockId: "lock-1"})
				Expect(err).ToNot(HaveOccurred())
				Expect(result.SpaceID).To(Equal(fix.spaceID))
			})

			It("returns PreconditionFailed when the file is already locked", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				_, err := d.SetLock(fix.ctx, ref, &provider.Lock{LockId: "lock-1"})
				Expect(err).ToNot(HaveOccurred())

				_, err = d.SetLock(fix.ctx, ref, &provider.Lock{LockId: "lock-2"})
				Expect(err).To(Satisfy(precFailed))
			})
		})

		Describe("Unlock", func() {
			It("unlocks a locked file and returns the space ID", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				_, err := d.SetLock(fix.ctx, ref, &provider.Lock{LockId: "lock-1"})
				Expect(err).ToNot(HaveOccurred())

				result, err := d.Unlock(fix.ctx, ref, &provider.Lock{LockId: "lock-1"})
				Expect(err).ToNot(HaveOccurred())
				Expect(result.SpaceID).To(Equal(fix.spaceID))
			})

			It("returns PreconditionFailed when the file is not locked", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "lock-file-1"},
				}
				_, err := d.Unlock(fix.ctx, ref, &provider.Lock{LockId: "lock-1"})
				Expect(err).To(Satisfy(precFailed))
			})
		})
	})

	Context("versions", func() {
		permDenied := func(err error) bool { return errors.As(err, new(errtypes.PermissionDenied)) }

		Describe("ListRevisions", func() {
			It("returns versions with fileID@versionID keys", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "versioned-file-1"},
				}
				revs, err := d.ListRevisions(fix.ctx, ref)
				Expect(err).ToNot(HaveOccurred())
				Expect(revs).To(HaveLen(2))
				Expect(revs[0].Key).To(Equal("versioned-file-1@rev-1"))
				Expect(revs[1].Key).To(Equal("versioned-file-1@rev-2"))
				Expect(revs[1].Size).To(BeEquivalentTo(20))
			})

			It("returns PermissionDenied when version_view is not granted", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "file-1"},
				}
				_, err := d.ListRevisions(fix.ctx, ref)
				Expect(err).To(Satisfy(permDenied))
			})
		})

		Describe("DownloadRevision", func() {
			It("streams version content", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "versioned-file-1"},
				}
				ri, rc, err := d.DownloadRevision(fix.ctx, ref, "versioned-file-1@rev-2", func(_ *provider.ResourceInfo) bool { return true })
				Expect(err).ToNot(HaveOccurred())
				Expect(rc).ToNot(BeNil())
				defer rc.Close()
				b, err := io.ReadAll(rc)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(b)).To(Equal("version 2 content"))
				Expect(ri.Size).To(BeEquivalentTo(len("version 2 content")))
			})

			It("returns nil reader when openReaderFunc returns false", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "versioned-file-1"},
				}
				ri, rc, err := d.DownloadRevision(fix.ctx, ref, "versioned-file-1@rev-2", func(_ *provider.ResourceInfo) bool { return false })
				Expect(err).ToNot(HaveOccurred())
				Expect(ri).ToNot(BeNil())
				Expect(rc).To(BeNil())
			})

			It("returns PermissionDenied when version_view is not granted", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "file-1"},
				}
				_, _, err := d.DownloadRevision(fix.ctx, ref, "file-1@rev-1", func(_ *provider.ResourceInfo) bool { return true })
				Expect(err).To(Satisfy(permDenied))
			})
		})

		Describe("RestoreRevision", func() {
			It("promotes the version successfully", func() {
				skipIfRealBox()
				result, err := d.RestoreRevision(fix.ctx, nil, "versioned-file-1@rev-2")
				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})

			It("returns PermissionDenied when version_promote is not granted", func() {
				skipIfRealBox()
				_, err := d.RestoreRevision(fix.ctx, nil, "file-1@rev-1")
				Expect(err).To(Satisfy(permDenied))
			})
		})

		Describe("GetMD with version OpaqueId", func() {
			It("returns ResourceInfo with the version OpaqueId", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "versioned-file-1@rev-2"},
				}
				ri, err := d.GetMD(fix.ctx, ref, nil, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(ri.Type).To(Equal(provider.ResourceType_RESOURCE_TYPE_FILE))
				Expect(ri.Id.OpaqueId).To(Equal("versioned-file-1@rev-2"))
			})
		})

		Describe("Download with version OpaqueId", func() {
			It("streams version content via version ref", func() {
				skipIfRealBox()
				ref := &provider.Reference{
					ResourceId: &provider.ResourceId{SpaceId: fix.spaceID, OpaqueId: "versioned-file-1@rev-2"},
				}
				ri, rc, err := d.Download(fix.ctx, ref, func(_ *provider.ResourceInfo) bool { return true })
				Expect(err).ToNot(HaveOccurred())
				Expect(rc).ToNot(BeNil())
				defer rc.Close()
				b, err := io.ReadAll(rc)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(b)).To(Equal("version 2 content"))
				Expect(ri.Size).To(BeEquivalentTo(len("version 2 content")))
			})
		})
	})

	Context("write rejection", func() {
		notSupported := func(err error) bool { return errors.As(err, new(errtypes.NotSupported)) }

		It("rejects AddGrant", func() {
			err := d.AddGrant(fix.ctx, &provider.Reference{ResourceId: &provider.ResourceId{SpaceId: fix.spaceID}}, &provider.Grant{})
			Expect(err).To(Satisfy(notSupported))
		})
		It("rejects InitiateUpload", func() {
			_, err := d.InitiateUpload(fix.ctx, &provider.Reference{ResourceId: &provider.ResourceId{SpaceId: fix.spaceID}}, 0, nil)
			Expect(err).To(Satisfy(notSupported))
		})
	})

	Context("capabilities", func() {
		It("declares the implemented write, version and lock support", func() {
			cp, ok := d.(storage.CapabilityProvider)
			Expect(ok).To(BeTrue())
			Expect(cp.Capabilities(fix.ctx)).To(Equal(storage.Capabilities{
				Upload:          true,
				CreateContainer: true,
				Delete:          true,
				Move:            true,
				Versioning:      true,
				Locking:         true,
			}))
		})
	})
})
