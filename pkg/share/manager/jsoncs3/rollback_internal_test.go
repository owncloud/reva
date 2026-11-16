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

package jsoncs3

// A failed Share must not leave state that blocks a new share or that
// creates a stale grantee entry. A listing must not remove valid group
// shares when another listing resets the provider cache at the same time.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

// cancelOnUploadStorage cancels the request context at the first upload to
// path and fails this upload with the context error. This is the same result
// as a gRPC deadline that expires during the write.
type cancelOnUploadStorage struct {
	metadata.Storage
	path   string
	cancel context.CancelFunc
	fired  int32
}

func (c *cancelOnUploadStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if req.Path == c.path && atomic.CompareAndSwapInt32(&c.fired, 0, 1) {
		c.cancel()
		return nil, context.Canceled
	}
	return c.Storage.Upload(ctx, req)
}

// notFoundEverySecondDownloadStorage returns NotFound for every second
// download of path. A temporary NotFound makes the provider cache reset the
// space to an empty share list.
type notFoundEverySecondDownloadStorage struct {
	metadata.Storage
	path      string
	downloads int32
}

func (n *notFoundEverySecondDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if req.Path == n.path && atomic.AddInt32(&n.downloads, 1)%2 == 0 {
		return nil, errtypes.NotFound("injected temporary not found for " + req.Path)
	}
	return n.Storage.Download(ctx, req)
}

func resourceInfo(n int) *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Id:    &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid", OpaqueId: fmt.Sprintf("file%d", n)},
		Owner: &userpb.UserId{OpaqueId: "owner"},
	}
}

func grant(grantee *provider.Grantee) *collaboration.ShareGrant {
	return &collaboration.ShareGrant{
		Grantee:     grantee,
		Permissions: &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true}},
	}
}

// Break: the rollback uses the cancelled request context, so the removal of
// the provider entry and of the created.json entry fails at once.
func TestShareRollsBackWhenTheRequestIsCancelledDuringTheGranteeWrite(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	ctx, cancel := context.WithCancel(ctxOf("admin"))
	defer cancel()
	cs := &cancelOnUploadStorage{Storage: f.storage, path: "/groups/group1/received.json", cancel: cancel}
	m, err := New(cs, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())

	_, err = m.Share(ctx, resourceInfo(0), grant(groupGrantee("group1")))
	g.Expect(err).To(HaveOccurred())

	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
	_, err = m.Share(ctxOf("admin"), resourceInfo(0), grant(groupGrantee("group1")))
	g.Expect(err).ToNot(HaveOccurred(), "a new share of the same resource to the same grantee")
}

// Break: the rollback does not remove the grantee entry. The failed write
// keeps the share ID in memory, and the next write of the same grantee
// document uploads this ID without a share behind it.
func TestShareRollbackDoesNotLeaveTheGranteeEntryInMemory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grantee  *provider.Grantee
		failPath string
		ids      func(m *Manager) []string
	}{
		{
			name:     "group",
			grantee:  groupGrantee("group1"),
			failPath: "/groups/group1/received.json",
			ids: func(m *Manager) []string {
				l, err := m.GroupReceivedCache.List(ctxOf("admin"), "group1")
				if err != nil {
					return []string{err.Error()}
				}
				var ids []string
				for id := range l["storageid:spaceid"].IDs {
					ids = append(ids, id)
				}
				return ids
			},
		},
		{
			name:     "user",
			grantee:  userGrantee("einstein"),
			failPath: "/users/einstein/received.json",
			ids: func(m *Manager) []string {
				l, err := m.UserReceivedStates.List(ctxOf("einstein"), "einstein")
				if err != nil {
					return []string{err.Error()}
				}
				var ids []string
				if s := l["storageid:spaceid"]; s != nil {
					for id := range s.States {
						ids = append(ids, id)
					}
				}
				return ids
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			f := newBatchFixture(t)

			f.cs.FailPath = tc.failPath
			_, err := f.m.Share(ctxOf("admin"), resourceInfo(0), grant(tc.grantee))
			g.Expect(err).To(HaveOccurred())
			f.cs.FailPath = ""

			second, err := f.m.Share(ctxOf("admin"), resourceInfo(1), grant(tc.grantee))
			g.Expect(err).ToNot(HaveOccurred())

			m, err := New(f.storage, nil, 0, nopStream{}, 0)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(tc.ids(m)).To(ConsistOf(second.GetId().GetOpaqueId()), "only the share that was created")
		})
	}
}

// Break: the stale cleanup reads each share from the live provider cache,
// not from the ListSpace snapshot that set staleIsProven. A parallel listing
// that gets a temporary NotFound empties the live cache between the two
// reads. Then all shares look stale, and the cleanup removes them from the
// group cache.
func TestListReceivedSharesDoesNotRemoveValidGroupSharesWhenTheCacheIsReset(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	const shares = 20
	for i := 0; i < shares; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), nil)
	}

	ns := &notFoundEverySecondDownloadStorage{Storage: f.storage, path: "/storages/storageid/spaceid.json"}
	m, err := New(ns, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	// With a grace period, no listing removes an entry, and the test proves nothing.
	m.StaleShareGracePeriod = 0
	// initialize reads m.initialized without the lock. Initialize before the
	// parallel listings, so the race detector reports only the races of this test.
	g.Expect(m.initialize(ctxOf("admin"))).To(Succeed())

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// The result can miss shares when the listing itself gets the
				// NotFound. Only the group cache on storage matters here.
				_, _ = m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
			}
		}()
	}
	wg.Wait()

	fresh, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	l, err := fresh.GroupReceivedCache.List(ctxOf("admin"), "group1")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(l["storageid:spaceid"].IDs).To(HaveLen(shares), "the group cache keeps all valid shares")
}

// slowGranteeRollbackStorage fails the first upload of path, so Share rolls
// back. Each later upload of path waits until the context ends. Each call
// fails at once on an ended context, the same as a gRPC call.
type slowGranteeRollbackStorage struct {
	metadata.Storage
	path    string
	uploads int32
}

func (s *slowGranteeRollbackStorage) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Path == s.path {
		if atomic.AddInt32(&s.uploads, 1) == 1 {
			return nil, errtypes.PermissionDenied("injected upload failure for " + req.Path)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Storage.Upload(ctx, req)
}

func (s *slowGranteeRollbackStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Storage.Download(ctx, req)
}

// Break: the three rollback removals share one timeout. The slow removal of
// the grantee entry uses all of it, and the removal of the share fails at
// once. The share stays, and a new Share fails with AlreadyExists.
func TestShareRollbackRemovesTheShareWhenTheGranteeRollbackIsSlow(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	old := rollbackTimeout
	rollbackTimeout = 200 * time.Millisecond
	t.Cleanup(func() { rollbackTimeout = old })

	ss := &slowGranteeRollbackStorage{Storage: f.storage, path: "/groups/group1/received.json"}
	m, err := New(ss, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())

	_, err = m.Share(ctxOf("admin"), resourceInfo(0), grant(groupGrantee("group1")))
	g.Expect(err).To(HaveOccurred())

	f.expectEmpty(t, []string{"admin"}, nil, nil)
	_, err = m.Share(ctxOf("admin"), resourceInfo(0), grant(userGrantee("einstein")))
	g.Expect(err).ToNot(HaveOccurred(), "a new share of the same resource")
	fresh, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	_, err = fresh.getByKey(ctxOf("admin"), &collaboration.ShareKey{ResourceId: resourceInfo(0).Id, Grantee: groupGrantee("group1")})
	g.Expect(err).To(HaveOccurred(), "the rolled back group share is gone")
}
