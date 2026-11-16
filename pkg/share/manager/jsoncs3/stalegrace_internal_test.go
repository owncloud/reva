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

// An older version writes the grantee entry and the share at the same time.
// During a rolling upgrade, a listing can find the grantee entry before the
// share is on storage. That entry is not stale yet, so a listing must not
// remove it at once.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

func receivedIDs(t *testing.T, m *Manager, user string, groups ...string) []string {
	received, err := m.ListReceivedShares(ctxOf(user, groups...), nil, nil)
	NewWithT(t).Expect(err).ToNot(HaveOccurred())
	ids := []string{}
	for _, rs := range received {
		ids = append(ids, rs.GetShare().GetId().GetOpaqueId())
	}
	return ids
}

func groupIDsOnStorage(t *testing.T, f *batchFixture, gid string) []string {
	g := NewWithT(t)
	m, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	l, err := m.GroupReceivedCache.List(ctxOf("admin"), gid)
	g.Expect(err).ToNot(HaveOccurred())
	ids := []string{}
	for id := range l["storageid:spaceid"].IDs {
		ids = append(ids, id)
	}
	return ids
}

// Break: a listing removes a grantee entry at the first time that it finds
// no share for it. An older pod writes the share a moment later, but no
// group member sees the share any more.
func TestListReceivedSharesKeepsAGroupEntryThatIsWrittenBeforeTheShare(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	valid := receivedIDs(t, f.m, "einstein", "group1")
	g.Expect(valid).To(HaveLen(1))

	// The older pod writes the group entry first.
	oldPod, err := New(f.storage, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	id := shareid.Encode("storageid", "spaceid", "written-by-an-older-pod")
	g.Expect(oldPod.GroupReceivedCache.Add(ctxOf("admin"), "group1", id)).To(Succeed())

	// A new pod lists in the window before the share is on storage.
	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the share is not on storage yet")

	// The older pod writes the share.
	s := &collaboration.Share{
		Id:          &collaboration.ShareId{OpaqueId: id},
		ResourceId:  &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid", OpaqueId: "file1"},
		Permissions: &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true}},
		Grantee:     groupGrantee("group1"),
		Owner:       &userpb.UserId{OpaqueId: "owner"},
		Creator:     &userpb.UserId{OpaqueId: "admin"},
	}
	g.Expect(oldPod.Cache.Add(ctxOf("admin"), "storageid", "spaceid", id, s)).To(Succeed())
	g.Expect(oldPod.CreatedCache.Add(ctxOf("admin"), "admin", id)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0], id), "the group member sees the new share")
}

// Break: the listing ignores the grace period and removes the stale entry
// at once, or it never removes the stale entry.
func TestListReceivedSharesRemovesAStaleGroupEntryAfterTheGracePeriod(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	valid := receivedIDs(t, f.m, "einstein", "group1")
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the stale entry is not listed")
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0], ghost), "the first listing keeps the stale entry")

	time.Sleep(250 * time.Millisecond)
	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "a listing after the grace period removes the stale entry")
}

// Break: the listing keeps a stale entry when the space file does not exist.
// A failed first Share in a space leaves a grantee entry, but no space file.
// Each listing then skips the entry again, and nothing removes it.
func TestListReceivedSharesRemovesAStaleGroupEntryWhenTheSpaceFileDoesNotExist(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(BeEmpty())
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(ghost), "the first listing keeps the stale entry")

	time.Sleep(250 * time.Millisecond)
	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(BeEmpty())
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(BeEmpty(), "a listing after the grace period removes the stale entry")
}

// Break: the grace period does not restart when a listing finds the share
// again. Two short losses of the space file, longer apart than the grace
// period, then look like one long loss, and the listing removes a valid entry.
func TestListReceivedSharesRestartsTheGracePeriodWhenTheShareIsFoundAgain(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	valid := receivedIDs(t, f.m, "einstein", "group1")
	g.Expect(valid).To(HaveLen(1))

	spaceFile := "/storages/storageid/spaceid.json"
	content, err := f.storage.SimpleDownload(ctxOf("admin"), spaceFile)
	g.Expect(err).ToNot(HaveOccurred())
	loseSpaceFileDuringAListing := func() {
		g.Expect(f.storage.Delete(ctxOf("admin"), spaceFile)).To(Succeed())
		_ = receivedIDs(t, f.m, "einstein", "group1")
		g.Expect(f.storage.SimpleUpload(ctxOf("admin"), spaceFile, content)).To(Succeed())
	}

	loseSpaceFileDuringAListing()
	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the space file is back")
	time.Sleep(250 * time.Millisecond)
	loseSpaceFileDuringAListing()

	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "two short losses do not remove the entry")
}

// countingDownloadStorage counts the downloads of path.
type countingDownloadStorage struct {
	metadata.Storage
	path      string
	downloads int32
}

func (c *countingDownloadStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if req.Path == c.path {
		atomic.AddInt32(&c.downloads, 1)
	}
	return c.Storage.Download(ctx, req)
}

// Break: the cleanup also sends the stale IDs of a group to the received.json
// of the user. These IDs are not in that file, so the user cache downloads it
// again only to find nothing to remove.
func TestListReceivedSharesCleansAStaleGroupEntryWithoutAnExtraUserDownload(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	cd := &countingDownloadStorage{Storage: f.storage, path: "/users/einstein/received.json"}
	m, err := New(cd, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	m.StaleShareGracePeriod = 0 // this test checks the cleanup, not the grace period
	downloadsDuringAListing := func() int32 {
		before := atomic.LoadInt32(&cd.downloads)
		_ = receivedIDs(t, m, "einstein", "group1")
		return atomic.LoadInt32(&cd.downloads) - before
	}
	_ = downloadsDuringAListing() // warm up the caches
	normal := downloadsDuringAListing()

	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())
	g.Expect(downloadsDuringAListing()).To(Equal(normal), "the cleanup of a group entry does not read received.json of the user again")
	g.Expect(groupIDsOnStorage(t, f, "group1")).ToNot(ContainElement(ghost), "the listing removes the stale entry")
}
