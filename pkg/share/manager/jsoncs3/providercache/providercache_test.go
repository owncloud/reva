// Copyright 2018-2022 CERN
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

package providercache_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

var _ = Describe("Cache", func() {
	var (
		c       providercache.Cache
		storage metadata.Storage

		storageID = "storageid"
		spaceID   = "spaceid"
		shareID   = "storageid$spaceid!share1"
		share1    *collaboration.Share
		ctx       context.Context
		tmpdir    string
	)

	BeforeEach(func() {
		ctx = context.Background()
		share1 = &collaboration.Share{
			Id: &collaboration.ShareId{
				OpaqueId: "share1",
			},
		}

		var err error
		tmpdir, err = os.MkdirTemp("", "providercache-test")
		Expect(err).ToNot(HaveOccurred())

		err = os.MkdirAll(tmpdir, 0755)
		Expect(err).ToNot(HaveOccurred())

		storage, err = metadata.NewDiskStorage(tmpdir)
		Expect(err).ToNot(HaveOccurred())

		c = providercache.New(storage, 0*time.Second)
		Expect(&c).ToNot(BeNil())
	})

	AfterEach(func() {
		if tmpdir != "" {
			os.RemoveAll(tmpdir)
		}
	})

	Describe("Add", func() {
		It("adds a share", func() {
			s, err := c.Get(ctx, storageID, spaceID, shareID, false)
			Expect(err).ToNot(HaveOccurred())
			Expect(s).To(BeNil())

			Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())

			s, err = c.Get(ctx, storageID, spaceID, shareID, false)
			Expect(err).ToNot(HaveOccurred())
			Expect(s).ToNot(BeNil())
			Expect(s).To(Equal(share1))
		})

		It("sets the etag", func() {
			Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())
			spaces, ok := c.Providers.Load(storageID)
			Expect(ok).To(BeTrue())
			space, ok := spaces.Spaces.Load(spaceID)
			Expect(ok).To(BeTrue())
			Expect(space.Etag).ToNot(BeEmpty())
		})

		It("updates the etag", func() {
			Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())
			spaces, ok := c.Providers.Load(storageID)
			Expect(ok).To(BeTrue())
			space, ok := spaces.Spaces.Load(spaceID)
			Expect(ok).To(BeTrue())
			old := space.Etag
			Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())
			Expect(space.Etag).ToNot(Equal(old))
		})

		It("retries a TooEarly CAS conflict on persist instead of aborting", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := providercache.New(fs, 0*time.Second)

			err := c2.Add(ctx, storageID, spaceID, shareID, share1)
			Expect(err).ToNot(HaveOccurred(), "TooEarly from write-lock contention should be retried, not treated as fatal")
			Expect(atomic.LoadInt32(&fs.Uploads)).To(Equal(int32(2)))
		})

		It("retries a transient cold-start sync error instead of aborting", func() {
			fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := providercache.New(fs, 0*time.Second)

			err := c2.Add(ctx, storageID, spaceID, shareID, share1)
			Expect(err).ToNot(HaveOccurred(), "transient error on cold-start sync should be retried, not treated as fatal")
			Expect(atomic.LoadInt32(&fs.Downloads)).To(Equal(int32(2)))
		})

		It("stops retrying once the context is canceled instead of exhausting all 100 attempts", func() {
			fs := &helpers.AlwaysAbortedUploadStorage{Storage: storage}
			c2 := providercache.New(fs, 0*time.Second)

			cctx, cancel := context.WithCancel(ctx)
			cancel()

			err := c2.Add(cctx, storageID, spaceID, shareID, share1)
			Expect(err).To(HaveOccurred())
			Expect(atomic.LoadInt32(&fs.Uploads)).To(BeNumerically("<", 100),
				"Add should give up once the context is canceled instead of busy-spinning through all 100 persist attempts")
		})

		It("fails instead of dropping other shares when the post-conflict resync finds the file gone", func() {
			otherShareID := "storageid$spaceid!other-share"
			otherShare := &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "other-share"}}
			initial := providercache.Shares{Shares: map[string]*collaboration.Share{
				shareID:      share1,
				otherShareID: otherShare,
			}}
			initialBytes, err := json.Marshal(initial)
			Expect(err).ToNot(HaveOccurred())

			fs := &conflictThenNotFoundStorage{initialData: initialBytes, initialEtag: "etag1"}
			c2 := providercache.New(fs, 0*time.Second)

			newShareID := "storageid$spaceid!share3"
			addErr := c2.Add(ctx, storageID, spaceID, newShareID, &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share3"}})
			Expect(addErr).To(HaveOccurred(), "a backing file that vanished mid-retry must surface as an error, not a partial write")
			Expect(addErr).To(BeAssignableToTypeOf(errtypes.NotFound("")))
			Expect(fs.uploadedContent()).To(BeNil(), "no write should ever reach the storage once the resync reports NotFound")
		})

		It("[hypothesis, unfixed] a cold-start sync has no way to detect the same race, confirming the gap is structural, not call-site-specific", func() {
			// Disk's WasRecentlyDeleted always answers false (no trash), so this
			// documents a known, accepted gap. Diagnosis: DOCS/RESEARCH_OCISDEV-855_IDEAL.md.
			fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.NotFound("injected: cold sync races a deletion")}
			c2 := providercache.New(fs, 0*time.Second)

			newShareID := "storageid$spaceid!cold-share"
			err := c2.Add(ctx, storageID, spaceID, newShareID, &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "cold-share"}})

			Expect(err).ToNot(HaveOccurred(),
				"cold-start sync unconditionally treats NotFound as safe-to-reset -- there is no etag to gate on, confirming no defense exists here")
		})

		It("errors on a cold-start NotFound when the backend confirms the file was trashed", func() {
			// Closes the gap above for backends that can answer the question
			// (CS3/decomposedfs, via WasRecentlyDeleted). Disk can't (always
			// false), so this is backend-specific, not a general fix.
			base := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.NotFound("injected: cold sync races a deletion")}
			fs := &trashAwareStorage{Storage: base, trashed: true}
			c2 := providercache.New(fs, 0*time.Second)

			newShareID := "storageid$spaceid!cold-share"
			err := c2.Add(ctx, storageID, spaceID, newShareID, &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "cold-share"}})

			Expect(err).To(HaveOccurred(),
				"a confirmed-trashed path must surface as an error instead of being silently treated as empty")
		})
	})

	Context("with an existing entry", func() {
		BeforeEach(func() {
			Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())
		})

		Describe("Get", func() {
			It("returns the entry", func() {
				s, err := c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).ToNot(BeNil())
			})
		})

		Describe("Remove", func() {
			It("removes the entry", func() {
				s, err := c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).ToNot(BeNil())
				Expect(s).To(Equal(share1))

				Expect(c.Remove(ctx, storageID, spaceID, shareID)).To(Succeed())

				s, err = c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("updates the etag", func() {
				Expect(c.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())
				spaces, ok := c.Providers.Load(storageID)
				Expect(ok).To(BeTrue())
				space, ok := spaces.Spaces.Load(spaceID)
				Expect(ok).To(BeTrue())
				old := space.Etag
				Expect(c.Remove(ctx, storageID, spaceID, shareID)).To(Succeed())
				Expect(space.Etag).ToNot(Equal(old))
			})

			It("retries a TooEarly CAS conflict on persist instead of aborting", func() {
				fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
				c2 := providercache.New(fs, 0*time.Second)
				Expect(c2.Add(ctx, storageID, spaceID, shareID, share1)).To(Succeed())

				err := c2.Remove(ctx, storageID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred(), "TooEarly from write-lock contention should be retried, not treated as fatal")
			})

			It("clears the stale in-memory space instead of returning deleted data", func() {
				s, err := c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(Equal(share1))

				// simulate the backing file being deleted externally, e.g. by another node
				jsonPath := filepath.Join(tmpdir, "storages", storageID, spaceID+".json")
				Expect(os.Remove(jsonPath)).To(Succeed())

				s, err = c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil(), "stale in-memory share should not survive a NotFound resync")
			})
		})

		Describe("Persist", func() {
			It("handles non-existent storages", func() {
				Expect(c.Persist(ctx, "foo", "bar")).To(Succeed())
			})
			It("handles non-existent spaces", func() {
				Expect(c.Persist(ctx, storageID, "bar")).To(Succeed())
			})

			It("persists", func() {
				Expect(c.Persist(ctx, storageID, spaceID)).To(Succeed())
			})

			It("updates the etag", func() {
				spaces, ok := c.Providers.Load(storageID)
				Expect(ok).To(BeTrue())
				space, ok := spaces.Spaces.Load(spaceID)
				Expect(ok).To(BeTrue())
				oldEtag := space.Etag

				Expect(c.Persist(ctx, storageID, spaceID)).To(Succeed())
				Expect(space.Etag).ToNot(Equal(oldEtag))
			})

		})

		Describe("PersistWithTime", func() {
			It("does not persist if the etag changed", func() {
				time.Sleep(1 * time.Nanosecond)
				path := filepath.Join(tmpdir, "storages/storageid/spaceid.json")
				now := time.Now()
				_ = os.Chtimes(path, now, now) // this only works for the file backend
				Expect(c.Persist(ctx, storageID, spaceID)).ToNot(Succeed())
			})
		})

		Describe("PurgeSpace", func() {
			It("removes the entry", func() {
				Expect(c.PurgeSpace(ctx, storageID, spaceID)).To(Succeed())

				s, err := c.Get(ctx, storageID, spaceID, shareID, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})
		})

		Describe("All", func() {
			It("returns all entries", func() {
				entries, err := c.All(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(entries.Count()).To(Equal(1))
			})

			It("does not leak the space lock when syncWithLock fails", func() {
				// outer BeforeEach already persisted via c; c2 just needs to observe it through a failing storage.
				fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.InternalError("injected")}
				c2 := providercache.New(fs, 0*time.Second)

				_, err := c2.All(ctx)
				Expect(err).To(HaveOccurred())

				done := make(chan struct{})
				go func() {
					defer GinkgoRecover()
					_, _ = c2.Get(ctx, storageID, spaceID, shareID, true)
					close(done)
				}()

				Eventually(done, 2*time.Second).Should(BeClosed(), "space lock was not released after a failed All(), it leaked")
			})
		})
	})
})


