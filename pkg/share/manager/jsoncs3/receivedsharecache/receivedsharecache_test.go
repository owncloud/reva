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
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/receivedsharecache"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/rs/zerolog"

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
		zl := zerolog.New(os.Stdout).Level(zerolog.DebugLevel)
		ctx = appctx.WithLogger(context.Background(), &zl)

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
			It("creates the cache file on first call", func() {
				_, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())

				_, err = os.Stat(tmpdir + "/users/" + userID + "/received.json")
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns empty spaces", func() {
				spaces, err := c.List(ctx, userID)
				Expect(err).ToNot(HaveOccurred())
				Expect(spaces).To(BeEmpty())
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

		It("fails instead of dropping other shares when the post-conflict resync finds the file gone", func() {
			initial := struct {
				Spaces map[string]*receivedsharecache.Space
			}{
				Spaces: map[string]*receivedsharecache.Space{
					"spaceid1": {States: map[string]*receivedsharecache.State{"share1": {State: collaboration.ShareState_SHARE_STATE_PENDING}}},
					"spaceid2": {States: map[string]*receivedsharecache.State{"share2": {State: collaboration.ShareState_SHARE_STATE_PENDING}}},
				},
			}
			initialBytes, err := json.Marshal(initial)
			Expect(err).ToNot(HaveOccurred())

			fs := &helpers.ConflictThenNotFoundStorage{InitialData: initialBytes, InitialEtag: "etag1"}
			c2 := receivedsharecache.New(fs, 0*time.Second)

			rs := &collaboration.ReceivedShare{
				Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share3"}},
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			addErr := c2.Add(ctx, userID, "spaceid3", rs)
			Expect(addErr).To(HaveOccurred(), "a backing file that vanished mid-retry must surface as an error, not a partial write")
			Expect(fs.UploadedContent()).To(BeNil(), "no write should ever reach the storage once the resync reports NotFound")
		})

		It("does not retry the write with stale state while the post-conflict resync is still failing transiently", func() {
			fs := &casSimStorage{etag: "etag0", content: []byte(`{"Spaces":{}}`)}
			c2 := receivedsharecache.New(fs, 0*time.Second)

			// first Upload always loses the race; the resync right after it has
			// exactly one transient hiccup before it succeeds and reveals the new etag.
			fs.forceFirstUploadConflict = true
			fs.resyncFailuresBeforeSuccess = 1

			rs := &collaboration.ReceivedShare{
				Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share1"}},
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			Expect(c2.Add(ctx, userID, spaceID, rs)).To(Succeed())

			attempts := fs.uploadIfMatchEtags()
			Expect(attempts).To(Equal([]string{"etag0", "etag1"}),
				"a write attempt reusing the pre-resync etag means the loop retried the write "+
					"with stale state instead of waiting for the resync to actually refresh it: %v", attempts)
		})

		It("retries a transient gateway error during resync instead of failing fast", func() {
			fs := &casSimStorage{etag: "etag0", content: []byte(`{"Spaces":{}}`)}
			c2 := receivedsharecache.New(fs, 0*time.Second)

			fs.forceFirstUploadConflict = true
			fs.resyncFailuresBeforeSuccess = 1
			fs.transientResyncErr = errtypes.Unavailable("injected: gateway timeout")

			rs := &collaboration.ReceivedShare{
				Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share1"}},
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			Expect(c2.Add(ctx, userID, spaceID, rs)).To(Succeed(),
				"a 502/503/504-shaped Unavailable error during resync should be retried, not treated as permanent")
		})

		It("fails fast on a permanent InternalError during resync instead of burning the retry budget", func() {
			fs := &casSimStorage{etag: "etag0", content: []byte(`{"Spaces":{}}`)}
			c2 := receivedsharecache.New(fs, 0*time.Second)

			fs.forceFirstUploadConflict = true
			fs.resyncErr = errtypes.InternalError("permanent failure")

			rs := &collaboration.ReceivedShare{
				Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: "share1"}},
				State: collaboration.ShareState_SHARE_STATE_PENDING,
			}
			err := c2.Add(ctx, userID, spaceID, rs)
			Expect(err).To(HaveOccurred())
			Expect(fs.downloadCount()).To(BeNumerically("<", 5),
				"a permanent InternalError on resync should fail fast, not burn the retry budget: %d resync attempts", fs.downloadCount())
		})
	})

	Describe("concurrent writes from multiple cache instances", func() {
		It("preserves the share when 15 replicas write the same file simultaneously", func() {
			const numReplicas = 15

			// barrier releases all 15 Upload calls at once — every replica is a loser
			// except one, maximising retry pressure on a single shared file.
			bs := newBarrierStorage(storage, numReplicas)
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

			It("returns context.Canceled immediately when ctx is already canceled", func() {
				as := &alwaysFailStorage{Storage: storage}
				c2 := receivedsharecache.New(as, 0*time.Second)

				canceled, cancel := context.WithCancel(ctx)
				cancel()

				err := c2.Remove(canceled, userID, spaceID, shareID)
				Expect(err).To(MatchError(context.Canceled))
				Expect(atomic.LoadInt32(&as.uploads)).To(Equal(int32(0)))
			})

			It("exits the backoff sleep when ctx is canceled", func() {
				as := &alwaysFailStorage{Storage: storage}
				c2 := receivedsharecache.New(as, 0*time.Second)

				ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()

				start := time.Now()
				_ = c2.Remove(ctx2, userID, spaceID, shareID)
				Expect(time.Since(start)).To(BeNumerically("<", 200*time.Millisecond))
			})
		})
	})
})

