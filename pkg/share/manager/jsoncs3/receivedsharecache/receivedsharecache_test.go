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

package receivedsharecache_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/receivedsharecache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache", func() {
	var (
		c       receivedsharecache.Cache
		storage metadata.Storage

		userID  = "user"
		spaceID = "spaceid"
		shareID = "storageid$spaceid!share1"
		share   = &collaboration.Share{
			Id: &collaboration.ShareId{
				OpaqueId: shareID,
			},
		}
		ctx    context.Context
		tmpdir string
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

		c = receivedsharecache.New(storage, 0*time.Second)
		Expect(&c).ToNot(BeNil())
	})

	AfterEach(func() {
		if tmpdir != "" {
			os.RemoveAll(tmpdir)
		}
	})

	Describe("List", func() {
		Context("when no cache file exists yet", func() {
			It("returns empty spaces", func() {
				spaces, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())
			})

			It("retries a transient error on the initial sync instead of failing immediately", func() {
				fs := &flakyTooEarlyDownloadStorage{Storage: storage, failures: 3}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				_, err := c2.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
			})

			It("succeeds even when the underlying storage would refuse a write (read must not require write permission)", func() {
				ps := &alwaysFailUploadStorage{Storage: storage, err: errtypes.PermissionDenied("injected")}
				c2 := receivedsharecache.New(ps, 0*time.Second)

				spaces, err := c2.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())
				Expect(atomic.LoadInt32(&ps.uploads)).To(Equal(int32(0)), "a pure read must never call Upload")
			})

			It("is readable by a fresh cache instance after first call", func() {
				_, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())

				// a new cache instance must be able to read the bootstrapped file
				c2 := receivedsharecache.New(storage, 0*time.Second)
				spaces, err := c2.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())
			})

			It("allows adding a share after bootstrap", func() {
				_, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())

				rs := &collaboration.ReceivedShare{
					Share: share,
					State: collaboration.ShareState_SHARE_STATE_PENDING,
				}
				Expect(c.Add(ctx, userID, spaceID, rs)).To(Succeed())

				spaces, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces[spaceID].States).To(HaveKey(shareID))
			})
		})
	})

	Describe("Add", func() {
		It("adds an entry", func() {
			rs := &collaboration.ReceivedShare{
				Share: share,
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			err := c.Add(ctx, userID, spaceID, rs)
			Expect(err).ToNot(HaveOccurred())

			s, err := c.Get(ctx, userID, spaceID, shareID)
			Expect(err).ToNot(HaveOccurred())
			Expect(s).ToNot(BeNil())
		})

		It("persists the new entry", func() {
			rs := &collaboration.ReceivedShare{
				Share: share,
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			err := c.Add(ctx, userID, spaceID, rs)
			Expect(err).ToNot(HaveOccurred())

			c = receivedsharecache.New(storage, 0*time.Second)
			s, err := c.Get(ctx, userID, spaceID, shareID)
			Expect(err).ToNot(HaveOccurred())
			Expect(s).ToNot(BeNil())
		})
	})

	Describe("concurrent writes from multiple cache instances", func() {
		It("preserves the share when 15 replicas write the same file simultaneously", func() {
			const numReplicas = 15

			// barrier releases all 15 Upload calls at once — every replica is a loser
			// except one, maximising retry pressure on a single shared file.
			bs := metadata.NewBarrierStorage(storage, numReplicas)
			replicas := make([]receivedsharecache.Cache, numReplicas)
			for i := range replicas {
				replicas[i] = receivedsharecache.New(bs, 0*time.Second)
			}

			errs := make([]error, numReplicas)
			var wg sync.WaitGroup
			for i := 0; i < numReplicas; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					rs := &collaboration.ReceivedShare{
						Share: &collaboration.Share{
							Id: &collaboration.ShareId{OpaqueId: "share-0"},
						},
						State: collaboration.ShareState_SHARE_STATE_PENDING,
					}
					errs[idx] = replicas[idx].Add(ctx, userID, spaceID, rs)
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				Expect(err).ToNot(HaveOccurred(), "replica %d failed", i)
			}

			fresh := receivedsharecache.New(storage, 0*time.Second)
			spaces, err := fresh.List(ctx, userID)
			Expect(err).ToNot(HaveOccurred())
			Expect(spaces[spaceID]).ToNot(BeNil())
			Expect(spaces[spaceID].States).To(HaveKey("share-0"))
		})

		It("preserves every share when 40 replicas write concurrently through the real disk lock", func() {
			const numReplicas = 40

			replicas := make([]receivedsharecache.Cache, numReplicas)
			for i := range replicas {
				replicas[i] = receivedsharecache.New(storage, 0*time.Second)
			}

			errs := make([]error, numReplicas)
			var wg sync.WaitGroup
			for i := 0; i < numReplicas; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					rs := &collaboration.ReceivedShare{
						Share: &collaboration.Share{
							Id: &collaboration.ShareId{OpaqueId: fmt.Sprintf("share-%d", idx)},
						},
						State: collaboration.ShareState_SHARE_STATE_PENDING,
					}
					errs[idx] = replicas[idx].Add(ctx, userID, spaceID, rs)
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				Expect(err).ToNot(HaveOccurred(), "replica %d failed", i)
			}

			fresh := receivedsharecache.New(storage, 0*time.Second)
			spaces, err := fresh.List(ctx, userID)
			Expect(err).ToNot(HaveOccurred())
			for i := 0; i < numReplicas; i++ {
				Expect(spaces[spaceID].States).To(HaveKey(fmt.Sprintf("share-%d", i)), "missing share-%d", i)
			}
		})
	})

	Describe("retryPersist's post-failure resync", func() {
		It("does not perform a redundant bootstrap upload when no file exists yet", func() {
			fs := &errOnceUploadStorage{Storage: storage, err: errtypes.Aborted("injected")}
			c2 := receivedsharecache.New(fs, 0*time.Second)

			err := c2.Remove(ctx, userID, spaceID, shareID)
			Expect(err).ToNot(HaveOccurred())
			// 1 forced failure + 1 real write; no extra bootstrap upload from the resync in between.
			Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
		})
	})

	Describe("with an existing entry", func() {
		BeforeEach(func() {
			rs := &collaboration.ReceivedShare{
				Share: share,
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			Expect(c.Add(ctx, userID, spaceID, rs)).To(Succeed())
		})

		Describe("Get", func() {
			It("handles unknown users", func() {
				s, err := c.Get(ctx, "something", spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("handles unknown spaces", func() {
				s, err := c.Get(ctx, userID, "something", shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("handles unknown shares", func() {
				s, err := c.Get(ctx, userID, spaceID, "something")
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("gets the entry", func() {
				s, err := c.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).ToNot(BeNil())
			})

			It("retries a raw gRPC transient error on the resync path like it does on persist", func() {
				fs := &errOnceDownloadStorage{Storage: storage, err: status.Error(codes.Unavailable, "backend temporarily unavailable")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				_, err := c2.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.downloads)).To(Equal(int32(2)))
			})

			It("fails fast on a non-transient Download error instead of retrying", func() {
				fs := &errOnceDownloadStorage{Storage: storage, err: errtypes.PermissionDenied("injected")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				_, err := c2.Get(ctx, userID, spaceID, shareID)
				Expect(err).To(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.downloads)).To(Equal(int32(1)))
			})

			It("returns without erroring when the server reports NotModified", func() {
				spy := &downloadSpyStorage{Storage: storage}
				c2 := receivedsharecache.New(spy, 0*time.Second)

				_, err := c2.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spy.notModifiedSeen).To(BeFalse(), "first read has no etag yet, can't be NotModified")

				_, err = c2.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spy.notModifiedSeen).To(BeTrue())
			})
		})

		Describe("Remove", func() {
			It("removes the entry", func() {
				err := c.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())

				s, err := c.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("persists the removal", func() {
				err := c.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())

				c = receivedsharecache.New(storage, 0*time.Second)
				s, err := c.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("recovers when the backing file was deleted externally (space reprovisioned)", func() {
				// c already holds stale state from the BeforeEach Add.
				// Simulate an admin/backup wiping received.json out from under it.
				Expect(os.Remove(filepath.Join(tmpdir, "users", userID, "received.json"))).To(Succeed())

				err := c.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred(), "sync's NotFound must discard the stale snapshot so persist can bootstrap-recreate the file")
			})

			It("does not resurrect other shares that existed before the backing file was deleted", func() {
				other := &collaboration.ReceivedShare{
					Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "other-share"}},
					State: collaboration.ShareState_SHARE_STATE_PENDING,
				}
				Expect(c.Add(ctx, userID, spaceID, other)).To(Succeed())

				Expect(os.Remove(filepath.Join(tmpdir, "users", userID, "received.json"))).To(Succeed())

				Expect(c.Remove(ctx, userID, spaceID, shareID)).To(Succeed())

				fresh := receivedsharecache.New(storage, 0*time.Second)
				spaces, err := fresh.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				if spaces[spaceID] != nil {
					Expect(spaces[spaceID].States).ToNot(HaveKey("other-share"),
						"stale pre-wipe share resurrected after the backing file was externally deleted")
				}
			})

			It("keeps List/Get usable on the same instance right after the backing file is found missing", func() {
				Expect(os.Remove(filepath.Join(tmpdir, "users", userID, "received.json"))).To(Succeed())

				spaces, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())

				s, err := c.Get(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(s).To(BeNil())
			})

			It("returns context.Canceled immediately when ctx is already canceled", func() {
				as := &alwaysFailUploadStorage{Storage: storage, err: errtypes.PreconditionFailed("injected")}
				c2 := receivedsharecache.New(as, 0*time.Second)

				canceled, cancel := context.WithCancel(ctx)
				cancel()

				err := c2.Remove(canceled, userID, spaceID, shareID)
				Expect(err).To(MatchError(context.Canceled))
				Expect(atomic.LoadInt32(&as.uploads)).To(Equal(int32(0)))
			})

			It("exits the backoff sleep when ctx is canceled", func() {
				as := &alwaysFailUploadStorage{Storage: storage, err: errtypes.PreconditionFailed("injected")}
				c2 := receivedsharecache.New(as, 0*time.Second)

				ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()

				start := time.Now()
				_ = c2.Remove(ctx2, userID, spaceID, shareID)
				Expect(time.Since(start)).To(BeNumerically("<", 200*time.Millisecond))
			})

			It("returns an error when the retry budget is exhausted, never a false success", func() {
				as := &alwaysFailUploadStorage{Storage: storage, err: errtypes.PreconditionFailed("injected")}
				c2 := receivedsharecache.New(as, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).To(HaveOccurred(), "persist never succeeded; retryPersist must not report success")
			})

			It("fails fast on a permanent InternalError instead of burning the retry budget", func() {
				// errtypes.InternalError is NewErrtypeFromHTTPStatusCode's catch-all
				// (errtypes.go default arm) for e.g. 401/500/502 -- permanent failures,
				// not just disk.go's transient flock contention.
				as := &alwaysFailUploadStorage{Storage: storage, err: errtypes.InternalError("http 401: unauthorized")}
				c2 := receivedsharecache.New(as, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).To(HaveOccurred())
				Expect(atomic.LoadInt32(&as.uploads)).To(Equal(int32(1)), "a permanent error must not be retried")
			})

			It("succeeds within budget when contention needs more than 10 real persist attempts", func() {
				fs := &flakyAbortedStorage{Storage: storage, failures: 14}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred(), "retryPersist gave up before exhausting a reasonable persist-attempt budget")
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(15)))
			})

			It("retries a raw gRPC Unavailable error the way CS3's Upload actually returns it", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: status.Error(codes.Unavailable, "backend temporarily unavailable")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred(), "a transient gRPC transport error must be retried, not treated as fatal")
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("retries an AlreadyExists CAS conflict on persist like other transient storage errors", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: errtypes.AlreadyExists("injected")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("retries a TooEarly CAS conflict on persist like other transient storage errors", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: errtypes.TooEarly("injected")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("retries a raw gRPC DeadlineExceeded error on persist", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: status.Error(codes.DeadlineExceeded, "deadline exceeded")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("retries a raw gRPC Canceled error on persist", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: status.Error(codes.Canceled, "canceled")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("retries a raw gRPC ResourceExhausted error on persist", func() {
				fs := &errOnceUploadStorage{Storage: storage, err: status.Error(codes.ResourceExhausted, "resource exhausted")}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
			})

			It("does not reuse stale state when the post-failure resync itself fails transiently", func() {
				fs := &flakyDownloadStorage{Storage: storage, downloadFailures: 2}
				c2 := receivedsharecache.New(fs, 0*time.Second)

				err := c2.Remove(ctx, userID, spaceID, shareID)
				Expect(err).ToNot(HaveOccurred())

				// exactly one upload before the resync, one after it recovers — never
				// interleaved with the still-failing downloads, which is what the bug did
				Expect(fs.calls).To(Equal([]string{"upload", "download", "download", "download", "upload"}))
				Expect(atomic.LoadInt32(&fs.uploads)).To(Equal(int32(2)))
				Expect(atomic.LoadInt32(&fs.downloads)).To(Equal(int32(3)))
			})
		})
	})
})

// alwaysFailUploadStorage fails every Upload with a configured error, never delegates.
type alwaysFailUploadStorage struct {
	metadata.Storage
	err     error
	uploads int32
}

func (a *alwaysFailUploadStorage) Upload(_ context.Context, _ metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&a.uploads, 1)
	return nil, a.err
}

// flakyAbortedStorage fails Upload with a CAS conflict N times, then delegates.
type flakyAbortedStorage struct {
	metadata.Storage
	failures int32 // remaining failures before success
	uploads  int32
}

func (a *flakyAbortedStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&a.uploads, 1)
	if atomic.AddInt32(&a.failures, -1) >= 0 {
		return nil, errtypes.Aborted("injected")
	}
	return a.Storage.Upload(ctx, req)
}

// flakyTooEarlyDownloadStorage fails Download with TooEarly N times, then delegates.
type flakyTooEarlyDownloadStorage struct {
	metadata.Storage
	failures int32 // remaining transient failures before delegating
}

func (f *flakyTooEarlyDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if atomic.AddInt32(&f.failures, -1) >= 0 {
		return nil, errtypes.TooEarly("injected")
	}
	return f.Storage.Download(ctx, req)
}

// errOnceUploadStorage fails Upload once with a configured error, then delegates.
type errOnceUploadStorage struct {
	metadata.Storage
	err     error
	uploads int32
}

func (e *errOnceUploadStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if atomic.AddInt32(&e.uploads, 1) == 1 {
		return nil, e.err
	}
	return e.Storage.Upload(ctx, req)
}

// errOnceDownloadStorage fails Download once with a configured error, then delegates.
type errOnceDownloadStorage struct {
	metadata.Storage
	err       error
	downloads int32
}

func (e *errOnceDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if atomic.AddInt32(&e.downloads, 1) == 1 {
		return nil, e.err
	}
	return e.Storage.Download(ctx, req)
}

// downloadSpyStorage records whether the server ever answered NotModified.
type downloadSpyStorage struct {
	metadata.Storage
	notModifiedSeen bool
}

func (s *downloadSpyStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	res, err := s.Storage.Download(ctx, req)
	if _, ok := err.(errtypes.NotModified); ok {
		s.notModifiedSeen = true
	}
	return res, err
}

// flakyDownloadStorage fails the first Upload with a CAS conflict (to enter
// retryPersist's post-failure resync path), then fails the subsequent Download
// calls with InternalError downloadFailures times before delegating. It records
// the call sequence so a test can prove persistFunc/Upload is never re-invoked
// with stale state while the resync is still failing transiently.
type flakyDownloadStorage struct {
	metadata.Storage

	downloadFailures int32 // remaining transient Download failures before success
	uploads          int32
	downloads        int32

	mu    sync.Mutex
	calls []string // "upload" / "download" in call order
}

func (f *flakyDownloadStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "upload")
	f.mu.Unlock()
	if atomic.AddInt32(&f.uploads, 1) == 1 {
		return nil, errtypes.Aborted("injected")
	}
	return f.Storage.Upload(ctx, req)
}

func (f *flakyDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "download")
	f.mu.Unlock()
	atomic.AddInt32(&f.downloads, 1)
	if atomic.AddInt32(&f.downloadFailures, -1) >= 0 {
		return nil, errtypes.InternalError("injected")
	}
	return f.Storage.Download(ctx, req)
}
