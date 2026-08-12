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
	"os"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/receivedsharecache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

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
		})
	})
})

// barrierStorage wraps a Storage and holds Upload calls until n goroutines have
// arrived, then releases them all at once. This makes the concurrent-write race
// reproducible regardless of OS goroutine scheduling.
// mu serializes Upload/Download pairs because DiskStorage.Upload is not atomic
// on this branch (os.WriteFile, not renameio) — without it a concurrent Download
// can read a partial file and get a json.SyntaxError.
type barrierStorage struct {
	metadata.Storage
	mu        sync.Mutex
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
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Storage.Upload(ctx, req)
}

func (b *barrierStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Storage.Download(ctx, req)
}

type alwaysFailStorage struct {
	metadata.Storage
	uploads int32
}

func (a *alwaysFailStorage) Upload(_ context.Context, _ metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&a.uploads, 1)
	return nil, errtypes.PreconditionFailed("injected")
}
