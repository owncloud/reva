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
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata/mocks"
	"github.com/stretchr/testify/mock"
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

	// Many shares of one user or group go with one upload, also when they are in different spaces.
	Describe("RemoveMany", func() {
		var s1, s2, s3 string

		BeforeEach(func() {
			s1 = shareid.Encode("storageid", "spaceid", "s1")
			s2 = shareid.Encode("storageid", "spaceid", "s2")
			s3 = shareid.Encode("storageid", "otherspace", "s3")
			for _, id := range []string{s1, s2, s3} {
				Expect(c.Add(ctx, userid, id)).To(Succeed())
			}
		})

		It("removes the given shares with one upload", func() {
			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)

			Expect(c2.RemoveMany(ctx, userid, []string{s1, s3, shareid.Encode("storageid", "spaceid", "unknown")})).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(1)))

			fresh := sharecache.New(storage, "users", "created.json", 0*time.Second)
			spaces, err := fresh.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).To(HaveLen(1))
			Expect(spaces["storageid:spaceid"].IDs).To(HaveKey(s2))
			Expect(spaces["storageid:otherspace"].IDs).To(BeEmpty())
		})

		// An empty space entry makes each listing of the group read the space
		// file again, also when the space has no share for the group.
		It("removes the space entry with the last share of the space", func() {
			Expect(c.RemoveMany(ctx, userid, []string{s1, s3})).To(Succeed())

			fresh := sharecache.New(storage, "users", "created.json", 0*time.Second)
			spaces, err := fresh.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces).To(HaveKey("storageid:spaceid"), "the space still has a share")
			Expect(spaces).ToNot(HaveKey("storageid:otherspace"))
		})

		It("does not upload when none of the shares is in the cache", func() {
			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)

			Expect(c2.RemoveMany(ctx, userid, []string{
				shareid.Encode("storageid", "spaceid", "unknown"),
				shareid.Encode("storageid", "nospace", "unknown"),
			})).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(0)))
		})

		// A failed upload does not change the etag. The next sync then gets
		// NotModified and keeps the memory state. The memory state must still
		// have the shares, or a retry finds nothing to remove and does not upload.
		It("removes the shares on a retry after a failed upload", func() {
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)
			_, err := c2.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())

			Expect(c2.RemoveMany(ctx, userid, []string{s1, s3})).ToNot(Succeed())
			Expect(c2.RemoveMany(ctx, userid, []string{s1, s3})).To(Succeed())

			fresh := sharecache.New(storage, "users", "created.json", 0*time.Second)
			spaces, err := fresh.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).ToNot(HaveKey(s1))
			Expect(spaces).ToNot(HaveKey("storageid:otherspace"))
		})
	})

	Describe("Persist", func() {
		Context("with an existing entry", func() {
			BeforeEach(func() {
				Expect(c.Add(ctx, userid, shareID)).To(Succeed())
			})

			It("updates the etag", func() {
				download := func() *metadata.DownloadResponse {
					res, err := storage.Download(ctx, metadata.DownloadRequest{Path: "/users/user/created.json"})
					Expect(err).ToNot(HaveOccurred())
					return res
				}
				oldEtag := download().Etag
				Expect(oldEtag).ToNot(BeEmpty())

				Expect(c.Persist(ctx, userid)).To(Succeed())

				Expect(download().Etag).ToNot(Equal(oldEtag))
			})
		})
	})

	Describe("Add", func() {
		// A failed upload does not change the etag. The next sync then gets
		// NotModified and keeps the memory state. The memory state must not
		// have the share, or this pod lists a share that is not in the file.
		It("reads the file again after a failed upload", func() {
			s1 := shareid.Encode("storageid", "spaceid", "s1")
			s2 := shareid.Encode("storageid", "spaceid", "s2")
			Expect(c.Add(ctx, userid, s1)).To(Succeed())
			fs := &helpers.ErrOnceUploadStorage{Storage: &helpers.NotModifiedStorage{Storage: storage}, Err: errtypes.PermissionDenied("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)
			_, err := c2.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())

			Expect(c2.Add(ctx, userid, s2)).ToNot(Succeed())

			spaces, err := c2.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).To(HaveKey(s1))
			Expect(spaces["storageid:spaceid"].IDs).ToNot(HaveKey(s2))
		})

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

		// The Describe("RemoveMany") block above creates the file in its BeforeEach.
		It("RemoveMany does not upload and creates no file when no file exists yet", func() {
			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)

			Expect(c2.RemoveMany(ctx, userid, []string{shareid.Encode("storageid", "spaceid", "s1")})).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(0)))

			_, err := os.Stat(filepath.Join(tmpdir, "users", userid, "created.json"))
			Expect(os.IsNotExist(err)).To(BeTrue(), "no file was created, got %v", err)
		})

		It("does not waste a round trip on a cold cache when the remote file already exists", func() {
			Expect(c.Add(ctx, userid, shareID)).To(Succeed())

			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)
			Expect(c2.Remove(ctx, userid, shareID)).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(1)), "a cold Remove should sync first instead of blindly uploading an empty cache")
		})

		// The exact case from a production panic: a share in space-a, then a
		// Remove of a share in space-b for the same user, on a warm cache.
		It("does not panic on a warm cache when the share is in a space that the user has no entry for", func() {
			Expect(c.Add(ctx, userid, "storageid:space-a:share1")).To(Succeed())

			Expect(func() {
				Expect(c.Remove(ctx, userid, "storageid:space-b:share2")).To(Succeed())
			}).ToNot(Panic())

			spaces, err := c.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:space-a"].IDs).To(HaveKey("storageid:space-a:share1"))
		})

		// shareid.Decode gives ("", "", id) for an ID with no delimiter, so the space key is ":".
		It("does not panic and does not upload when the share ID has no delimiter", func() {
			Expect(c.Add(ctx, userid, "storageid:spaceid:share1")).To(Succeed())

			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)
			Expect(func() {
				Expect(c2.Remove(ctx, userid, "share-id-without-delimiter")).To(Succeed())
			}).ToNot(Panic())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(0)))

			Expect(func() {
				Expect(c.Remove(ctx, userid, "share-id-without-delimiter")).To(Succeed())
			}).ToNot(Panic(), "also on a warm cache")

			spaces, err := c.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).To(HaveKey("storageid:spaceid:share1"))
		})

		// The cleanup of a stale group share calls Remove on the group cache.
		// The group can have entries in other spaces only.
		It("does not panic when the user has no entry for the space of the share", func() {
			Expect(c.Add(ctx, userid, shareid.Encode("storageid", "spaceid", "s1"))).To(Succeed())

			Expect(func() {
				Expect(c.Remove(ctx, userid, shareid.Encode("storageid", "otherspace", "s2"))).To(Succeed())
			}).ToNot(Panic())

			spaces, err := c.List(ctx, userid)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces["storageid:spaceid"].IDs).To(HaveKey("storageid:spaceid:s1"))
		})

		It("does not upload when the share is not in the cache", func() {
			Expect(c.Add(ctx, userid, shareid.Encode("storageid", "spaceid", "s1"))).To(Succeed())

			cs := &countingStorage{Storage: storage}
			c2 := sharecache.New(cs, "users", "created.json", 0*time.Second)
			Expect(c2.Remove(ctx, userid, shareid.Encode("storageid", "spaceid", "unknown"))).To(Succeed())
			Expect(atomic.LoadInt32(&cs.uploads)).To(Equal(int32(0)))
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

			// The NotFound must not cause more storage calls than the write of
			// the empty file, for example a lookup in the trash.
			It("makes no storage call after the NotFound download except the write of the empty file", func() {
				st := mocks.NewStorage(GinkgoT())
				st.EXPECT().Download(mock.Anything, mock.Anything).Return(nil, errtypes.NotFound("received.json")).Once()
				st.EXPECT().MakeDirIfNotExist(mock.Anything, mock.Anything).Return(nil).Once()
				st.EXPECT().Upload(mock.Anything, mock.Anything).Return(&metadata.UploadResponse{Etag: "etag"}, nil).Once()
				c2 := sharecache.New(st, "groups", "received.json", 0*time.Second)

				shares, err := c2.List(ctx, "group1")
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(BeEmpty())
			})

			It("does not repeat the NotFound on later calls", func() {
				// a group that never received a share: the case from OCISDEV-1198
				cs := &countingStorage{Storage: storage}
				c2 := sharecache.New(cs, "groups", "received.json", 0*time.Second)

				for i := 0; i < 3; i++ {
					shares, err := c2.List(ctx, "group1")
					Expect(err).ToNot(HaveOccurred())
					Expect(shares).To(BeEmpty())
				}
				Expect(atomic.LoadInt32(&cs.notFounds)).To(Equal(int32(1)), "only the first call may get a NotFound")
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

	Describe("lock acquisition", func() {
		It("does not download when the context is already canceled", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			close(bs.Release)
			c2 := sharecache.New(bs, "users", "created.json", 0*time.Second)

			cctx, cancel := context.WithCancel(ctx)
			cancel()

			_, err := c2.List(cctx, userid)
			Expect(err).To(MatchError(context.Canceled))
			Expect(atomic.LoadInt32(&bs.Downloads)).To(BeZero())
		})

		It("abandons a List waiting for the user lock once the context is canceled", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			defer close(bs.Release)
			c2 := sharecache.New(bs, "users", "created.json", 0*time.Second)

			// the holder parks inside Download while holding the user lock
			go func() {
				defer GinkgoRecover()
				_, _ = c2.List(ctx, userid)
			}()
			Eventually(bs.Entered, 2*time.Second).Should(Receive())

			cctx, cancel := context.WithCancel(ctx)
			errc := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, err := c2.List(cctx, userid)
				errc <- err
			}()
			Consistently(errc, 100*time.Millisecond).ShouldNot(Receive(), "waiter should block while the lock is held")

			cancel()
			var err error
			Eventually(errc, 2*time.Second).Should(Receive(&err), "waiter is still parked on the user lock after its context was canceled")
			Expect(err).To(MatchError(context.Canceled))
			Expect(atomic.LoadInt32(&bs.Downloads)).To(Equal(int32(1)), "the canceled waiter must not download")
		})

		It("abandons an Add, Remove or RemoveMany waiting for the user lock once the context is done", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			defer close(bs.Release)
			c2 := sharecache.New(bs, "users", "created.json", 0*time.Second)

			go func() {
				defer GinkgoRecover()
				_, _ = c2.List(ctx, userid)
			}()
			Eventually(bs.Entered, 2*time.Second).Should(Receive())

			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			calls := []func() error{
				func() error {
					return c2.Add(cctx, userid, shareID)
				},
				func() error {
					return c2.Remove(cctx, userid, shareID)
				},
				func() error {
					return c2.RemoveMany(cctx, userid, []string{shareID})
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

		It("still serializes and succeeds for live contexts once the holder finishes", func() {
			bs := helpers.NewBlockingDownloadStorage(storage)
			c2 := sharecache.New(bs, "users", "created.json", 0*time.Second)

			errc := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					defer GinkgoRecover()
					_, err := c2.List(ctx, userid)
					errc <- err
				}()
			}
			Eventually(bs.Entered, 2*time.Second).Should(Receive())
			Consistently(bs.Entered, 100*time.Millisecond).ShouldNot(Receive(), "a second Download started while the first still held the lock")
			close(bs.Release)
			Eventually(errc, 2*time.Second).Should(Receive(BeNil()))
			Eventually(errc, 2*time.Second).Should(Receive(BeNil()))
		})
	})
})

// countingStorage counts Upload calls and NotFound downloads.
type countingStorage struct {
	metadata.Storage
	uploads   int32
	notFounds int32
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
