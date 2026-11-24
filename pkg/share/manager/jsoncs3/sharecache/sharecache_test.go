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

package sharecache_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/sharecache"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

var _ = Describe("Sharecache", func() {
	var (
		c       sharecache.Cache
		storage metadata.Storage

		userid  = "user"
		shareID = "storageid$spaceid!share1"
		ctx     context.Context
		tmpdir  string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error
		tmpdir, err = os.MkdirTemp("", "providercache-test")
		Expect(err).ToNot(HaveOccurred())

		err = os.MkdirAll(tmpdir, 0755)
		Expect(err).ToNot(HaveOccurred())

		storage, err = metadata.NewDiskStorage(tmpdir)
		Expect(err).ToNot(HaveOccurred())

		c = sharecache.New(storage, "users", "created.json", 0*time.Second)
		Expect(&c).ToNot(BeNil())
	})

	AfterEach(func() {
		if tmpdir != "" {
			os.RemoveAll(tmpdir)
		}
	})

	Describe("Persist", func() {
		Context("with an existing entry", func() {
			BeforeEach(func() {
				Expect(c.Add(ctx, userid, shareID)).To(Succeed())
			})

			It("updates the etag", func() {
				uc, _ := c.UserShares.Load(userid)
				oldEtag := uc.Etag
				Expect(oldEtag).ToNot(BeEmpty())

				Expect(c.Persist(ctx, userid)).To(Succeed())

				uc, _ = c.UserShares.Load(userid)
				Expect(uc.Etag).ToNot(Equal(oldEtag))
			})
		})
	})

	Describe("Add", func() {
		It("retries a TooEarly CAS conflict on persist instead of aborting", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

			err := c2.Add(ctx, userid, shareID)
			Expect(err).ToNot(HaveOccurred(), "TooEarly from write-lock contention should be retried, not treated as fatal")
			Expect(atomic.LoadInt32(&fs.Uploads)).To(Equal(int32(2)))
		})

		It("retries a transient cold-start sync error instead of aborting", func() {
			fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

			err := c2.Add(ctx, userid, shareID)
			Expect(err).ToNot(HaveOccurred(), "transient error on cold-start sync should be retried, not treated as fatal")
			Expect(atomic.LoadInt32(&fs.Downloads)).To(Equal(int32(2)))
		})

		// Add writes the file with the share, so it does not write an empty file first.
		It("makes exactly one upload when no file exists yet", func() {
			s1 := shareid.Encode("storageid", "spaceid", "s1")
			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)

			Expect(c2.Add(ctx, userid, s1)).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(1)))

			fresh := sharecache.New(storage, "users", "created.json", 0*time.Second)
			spaces, err := fresh.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).To(HaveKey(s1))
		})

		It("stops retrying once the context is canceled instead of exhausting all 100 attempts", func() {
			fs := &helpers.AlwaysAbortedUploadStorage{Storage: storage}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

			cctx, cancel := context.WithCancel(ctx)
			cancel()

			err := c2.Add(cctx, userid, shareID)
			Expect(err).To(HaveOccurred())
			Expect(atomic.LoadInt32(&fs.Uploads)).To(BeNumerically("<", 100),
				"Add should give up once the context is canceled instead of busy-spinning through all 100 persist attempts")
		})
	})

	Describe("Remove", func() {
		It("retries a TooEarly CAS conflict on persist instead of aborting", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)
			Expect(c2.Add(ctx, userid, shareID)).To(Succeed())

			err := c2.Remove(ctx, userid, shareID)
			Expect(err).ToNot(HaveOccurred(), "TooEarly from write-lock contention should be retried, not treated as fatal")
		})

		It("does not waste a round trip on a cold cache when the remote file already exists", func() {
			Expect(c.Add(ctx, userid, shareID)).To(Succeed())

			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)
			Expect(c2.Remove(ctx, userid, shareID)).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(1)), "a cold Remove should sync first instead of blindly uploading an empty cache")
		})

		It("clears the stale in-memory shares instead of returning deleted data", func() {
			Expect(c.Add(ctx, userid, shareID)).To(Succeed())

			spaces, err := c.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces).ToNot(BeEmpty())

			// simulate the backing file being deleted externally, e.g. by another node
			jsonPath := filepath.Join(tmpdir, "users", userid, "created.json")
			Expect(os.Remove(jsonPath)).To(Succeed())

			spaces, err = c.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces).To(BeEmpty(), "stale in-memory shares should not survive a NotFound resync")
		})
	})

	Describe("List", func() {
		Context("when no cache file exists yet", func() {
			It("creates an empty cache file on the first call", func() {
				shares, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(BeEmpty())

				_, err = os.Stat(filepath.Join(tmpdir, "users", userid, "created.json"))
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not repeat the NotFound and the trash lookup on later calls", func() {
				// a group that never received a share: the case from OCISDEV-1198
				cs := &countingStorage{Storage: storage}
				c2 := sharecache.New(cs, "groups", "received.json", 0*time.Second)

				for i := 0; i < 3; i++ {
					shares, err := c2.List(ctx, "group1")
					Expect(err).ToNot(HaveOccurred())
					Expect(shares).To(BeEmpty())
				}
				Expect(atomic.LoadInt32(&cs.notFounds)).To(Equal(int32(1)), "only the first call may get a NotFound")
				Expect(atomic.LoadInt32(&cs.trashChecks)).To(Equal(int32(1)), "only the first call may look at the trash")
				Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(1)), "the empty file is written once")
			})

			It("lets a new cache instance read the file without a NotFound", func() {
				_, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				// a new instance, e.g. another replica or a restarted service
				cs := &countingStorage{Storage: storage}
				c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)
				shares, err := c2.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(BeEmpty())
				Expect(atomic.LoadInt32(&cs.notFounds)).To(BeZero())
				Expect(atomic.LoadInt32(&cs.uploads)).To(BeZero())
			})

			It("still succeeds when the empty file cannot be written", func() {
				fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.PermissionDenied("injected")}
				c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

				shares, err := c2.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred(), "a failed write of the empty file must not fail the read")
				Expect(shares).To(BeEmpty())
				Expect(atomic.LoadInt32(&fs.Uploads)).To(Equal(int32(1)))
			})

			It("does not overwrite a file another writer created after the NotFound", func() {
				// another instance creates the file with a share ...
				Expect(c.Add(ctx, userid, shareID)).To(Succeed())

				// ... after this instance got its NotFound
				fs := &helpers.ErrOnceDownloadStorage{Storage: storage, Err: errtypes.NotFound("injected: stale read")}
				c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)
				_, err := c2.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				// the share is still on storage, and the next call returns it
				shares, err := c2.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(HaveLen(1))

				c3 := sharecache.New(storage, "users", "created.json", 0*time.Second)
				shares, err = c3.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(HaveLen(1))
			})
		})
	})
})

// countingStorage counts Upload calls, NotFound downloads and trash lookups.
type countingStorage struct {
	metadata.Storage
	uploads     int32
	notFounds   int32
	trashChecks int32
}

func (c *countingStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&c.uploads, 1)
	return c.Storage.Upload(ctx, req)
}

func (c *countingStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	res, err := c.Storage.Download(ctx, req)
	if _, ok := err.(errtypes.NotFound); ok {
		atomic.AddInt32(&c.notFounds, 1)
	}
	return res, err
}

func (c *countingStorage) WasRecentlyDeleted(ctx context.Context, path string) (bool, error) {
	atomic.AddInt32(&c.trashChecks, 1)
	return c.Storage.WasRecentlyDeleted(ctx, path)
}