// conflictThenNotFoundStorage returns initialData/initialEtag on the first
// Download (cold-start sync), fails the first Upload with a CAS conflict,
// then returns NotFound on the second Download (the post-conflict resync),
// simulating the backing file being deleted in that window. Records whatever
// the caller eventually uploads.
type conflictThenNotFoundStorage struct {
	metadata.Storage
	downloads   int32
	uploads     int32
	initialData []byte
	initialEtag string

	mu      sync.Mutex
	content []byte
}

func (c *conflictThenNotFoundStorage) MakeDirIfNotExist(_ context.Context, _ string) error {
	return nil
}

func (c *conflictThenNotFoundStorage) Download(_ context.Context, _ metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if atomic.AddInt32(&c.downloads, 1) == 1 {
		return &metadata.DownloadResponse{Content: c.initialData, Etag: c.initialEtag}, nil
	}
	return nil, errtypes.NotFound("injected: file deleted externally")
}

func (c *conflictThenNotFoundStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if atomic.AddInt32(&c.uploads, 1) == 1 {
		return nil, errtypes.Aborted("injected")
	}
	c.mu.Lock()
	c.content = req.Content
	c.mu.Unlock()
	return &metadata.UploadResponse{Etag: "etag2"}, nil
}

func (c *conflictThenNotFoundStorage) uploadedContent() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content
}

// trashAwareStorage overrides WasRecentlyDeleted with a fixed answer,
// simulating a backend (CS3/decomposedfs) that can confirm trash state.
type trashAwareStorage struct {
	metadata.Storage
	trashed bool
}

func (t *trashAwareStorage) WasRecentlyDeleted(_ context.Context, _ string) (bool, error) {
	return t.trashed, nil
}
