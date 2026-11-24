// Copyright 2018-2021 CERN
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

package metadata_test

import (
	"context"
	"os"
	"path"

	"github.com/owncloud/reva/v2/pkg/storage/cache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Backend", func() {
	var (
		tmpdir string
		file   string

		backend metadata.Backend
	)

	BeforeEach(func() {
		var err error
		tmpdir, err = os.MkdirTemp(os.TempDir(), "XattrsBackendTest-")
		Expect(err).ToNot(HaveOccurred())
	})

	JustBeforeEach(func() {
		file = path.Join(tmpdir, "file")
	})

	AfterEach(func() {
		if tmpdir != "" {
			os.RemoveAll(tmpdir)
		}
	})

	Describe("XattrsBackend", func() {
		BeforeEach(func() {
			backend = metadata.NewXattrsBackend(tmpdir, cache.Config{
				Database: tmpdir,
			})
		})

		Describe("Purge", func() {
			JustBeforeEach(func() {
				Expect(os.WriteFile(file, []byte{}, 0600)).To(Succeed())
				err := backend.SetMultiple(context.Background(), file, map[string][]byte{"user.ocis.foo": []byte("bar")}, true)
				Expect(err).ToNot(HaveOccurred())
				// an upload to an existing node keeps the lock file
				Expect(os.WriteFile(backend.LockfilePath(file), []byte{}, 0600)).To(Succeed())

				Expect(backend.Purge(context.Background(), file)).To(Succeed())
			})

			It("removes the attributes", func() {
				_, err := backend.Get(context.Background(), file, "user.ocis.foo")
				Expect(err).To(HaveOccurred())
			})

			It("removes the lock file", func() {
				Expect(backend.LockfilePath(file)).ToNot(BeAnExistingFile())
			})
		})
	})

	Describe("MessagePackBackend", func() {
		BeforeEach(func() {
			backend = metadata.NewMessagePackBackend(tmpdir, cache.Config{
				Database: tmpdir,
			})
		})

		Describe("Set", func() {
			It("sets an attribute", func() {
				data := []byte(`bar\`)
				err := backend.Set(context.Background(), file, "foo", data)
				Expect(err).ToNot(HaveOccurred())

				readData, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(readData).To(Equal(data))
			})

			It("handles funny strings", func() {
				data := []byte(`bar\`)
				err := backend.Set(context.Background(), file, "foo", data)
				Expect(err).ToNot(HaveOccurred())

				readData, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(readData).To(Equal(data))
			})

			It("updates an attribute", func() {
				err := backend.Set(context.Background(), file, "foo", []byte("bar"))
				Expect(err).ToNot(HaveOccurred())
				err = backend.Set(context.Background(), file, "foo", []byte("baz"))
				Expect(err).ToNot(HaveOccurred())

				readData, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(readData).To(Equal([]byte("baz")))
			})

			It("sets an empty attribute", func() {
				_, err := backend.Get(context.Background(), file, "foo")
				Expect(err).To(HaveOccurred())

				err = backend.Set(context.Background(), file, "foo", []byte{})
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(v).To(Equal([]byte{}))
			})
		})

		Describe("SetMultiple", func() {
			It("sets attributes", func() {
				data := map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				readData, err := backend.All(context.Background(), file)
				Expect(err).ToNot(HaveOccurred())
				Expect(readData).To(Equal(data))
			})

			It("updates an attribute", func() {
				err := backend.Set(context.Background(), file, "foo", []byte("something"))

				data := map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}
				Expect(err).ToNot(HaveOccurred())
				err = backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				readData, err := backend.All(context.Background(), file)
				Expect(err).ToNot(HaveOccurred())
				Expect(readData).To(Equal(data))
			})
		})

		Describe("All", func() {
			It("returns the entries", func() {
				data := map[string][]byte{"foo": []byte("123"), "bar": []byte("baz")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.All(context.Background(), file)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(v)).To(Equal(2))
				Expect(v["foo"]).To(Equal([]byte("123")))
				Expect(v["bar"]).To(Equal([]byte("baz")))
			})

			It("fails when the metafile does not exist", func() {
				_, err := backend.All(context.Background(), file)
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("List", func() {
			It("returns the entries", func() {
				data := map[string][]byte{"foo": []byte("123"), "bar": []byte("baz")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.List(context.Background(), file)
				Expect(err).ToNot(HaveOccurred())
				Expect(v).To(ConsistOf("foo", "bar"))
			})

			It("fails when the metafile does not exist", func() {
				_, err := backend.List(context.Background(), file)
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("Get", func() {
			It("returns the attribute", func() {
				data := map[string][]byte{"foo": []byte("bar")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(v).To(Equal([]byte("bar")))
			})

			It("returns an error on unknown attributes", func() {
				_, err := backend.Get(context.Background(), file, "foo")
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("GetInt64", func() {
			It("returns the attribute", func() {
				data := map[string][]byte{"foo": []byte("123")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.GetInt64(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(v).To(Equal(int64(123)))
			})

			It("returns an error on unknown attributes", func() {
				_, err := backend.GetInt64(context.Background(), file, "foo")
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("Remove", func() {
			It("deletes an attribute", func() {
				data := map[string][]byte{"foo": []byte("bar")}
				err := backend.SetMultiple(context.Background(), file, data, true)
				Expect(err).ToNot(HaveOccurred())

				v, err := backend.Get(context.Background(), file, "foo")
				Expect(err).ToNot(HaveOccurred())
				Expect(v).To(Equal([]byte("bar")))

				err = backend.Remove(context.Background(), file, "foo", true)
				Expect(err).ToNot(HaveOccurred())

				_, err = backend.Get(context.Background(), file, "foo")
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("Purge", func() {
			JustBeforeEach(func() {
				// a locked write creates the metadata file and the lock file
				err := backend.SetMultiple(context.Background(), file, map[string][]byte{"foo": []byte("bar")}, true)
				Expect(err).ToNot(HaveOccurred())
				Expect(backend.MetadataPath(file)).To(BeAnExistingFile())
				Expect(backend.LockfilePath(file)).To(BeAnExistingFile())

				Expect(backend.Purge(context.Background(), file)).To(Succeed())
			})

			It("removes the metadata file", func() {
				Expect(backend.MetadataPath(file)).ToNot(BeAnExistingFile())
			})

			It("removes the lock file", func() {
				Expect(backend.LockfilePath(file)).ToNot(BeAnExistingFile())
			})
		})

		Describe("IsMetaFile", func() {
			It("returns true", func() {
				Expect(backend.IsMetaFile("foo.mpk")).To(BeTrue())
			})

			It("returns false", func() {
				Expect(backend.IsMetaFile("foo.txt")).To(BeFalse())
			})
		})
	})
})
