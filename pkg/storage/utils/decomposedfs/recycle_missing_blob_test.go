// Copyright 2018-2026 CERN
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

package decomposedfs_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/owncloud/reva/v2/pkg/storage/fs/s3ng/blobstore"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/node"
	helpers "github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/testhelpers"
	"github.com/stretchr/testify/mock"
)

// gcsLikeS3 is a minimal S3 endpoint that answers DELETE the way Google Cloud Storage's
// S3 interoperability API does: 204 for an existing key, 404 NoSuchKey for a missing one
// (AWS S3 answers 204 for both).
type gcsLikeS3 struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (s *gcsLikeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	w.Header().Set("Content-Type", "application/xml")
	switch r.Method {
	case http.MethodDelete:
		if !s.keys[key] {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		delete(s.keys, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

var _ = Describe("Purging a trashed folder on an S3 store that answers 404 for missing keys", func() {
	var (
		env    *helpers.TestEnv
		s3     *gcsLikeS3
		srv    *httptest.Server
		bs     *blobstore.Blobstore
		folder *node.Node
		files  []*node.Node
	)

	BeforeEach(func() {
		var err error
		env, err = helpers.NewTestEnv(nil)
		Expect(err).ToNot(HaveOccurred())
		registerPermissions(env.Permissions, "25b69780-5f39-43be-a7ac-a9b9e9fe4230", &provider.ResourcePermissions{
			CreateContainer:    true,
			Delete:             true,
			ListContainer:      true,
			ListRecycle:        true,
			PurgeRecycle:       true,
			RestoreRecycleItem: true,
			Stat:               true,
		})

		s3 = &gcsLikeS3{keys: map[string]bool{}}
		srv = httptest.NewServer(s3)
		bs, err = blobstore.New(srv.URL, "us-east-1", "bucket", "key", "secret", blobstore.Options{})
		Expect(err).ToNot(HaveOccurred())
		// route the tree's blob deletes through the real s3ng blobstore
		env.Blobstore.On("Delete", mock.Anything).Return(func(n *node.Node) error { return bs.Delete(n) })

		// /archive/<5 subfolders>/<4 files each>; every file has a blob in the store except one
		folder, err = env.CreateTestDir("/archive", &provider.Reference{ResourceId: env.SpaceRootRes})
		Expect(err).ToNot(HaveOccurred())
		files = nil
		for i := 0; i < 5; i++ {
			sub, err := env.CreateTestDir(fmt.Sprintf("/archive/sub%d", i), &provider.Reference{ResourceId: env.SpaceRootRes})
			Expect(err).ToNot(HaveOccurred())
			for j := 0; j < 4; j++ {
				f, err := env.CreateTestFile(fmt.Sprintf("file%d", j), fmt.Sprintf("0000%d%d00-0000-0000-0000-000000000000", i, j), sub.ID, sub.SpaceID, 1)
				Expect(err).ToNot(HaveOccurred())
				files = append(files, f)
				s3.keys[bs.Path(f)] = true
			}
		}
		// one blob went missing earlier (an interrupted upload, a lost revision, ...)
		delete(s3.keys, bs.Path(files[len(files)/2]))
	})

	AfterEach(func() {
		srv.Close()
		if env != nil {
			env.Cleanup()
		}
	})

	It("removes the whole folder and leaves nothing restorable behind", func() {
		_, err := env.Fs.Delete(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes, Path: "/archive"})
		Expect(err).ToNot(HaveOccurred())

		items, err := env.Fs.ListRecycle(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes}, "", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(items).To(HaveLen(1))

		err = env.Fs.PurgeRecycleItem(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes}, items[0].Key, "")
		Expect(err).ToNot(HaveOccurred())

		items, err = env.Fs.ListRecycle(env.Ctx, &provider.Reference{ResourceId: env.SpaceRootRes}, "", "")
		Expect(err).ToNot(HaveOccurred())
		Expect(items).To(BeEmpty(), "a failed purge leaves a hollow folder in the trash that can be restored")

		Expect(s3.keys).To(BeEmpty(), "every existing blob is deleted")
		for _, f := range files {
			_, err := os.Stat(f.InternalPath())
			Expect(os.IsNotExist(err)).To(BeTrue(), "node %s should be gone", filepath.Base(f.InternalPath()))
		}
		_, err = os.Stat(folder.InternalPath())
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})
