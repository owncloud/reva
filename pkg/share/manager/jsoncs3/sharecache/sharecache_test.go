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
	"encoding/json"
	"os"
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
			Expect(c.Add(ctx, userid, shareID)).To(Succeed())

			fs := &helpers.ErrOnceUploadStorage{Storage: storage, Err: errtypes.TooEarly("injected")}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

			err := c2.Add(ctx, userid, "storageid$spaceid!share2")
			Expect(err).ToNot(HaveOccurred(), "TooEarly from write-lock contention should be retried, not treated as fatal")
			Expect(atomic.LoadInt32(&fs.Uploads)).To(Equal(int32(2)))
		})

		It("fails instead of dropping other shares when the post-conflict resync finds the file gone", func() {
			initial := sharecache.UserShareCache{UserShares: map[string]*sharecache.SpaceShareIDs{
				"storageid$spaceid1": {IDs: map[string]struct{}{shareID: {}}},
				"storageid$spaceid2": {IDs: map[string]struct{}{"storageid$spaceid2!share2": {}}},
			}}
			initialBytes, err := json.Marshal(initial)
			Expect(err).ToNot(HaveOccurred())

			fs := &helpers.ConflictThenNotFoundStorage{InitialData: initialBytes, InitialEtag: "etag1"}
			c2 := sharecache.New(fs, "users", "created.json", 0*time.Second)

			addErr := c2.Add(ctx, userid, "storageid$spaceid3!share3")
			Expect(addErr).To(HaveOccurred(), "a backing file that vanished mid-retry must surface as an error, not a partial write")
			Expect(fs.UploadedContent()).To(BeNil(), "no write should ever reach the storage once the resync reports NotFound")
		})
	})

	Describe("List", func() {
		Context("when no cache file exists yet", func() {
			It("creates the cache file on first call", func() {
				_, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				_, err = os.Stat(tmpdir + "/users/" + userid + "/created.json")
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets an etag after the first call", func() {
				_, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				uc, ok := c.UserShares.Load(userid)
				Expect(ok).To(BeTrue())
				Expect(uc.Etag).ToNot(BeEmpty())
			})

			It("returns empty shares", func() {
				shares, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())
				Expect(shares).To(BeEmpty())
			})

			It("does not upload again on second call", func() {
				_, err := c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				uc, _ := c.UserShares.Load(userid)
				etagAfterFirst := uc.Etag

				_, err = c.List(ctx, userid)
				Expect(err).ToNot(HaveOccurred())

				uc, _ = c.UserShares.Load(userid)
				Expect(uc.Etag).To(Equal(etagAfterFirst))
			})
		})
	})
})

