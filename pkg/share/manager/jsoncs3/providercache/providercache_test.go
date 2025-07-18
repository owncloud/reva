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
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
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
		})
	})
})