// barrierStorage wraps a Storage and holds Upload calls until n goroutines have
// arrived, then releases them all at once. This makes the concurrent-write race
// reproducible regardless of OS goroutine scheduling.
type barrierStorage struct {
	metadata.Storage
	arrived   int32
	n         int32
	ready     chan struct{}
	closeOnce sync.Once
}

func newBarrierStorage(s metadata.Storage, n int) *barrierStorage {
	return &barrierStorage{Storage: s, n: int32(n), ready: make(chan struct{})}
}

func (b *barrierStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if atomic.AddInt32(&b.arrived, 1) >= b.n {
		b.closeOnce.Do(func() { close(b.ready) })
	}
	<-b.ready
	return b.Storage.Upload(ctx, req)
}

type alwaysFailStorage struct {
	metadata.Storage
	uploads int32
}

func (a *alwaysFailStorage) Upload(_ context.Context, _ metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&a.uploads, 1)
	return nil, errtypes.PreconditionFailed("injected")
}


// casSimStorage simulates CAS semantics in memory, with knobs to force a
// one-shot conflict and/or transient resync failures.
type casSimStorage struct {
	metadata.Storage
	mu sync.Mutex

	content []byte
	etag    string

	downloads                   int
	forceFirstUploadConflict    bool
	resyncFailuresBeforeSuccess int
	transientResyncErr          error // error to use for resyncFailuresBeforeSuccess; defaults to errtypes.TooEarly
	resyncErr                   error // when set, every Download after the first (cold-start) one fails with this error

	uploadEtags []string
}

func (s *casSimStorage) MakeDirIfNotExist(_ context.Context, _ string) error { return nil }

func (s *casSimStorage) Download(_ context.Context, _ metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloads++
	if s.downloads > 1 {
		if s.resyncErr != nil {
			return nil, s.resyncErr
		}
		if s.resyncFailuresBeforeSuccess > 0 {
			s.resyncFailuresBeforeSuccess--
			if s.transientResyncErr != nil {
				return nil, s.transientResyncErr
			}
			return nil, errtypes.TooEarly("injected: transient resync failure")
		}
	}
	return &metadata.DownloadResponse{Content: s.content, Etag: s.etag}, nil
}

func (s *casSimStorage) downloadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downloads
}

func (s *casSimStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadEtags = append(s.uploadEtags, req.IfMatchEtag)

	if s.forceFirstUploadConflict && len(s.uploadEtags) == 1 {
		s.forceFirstUploadConflict = false
		s.etag = "etag1" // an unseen writer's commit, landing in the gap before this Upload's response
		return nil, errtypes.Aborted("injected: lost the race against an unseen writer")
	}

	if req.IfMatchEtag != s.etag {
		return nil, errtypes.Aborted("injected: etag mismatch")
	}
	s.content = req.Content
	return &metadata.UploadResponse{Etag: s.etag}, nil
}

func (s *casSimStorage) uploadIfMatchEtags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.uploadEtags...)
}
