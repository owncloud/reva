package metadata_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
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
