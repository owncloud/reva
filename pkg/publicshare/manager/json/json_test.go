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

package json_test

import (
	"context"
	encjson "encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	link "github.com/cs3org/go-cs3apis/cs3/sharing/link/v1beta1"
	providerv1beta1 "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/publicshare"
	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json"
	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json/persistence"
	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json/persistence/cs3"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// slowReadPersistence is a fake persistence.Persistence whose Read blocks
// for a fixed delay, standing in for a network round trip such as the cs3
// persistence layer's Stat/SimpleDownload against metadata.CS3. It has no
// state of its own to protect - it exists to let a test observe whether
// concurrent manager calls overlap during that delay.
type slowReadPersistence struct {
	delay time.Duration
}

func (p *slowReadPersistence) Init(_ context.Context) error { return nil }

func (p *slowReadPersistence) Read(_ context.Context) (persistence.PublicShares, error) {
	time.Sleep(p.delay)
	return persistence.PublicShares{}, nil
}

func (p *slowReadPersistence) Write(_ context.Context, _ persistence.PublicShares) error {
	return nil
}

var _ = Describe("Json", func() {
	var (
		user1 = &userpb.User{
			Id: &userpb.UserId{
				Idp:      "https://localhost:9200",
				OpaqueId: "admin",
			},
		}

		sharedResource = &providerv1beta1.ResourceInfo{
			Id: &providerv1beta1.ResourceId{
				StorageId: "storageid",
				OpaqueId:  "opaqueid",
			},
			ArbitraryMetadata: &providerv1beta1.ArbitraryMetadata{
				Metadata: map[string]string{
					"name": "publicshare",
				},
			},
		}
		grant = &link.Grant{
			Permissions: &link.PublicSharePermissions{
				Permissions: &providerv1beta1.ResourcePermissions{
					InitiateFileUpload: false,
				},
			},
		}

		m       publicshare.Manager
		tmpFile *os.File
		ctx     context.Context
	)

	Context("with a file persistence layer", func() {

		BeforeEach(func() {
			var err error
			tmpFile, err = os.CreateTemp("", "reva-unit-test-*.json")
			Expect(err).ToNot(HaveOccurred())

			config := map[string]interface{}{
				"file":         tmpFile.Name(),
				"gateway_addr": "https://localhost:9200",
			}
			m, err = json.NewFile(config)
			Expect(err).ToNot(HaveOccurred())

			ctx = ctxpkg.ContextSetUser(context.Background(), user1)
		})

		AfterEach(func() {
			os.Remove(tmpFile.Name())
		})

		Describe("Dump", func() {
			JustBeforeEach(func() {
				_, err := m.CreatePublicShare(ctx, user1, sharedResource, &link.Grant{
					Password: "foo",
				})
				Expect(err).ToNot(HaveOccurred())
			})

			It("dumps all public shares", func() {
				psharesChan := make(chan *publicshare.WithPassword)
				pshares := []*publicshare.WithPassword{}

				wg := sync.WaitGroup{}
				wg.Add(1)
				go func() {
					for ps := range psharesChan {
						if ps != nil {
							pshares = append(pshares, ps)
						}
					}
					wg.Done()
				}()
				err := m.(publicshare.DumpableManager).Dump(ctx, psharesChan)
				Expect(err).ToNot(HaveOccurred())
				close(psharesChan)
				wg.Wait()
				Eventually(psharesChan).Should(BeClosed())

				Expect(len(pshares)).To(Equal(1))
				Expect(bcrypt.CompareHashAndPassword([]byte(pshares[0].Password), []byte("foo"))).To(Succeed())
				Expect(pshares[0].PublicShare.Creator).To(BeComparableTo(user1.Id, protocmp.Transform()))
				Expect(pshares[0].PublicShare.ResourceId).To(BeComparableTo(sharedResource.Id, protocmp.Transform()))
			})
		})

		Describe("ListPublicShares", func() {
			It("skips shares whose persisted resource_id is nil instead of panicking", func() {
				// Create one valid share so the manager has a healthy row to compare against.
				validShare, err := m.CreatePublicShare(ctx, user1, sharedResource, &link.Grant{
					Permissions: &link.PublicSharePermissions{
						Permissions: &providerv1beta1.ResourcePermissions{},
					},
				})
				Expect(err).ToNot(HaveOccurred())

				// Inject a corrupt row directly into the persistence file: the share's stored
				// JSON has no `resource_id`, so after unmarshal `local.ResourceId` is nil.
				// This mirrors the production state described in OCISDEV-862.
				raw, err := os.ReadFile(tmpFile.Name())
				Expect(err).ToNot(HaveOccurred())

				db := map[string]interface{}{}
				Expect(encjson.Unmarshal(raw, &db)).To(Succeed())

				db["corrupt-share-id"] = map[string]interface{}{
					"share":    `{"id":{"opaque_id":"corrupt-share-id"},"token":"corrupt-token"}`,
					"password": "",
				}
				patched, err := encjson.Marshal(db)
				Expect(err).ToNot(HaveOccurred())
				Expect(os.WriteFile(tmpFile.Name(), patched, 0644)).To(Succeed())

				// Listing must not panic and must return the valid share.
				shares, err := m.ListPublicShares(ctx, user1, []*link.ListPublicSharesRequest_Filter{}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(shares)).To(Equal(1))
				Expect(shares[0].Id.OpaqueId).To(Equal(validShare.Id.OpaqueId))
			})
		})

		Describe("Load", func() {
			It("loads shares including state and mountpoint information", func() {
				existingShare, err := m.CreatePublicShare(ctx, user1, sharedResource, &link.Grant{
					Password: "foo",
				})
				Expect(err).ToNot(HaveOccurred())

				targetManager, err := json.NewMemory(map[string]interface{}{})
				Expect(err).ToNot(HaveOccurred())

				sharesChan := make(chan *publicshare.WithPassword)

				wg := sync.WaitGroup{}
				wg.Add(2)
				go func() {
					err := targetManager.(publicshare.LoadableManager).Load(ctx, sharesChan)
					Expect(err).ToNot(HaveOccurred())
					wg.Done()
				}()
				go func() {
					tmpShare := &publicshare.WithPassword{
						Password: "foo",
					}
					proto.Merge(&tmpShare.PublicShare, existingShare)
					sharesChan <- tmpShare
					close(sharesChan)
					wg.Done()
				}()
				wg.Wait()
				Eventually(sharesChan).Should(BeClosed())

				loadedPublicShare, err := targetManager.GetPublicShare(ctx, user1, &link.PublicShareReference{
					Spec: &link.PublicShareReference_Token{
						Token: existingShare.Token,
					},
				}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(loadedPublicShare).ToNot(BeNil())
			})
		})
	})

	Context("with a cs3 persistence layer", func() {
		var (
			tmpdir string

			storage metadata.Storage
		)

		BeforeEach(func() {
			var err error
			tmpdir, err = os.MkdirTemp("", "json-publicshare-manager-test")
			Expect(err).ToNot(HaveOccurred())

			err = os.MkdirAll(tmpdir, 0755)
			Expect(err).ToNot(HaveOccurred())

			storage, err = metadata.NewDiskStorage(tmpdir)
			Expect(err).ToNot(HaveOccurred())

			persistence := cs3.New(storage)
			Expect(persistence.Init(context.Background())).To(Succeed())

			m, err = json.New("https://localhost:9200", 11, 60, false, persistence)
			Expect(err).ToNot(HaveOccurred())

			ctx = ctxpkg.ContextSetUser(context.Background(), user1)
		})

		AfterEach(func() {
			if tmpdir != "" {
				os.RemoveAll(tmpdir)
			}
		})
		Describe("CreatePublicShare", func() {
			It("creates public shares", func() {
				ps, err := m.CreatePublicShare(ctx, user1, sharedResource, grant)
				Expect(err).ToNot(HaveOccurred())
				Expect(ps).ToNot(BeNil())
			})
		})

		Describe("PublicShares", func() {
			It("lists public shares", func() {
				_, err := m.CreatePublicShare(ctx, user1, sharedResource, grant)
				Expect(err).ToNot(HaveOccurred())

				ps, err := m.ListPublicShares(ctx, user1, []*link.ListPublicSharesRequest_Filter{}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(ps)).To(Equal(1))
				Expect(ps[0].ResourceId).To(Equal(sharedResource.Id))
			})

			It("picks up shares from the storage", func() {
				_, err := m.CreatePublicShare(ctx, user1, sharedResource, grant)
				Expect(err).ToNot(HaveOccurred())

				// Reset manager
				p := cs3.New(storage)
				Expect(p.Init(context.Background())).To(Succeed())

				m, err = json.New("https://localhost:9200", 11, 60, false, p)
				Expect(err).ToNot(HaveOccurred())

				ps, err := m.ListPublicShares(ctx, user1, []*link.ListPublicSharesRequest_Filter{}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(ps)).To(Equal(1))
				Expect(ps[0].ResourceId).To(Equal(sharedResource.Id))
			})

			It("serves concurrent readers and writers without racing", func() {
				// This doesn't assert much beyond "no error" - its real job is to
				// give `go test -race` enough concurrent traffic through the
				// manager's RWMutex and the cs3 persistence's own internal mutex
				// to catch a regression of either lock being removed or a
				// persistence Read() result being aliased across goroutines.
				const writers = 4
				const readers = 4
				const perWorker = 25

				var wg sync.WaitGroup
				createErrs := make(chan error, writers*perWorker)

				wg.Add(writers)
				for range writers {
					go func() {
						defer wg.Done()
						for range perWorker {
							if _, err := m.CreatePublicShare(ctx, user1, sharedResource, grant); err != nil {
								createErrs <- err
							}
						}
					}()
				}

				wg.Add(readers)
				for range readers {
					go func() {
						defer wg.Done()
						missingRef := &link.PublicShareReference{
							Spec: &link.PublicShareReference_Id{Id: &link.PublicShareId{OpaqueId: "missing"}},
						}
						for range perWorker {
							_, _ = m.ListPublicShares(ctx, user1, nil, false)
							_, _ = m.GetPublicShare(ctx, user1, missingRef, false)
							_, _ = m.GetPublicShareByToken(ctx, "missing-token", nil, false)

							sharesChan := make(chan *publicshare.WithPassword)
							go func() {
								for range sharesChan {
								}
							}()
							_ = m.(publicshare.DumpableManager).Dump(ctx, sharesChan)
							close(sharesChan)
						}
					}()
				}

				wg.Wait()
				close(createErrs)
				for err := range createErrs {
					Expect(err).ToNot(HaveOccurred())
				}
			})

			It("overlaps concurrent ListPublicShares calls instead of queueing them", func() {
				// Regression test for manager.init (json.go) taking the manager's
				// write lock on every call ahead of ListPublicShares' own RLock.
				// Because a pending sync.RWMutex writer blocks new readers, that
				// turned every concurrent ListPublicShares call into a queue behind
				// whichever call was already inside persistence.Read - silently
				// undoing the switch from sync.Mutex to sync.RWMutex. A wrong
				// re-introduction of that lock wouldn't fail -race (it's a
				// correctly-used lock), only show up as this test timing out.
				const (
					delay       = 150 * time.Millisecond
					concurrency = 8
				)

				slow, err := json.New("https://localhost:9200", 11, 60, false, &slowReadPersistence{delay: delay})
				Expect(err).ToNot(HaveOccurred())

				var wg sync.WaitGroup
				start := time.Now()
				wg.Add(concurrency)
				for range concurrency {
					go func() {
						defer wg.Done()
						_, _ = slow.ListPublicShares(ctx, user1, nil, false)
					}()
				}
				wg.Wait()

				// Fully serialized would take concurrency*delay (1.2s here).
				// Overlapping reads should finish close to a single delay - allow
				// generous slack for scheduling noise without letting a real
				// regression pass.
				Expect(time.Since(start)).To(BeNumerically("<", delay*3))
			})

			It("refreshes its cache before writing new data", func() {
				_, err := m.CreatePublicShare(ctx, user1, sharedResource, grant)
				Expect(err).ToNot(HaveOccurred())

				ps, err := m.ListPublicShares(ctx, user1, []*link.ListPublicSharesRequest_Filter{}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(ps)).To(Equal(1))

				// Purge file on storage and make sure its mtime is newer than the cache
				path := filepath.Join(tmpdir, "publicshares.json")
				Expect(os.WriteFile(path, []byte("{}"), 0x644)).To(Succeed())
				t := time.Now().Add(5 * time.Minute)
				Expect(os.Chtimes(path, t, t)).To(Succeed())

				_, err = m.CreatePublicShare(ctx, user1, sharedResource, grant)
				Expect(err).ToNot(HaveOccurred())

				ps, err = m.ListPublicShares(ctx, user1, []*link.ListPublicSharesRequest_Filter{}, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(len(ps)).To(Equal(1)) // Make sure the first created public share is gone
			})
		})
	})

	Context("with a memory persistence layer", func() {
		// Unlike cs3, the memory backend has no lock of its own - Read/Write
		// rely entirely on persistence.Copy plus the manager's own RWMutex
		// for safety. This is the test that would catch persistence.Copy
		// being dropped from memory.Read.
		BeforeEach(func() {
			var err error
			m, err = json.NewMemory(map[string]interface{}{})
			Expect(err).ToNot(HaveOccurred())

			ctx = ctxpkg.ContextSetUser(context.Background(), user1)
		})

		It("serves concurrent readers and writers without racing", func() {
			const writers = 4
			const readers = 4
			const perWorker = 25

			var wg sync.WaitGroup
			createErrs := make(chan error, writers*perWorker)

			wg.Add(writers)
			for range writers {
				go func() {
					defer wg.Done()
					for range perWorker {
						if _, err := m.CreatePublicShare(ctx, user1, sharedResource, grant); err != nil {
							createErrs <- err
						}
					}
				}()
			}

			wg.Add(readers)
			for range readers {
				go func() {
					defer wg.Done()
					missingRef := &link.PublicShareReference{
						Spec: &link.PublicShareReference_Id{Id: &link.PublicShareId{OpaqueId: "missing"}},
					}
					for range perWorker {
						_, _ = m.ListPublicShares(ctx, user1, nil, false)
						_, _ = m.GetPublicShare(ctx, user1, missingRef, false)
						_, _ = m.GetPublicShareByToken(ctx, "missing-token", nil, false)
					}
				}()
			}

			wg.Wait()
			close(createErrs)
			for err := range createErrs {
				Expect(err).ToNot(HaveOccurred())
			}
		})
	})
})
