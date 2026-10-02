package metadata_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/filelocks"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisk(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Disk Suite")
}

var _ = Describe("Disk", func() {
	var (
		ctx     context.Context
		storage metadata.Storage
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		storage, err = metadata.NewDiskStorage(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(storage.Init(ctx, "test")).To(Succeed())
	})

	Describe("Upload", func() {
		It("returns AlreadyExists on IfNoneMatch=* when file exists", func() {
			Expect(storage.SimpleUpload(ctx, "f", []byte("v1"))).To(Succeed())
			_, err := storage.Upload(ctx, metadata.UploadRequest{
				Path:        "f",
				Content:     []byte("v2"),
				IfNoneMatch: []string{"*"},
			})
			Expect(err).To(HaveOccurred())
			_, ok := err.(errtypes.AlreadyExists)
			Expect(ok).To(BeTrue())
		})

		It("fails IfMatch on a target that doesn't exist, per RFC 9110, instead of writing", func() {
			_, err := storage.Upload(ctx, metadata.UploadRequest{
				Path:        "f",
				Content:     []byte("v1"),
				IfMatchEtag: "stale-etag-for-a-file-that-does-not-exist",
			})
			Expect(err).To(HaveOccurred())
			_, ok := err.(errtypes.PreconditionFailed)
			Expect(ok).To(BeTrue())
		})

		It("never produces a torn write under concurrent unconditional uploads", func() {
			const n = 15
			const size = 4096

			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer GinkgoRecover()
					defer wg.Done()
					content := bytes.Repeat([]byte{byte('A' + i%26)}, size)
					_, err := storage.Upload(ctx, metadata.UploadRequest{Path: "f", Content: content})
					Expect(err).ToNot(HaveOccurred())
				}(i)
			}
			wg.Wait()

			res, err := storage.Download(ctx, metadata.DownloadRequest{Path: "f"})
			Expect(err).ToNot(HaveOccurred())
			Expect(res.Content).To(HaveLen(size))
			// a torn write would mix bytes from two different writers
			Expect(bytes.Count(res.Content, []byte{res.Content[0]})).To(Equal(size))
		})
	})

	Describe("Download", func() {
		It("returns NotModified when IfNoneMatch etag matches", func() {
			res, err := storage.Upload(ctx, metadata.UploadRequest{Path: "f", Content: []byte("v1")})
			Expect(err).ToNot(HaveOccurred())
			_, err = storage.Download(ctx, metadata.DownloadRequest{
				Path:        "f",
				IfNoneMatch: []string{res.Etag},
			})
			Expect(err).To(HaveOccurred())
			_, ok := err.(errtypes.NotModified)
			Expect(ok).To(BeTrue())
		})
	})
})

// TestUpload_ReleaseErrorDoesNotClobberSuccessfulWrite proves a cleanup-only
// ReleaseLock error can't turn an already-successful write into a failure.
func TestUpload_ReleaseErrorDoesNotClobberSuccessfulWrite(t *testing.T) {
	const outerAttempts = 50

	for attempt := 0; attempt < outerAttempts; attempt++ {
		dir := t.TempDir()
		storage, err := metadata.NewDiskStorage(dir)
		require.NoError(t, err)
		require.NoError(t, storage.Init(context.Background(), "test"))

		contentPath := filepath.Join(dir, "f")
		lockPath := contentPath + filelocks.LockFileSuffix
		trapDir := filepath.Join(dir, "trap")
		require.NoError(t, os.Mkdir(trapDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(trapDir, "x"), []byte("x"), 0644))

		var stop atomic.Bool
		var hammered atomic.Int64
		go func() {
			for !stop.Load() {
				// Swap the lock path for the trap dir, then back, on repeat.
				_ = os.RemoveAll(lockPath)
				_ = os.Rename(trapDir, lockPath)
				hammered.Add(1)

				_ = os.RemoveAll(trapDir)
				require.NoError(t, os.Mkdir(trapDir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(trapDir, "x"), []byte("x"), 0644))

				_ = os.RemoveAll(lockPath)
			}
		}()

		_, uploadErr := storage.Upload(context.Background(), metadata.UploadRequest{
			Path:    "f",
			Content: []byte("v1"),
		})
		stop.Store(true)

		if uploadErr != nil {
			content, readErr := os.ReadFile(contentPath)
			if readErr == nil && string(content) == "v1" {
				t.Fatalf("Upload reported failure (%v) on outer attempt %d after %d hammer cycles, "+
					"but the content it reported on had already been written successfully to disk -- "+
					"a cleanup-only error clobbered a successful write", uploadErr, attempt, hammered.Load())
			}
			// else: acquisition itself failed, unrelated -- retry.
		}
	}
}

// TestUpload_IgnoresContextDuringLockAcquireWait proves AcquireWriteLock's
// blocking wait ignores ctx -- Upload's ctx param is unused (`_`), so a
// canceled/expired context doesn't shorten the wait under contention.
func TestUpload_IgnoresContextDuringLockAcquireWait(t *testing.T) {
	filelocks.SetMaxLockCycles(5)
	filelocks.SetLockCycleDurationFactor(10)

	dir := t.TempDir()
	storage, err := metadata.NewDiskStorage(dir)
	require.NoError(t, err)
	require.NoError(t, storage.Init(context.Background(), "test"))

	lockPath := filepath.Join(dir, "f") + filelocks.LockFileSuffix
	external := flock.New(lockPath)
	ok, lockErr := external.TryLock()
	require.NoError(t, lockErr)
	require.True(t, ok)
	defer external.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, uploadErr := storage.Upload(ctx, metadata.UploadRequest{Path: "f", Content: []byte("v1")})
	elapsed := time.Since(start)

	assert.Error(t, uploadErr)
	assert.Less(t, elapsed, 50*time.Millisecond,
		"Upload blocked %s despite a 10ms context deadline -- lock-acquire wait ignores ctx", elapsed)
}
