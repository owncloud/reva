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
})

// countingStorage counts Upload calls, to prove a cold Remove doesn't waste one.
type countingStorage struct {
	metadata.Storage
	uploads int32
}

func (c *countingStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	atomic.AddInt32(&c.uploads, 1)
	return c.Storage.Upload(ctx, req)
}
