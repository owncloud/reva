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
	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata/mocks"
	"github.com/stretchr/testify/mock"
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

	Describe("ListSpace", func() {
		// Each listing of a space without shares gets this NotFound. The
		// NotFound must not cause more storage calls, for example a lookup in
		// the trash.
		It("makes no storage call after the NotFound download", func() {
			st := mocks.NewStorage(GinkgoT())
			st.EXPECT().Download(mock.Anything, mock.Anything).Return(nil, errtypes.NotFound("spaceid.json")).Once()
			c2 := providercache.New(st, 0*time.Second)

			shares, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).To(BeEmpty())
		})
	})

	// Many shares in one space go with one upload, for example in a purge or
	// when a listing finds many expired shares.
	Describe("RemoveMany", func() {
		BeforeEach(func() {
			for _, id := range []string{"s1", "s2", "s3"} {
				Expect(c.Add(ctx, storageID, spaceID, id, &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: id}})).To(Succeed())
			}
		})

		It("removes the given shares with one upload", func() {
			cs := &helpers.CountingUploadStorage{Storage: storage}
			c2 := providercache.New(cs, 0*time.Second)

			removed, err := c2.RemoveMany(ctx, storageID, spaceID, []string{"s1", "s2", "unknown"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(ConsistOf("s1", "s2"), "only the shares that were in the space")
			Expect(atomic.LoadInt32(&cs.Uploads)).To(Equal(int32(1)))

			fresh := providercache.New(storage, 0*time.Second)
			shares, err := fresh.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).To(HaveLen(1))
			Expect(shares.Shares).To(HaveKey("s3"))
		})

		It("does not upload when none of the shares is in the space", func() {
			cs := &helpers.CountingUploadStorage{Storage: storage}
			c2 := providercache.New(cs, 0*time.Second)

			removed, err := c2.RemoveMany(ctx, storageID, spaceID, []string{"unknown1", "unknown2"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(BeEmpty())
			Expect(atomic.LoadInt32(&cs.Uploads)).To(Equal(int32(0)))
		})

		// Only one caller may report a share as removed. The callers use the
		// result to send one event for each share.
		It("does not report a share that another pod removed first", func() {
			podA := providercache.New(storage, 0*time.Second)
			podB := providercache.New(storage, 0*time.Second)
			_, err := podB.ListSpace(ctx, storageID, spaceID) // pod B has the share in memory
			Expect(err).ToNot(HaveOccurred())

			removed, err := podA.RemoveMany(ctx, storageID, spaceID, []string{"s1"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(ConsistOf("s1"))

			removed, err = podB.RemoveMany(ctx, storageID, spaceID, []string{"s1", "s2"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(ConsistOf("s2"), "pod A removed s1 first")
		})

		// A failed upload does not change the etag. The next sync then gets
		// NotModified and keeps the memory state. The memory state must still
		// have the share, or a retry finds nothing to remove and does not upload.
		It("removes the shares on a retry after a failed upload", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := providercache.New(fs, 0*time.Second)
			_, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())

			_, err = c2.RemoveMany(ctx, storageID, spaceID, []string{"s1"})
			Expect(err).To(HaveOccurred())

			removed, err := c2.RemoveMany(ctx, storageID, spaceID, []string{"s1"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(ConsistOf("s1"))

			fresh := providercache.New(storage, 0*time.Second)
			shares, err := fresh.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).ToNot(HaveKey("s1"))
		})
		// Break: a parallel listing gets a temporary NotFound and stores an empty
		// space with no etag. RemoveMany then finds no share, does not upload,
		// and reports success, but the share stays in the file.
		It("removes the shares after a temporary NotFound emptied the space in memory", func() {
			ts := &helpers.ToggleNotFoundDownloadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}}
			c2 := providercache.New(ts, 0*time.Second)
			_, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			ts.Fail.Store(true)
			_, err = c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			ts.Fail.Store(false)

			removed, err := c2.RemoveMany(ctx, storageID, spaceID, []string{"s1"})
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(ConsistOf("s1"))

			fresh := providercache.New(storage, 0*time.Second)
			shares, err := fresh.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).ToNot(HaveKey("s1"))
			Expect(shares.Shares).To(HaveKey("s2"))
		})
	})

	Describe("Add", func() {
		// A failed upload does not change the etag. The next sync then gets
		// NotModified and keeps the memory state. The memory state must not
		// have the share, or this pod lists a share that is not in the file.
		It("reads the file again after a failed upload", func() {
			Expect(c.Add(ctx, storageID, spaceID, "s1", &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "s1"}})).To(Succeed())
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := providercache.New(fs, 0*time.Second)
			_, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())

			Expect(c2.Add(ctx, storageID, spaceID, "s2", &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "s2"}})).ToNot(Succeed())

			shares, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).To(HaveKey("s1"))
			Expect(shares.Shares).ToNot(HaveKey("s2"))
		})

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
			// Without an etag, a NotFound always resets the cache. This documents
			// a known, accepted gap: the jsoncs3 caches never delete their files,
			// so only a manual delete can cause this race. A lookup in the trash
			// does not close the gap.
			fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.NotFound("injected: cold sync races a deletion")}
			c2 := providercache.New(fs, 0*time.Second)

			newShareID := "storageid$spaceid!cold-share"
			err := c2.Add(ctx, storageID, spaceID, newShareID, &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "cold-share"}})

			Expect(err).ToNot(HaveOccurred(),
				"cold-start sync unconditionally treats NotFound as safe-to-reset -- there is no etag to gate on, confirming no defense exists here")
		})
	})

	// UpdateShare reads the share before it takes the space lock. Update must
	// read the share again under the lock and write the change to the file.
	Describe("Update", func() {
		var expiration *typesv1beta1.Timestamp

		setExpiration := func(s *collaboration.Share) { s.Expiration = expiration }
		expirationOnStorage := func(id string) *typesv1beta1.Timestamp {
			fresh := providercache.New(storage, 0*time.Second)
			shares, err := fresh.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).To(HaveKey(id))
			return shares.Shares[id].GetExpiration()
		}

		BeforeEach(func() {
			expiration = &typesv1beta1.Timestamp{Seconds: 42}
			Expect(c.Add(ctx, storageID, spaceID, "s1", &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "s1"}})).To(Succeed())
		})

		It("rejects missing storage, space or share ids", func() {
			_, err := c.Update(ctx, "", spaceID, "s1", setExpiration)
			Expect(err).To(MatchError("missing storage id"))
			_, err = c.Update(ctx, storageID, "", "s1", setExpiration)
			Expect(err).To(MatchError("missing space id"))
			_, err = c.Update(ctx, storageID, spaceID, "", setExpiration)
			Expect(err).To(MatchError("missing share id"))
		})

		It("writes the change when the space is not in memory", func() {
			c2 := providercache.New(storage, 0*time.Second)

			updated, err := c2.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.GetExpiration()).To(Equal(expiration))
			Expect(expirationOnStorage("s1")).To(Equal(expiration))
		})

		// Break: a failed upload of a parallel Add drops the space from memory.
		// Persist then finds no space, and the update is lost without an error.
		It("writes the change after a failed upload dropped the space", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := providercache.New(fs, 0*time.Second)
			_, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(c2.Add(ctx, storageID, spaceID, "s2", &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "s2"}})).ToNot(Succeed())

			_, err = c2.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).ToNot(HaveOccurred())
			Expect(expirationOnStorage("s1")).To(Equal(expiration))
		})

		It("applies the change again after a CAS conflict", func() {
			podA := providercache.New(storage, 0*time.Second)
			podB := providercache.New(storage, 0*time.Second)
			_, err := podB.ListSpace(ctx, storageID, spaceID) // pod B has an old etag
			Expect(err).ToNot(HaveOccurred())
			Expect(podA.Add(ctx, storageID, spaceID, "s2", &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "s2"}})).To(Succeed())

			_, err = podB.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).ToNot(HaveOccurred())
			Expect(expirationOnStorage("s1")).To(Equal(expiration))
			Expect(expirationOnStorage("s2")).To(BeNil(), "the update keeps the share of pod A")
		})

		// Break: an update writes the share that it read before the lock. A
		// parallel revoke on another pod is then undone.
		It("returns NotFound and does not write a share that another pod removed", func() {
			podA := providercache.New(storage, 0*time.Second)
			podB := providercache.New(storage, 0*time.Second)
			_, err := podB.ListSpace(ctx, storageID, spaceID) // pod B has the share in memory
			Expect(err).ToNot(HaveOccurred())
			Expect(podA.Remove(ctx, storageID, spaceID, "s1")).To(Succeed())

			_, err = podB.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))

			fresh := providercache.New(storage, 0*time.Second)
			shares, err := fresh.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(shares.Shares).ToNot(HaveKey("s1"))
		})

		// Break: a parallel listing gets a temporary NotFound and stores an empty
		// space with no etag. Update then returns NotFound for a share that is
		// in the file.
		It("writes the change after a temporary NotFound emptied the space in memory", func() {
			ts := &helpers.ToggleNotFoundDownloadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}}
			c2 := providercache.New(ts, 0*time.Second)
			_, err := c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			ts.Fail.Store(true)
			_, err = c2.ListSpace(ctx, storageID, spaceID)
			Expect(err).ToNot(HaveOccurred())
			ts.Fail.Store(false)

			updated, err := c2.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated.GetExpiration()).To(Equal(expiration))
			Expect(expirationOnStorage("s1")).To(Equal(expiration))
		})

		// Break: the update changes the cached share in place. A reader that
		// holds the share then sees a change that is not in the file.
		It("does not change the share in memory when the upload fails", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := providercache.New(fs, 0*time.Second)
			before, err := c2.Get(ctx, storageID, spaceID, "s1", false)
			Expect(err).ToNot(HaveOccurred())

			_, err = c2.Update(ctx, storageID, spaceID, "s1", setExpiration)
			Expect(err).To(HaveOccurred())
			Expect(before.GetExpiration()).To(BeNil())

			after, err := c2.Get(ctx, storageID, spaceID, "s1", false)
			Expect(err).ToNot(HaveOccurred())
			Expect(after.GetExpiration()).To(BeNil())
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

			// PurgeSpace keeps the purged space in memory. Add does not sync a
			// space that is in memory, so it writes to the map of that space.
			It("adds a share to the purged space", func() {
				Expect(c.PurgeSpace(ctx, storageID, spaceID)).To(Succeed())

				share2 := &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share2"}}
				Expect(c.Add(ctx, storageID, spaceID, "storageid$spaceid!share2", share2)).To(Succeed())

				s, err := c.Get(ctx, storageID, spaceID, "storageid$spaceid!share2", false)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).ToNot(BeNil())
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

	Describe("lock acquisition", func() {
		It("does not download when the context is already canceled", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			close(bs.Release)
			c2 := providercache.New(bs, 0*time.Second)

			cctx, cancel := context.WithCancel(ctx)
			cancel()

			_, err := c2.ListSpace(cctx, storageID, spaceID)
			Expect(err).To(MatchError(context.Canceled))
			Expect(atomic.LoadInt32(&bs.Downloads)).To(BeZero())
		})

		It("abandons a ListSpace waiting for the space lock once the context is canceled", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			defer close(bs.Release)
			c2 := providercache.New(bs, 0*time.Second)

			// the holder parks inside Download while holding the space lock
			go func() {
				defer GinkgoRecover()
				_, _ = c2.ListSpace(ctx, storageID, spaceID)
			}()
			Eventually(bs.Entered, 2*time.Second).Should(Receive())

			cctx, cancel := context.WithCancel(ctx)
			errc := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, err := c2.ListSpace(cctx, storageID, spaceID)
				errc <- err
			}()
			Consistently(errc, 100*time.Millisecond).ShouldNot(Receive(), "waiter should block while the lock is held")

			cancel()
			var err error
			Eventually(errc, 2*time.Second).Should(Receive(&err), "waiter is still parked on the space lock after its context was canceled")
			Expect(err).To(MatchError(context.Canceled))
			Expect(atomic.LoadInt32(&bs.Downloads)).To(Equal(int32(1)), "the canceled waiter must not download")
		})

		It("abandons LockSpace, Add, Get, Remove, RemoveMany, Update and PurgeSpace once the context is done", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			defer close(bs.Release)
			c2 := providercache.New(bs, 0*time.Second)

			go func() {
				defer GinkgoRecover()
				_, _ = c2.ListSpace(ctx, storageID, spaceID)
			}()
			Eventually(bs.Entered, 2*time.Second).Should(Receive())

			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			calls := []func() error{
				func() error {
					unlock, err := c2.LockSpace(cctx, spaceID)
					if unlock != nil {
						unlock()
					}
					return err
				},
				func() error {
					return c2.Add(cctx, storageID, spaceID, shareID, share1)
				},
				func() error {
					_, err := c2.Get(cctx, storageID, spaceID, shareID, false)
					return err
				},
				func() error {
					return c2.Remove(cctx, storageID, spaceID, shareID)
				},
				func() error {
					_, err := c2.RemoveMany(cctx, storageID, spaceID, []string{shareID})
					return err
				},
				func() error {
					_, err := c2.Update(cctx, storageID, spaceID, shareID, func(*collaboration.Share) {})
					return err
				},
				func() error {
					return c2.PurgeSpace(cctx, storageID, spaceID)
				},
			}
			errc := make(chan error, len(calls))
			for _, call := range calls {
				go func() {
					defer GinkgoRecover()
					errc <- call()
				}()
			}
			for range calls {
				var err error
				Eventually(errc, 2*time.Second).Should(Receive(&err), "a call is still parked on the lock after its context expired")
				Expect(err).To(MatchError(context.DeadlineExceeded))
			}
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
