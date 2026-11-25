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
// share is on storage. That entry is not stale yet, so the cleanup must not
// remove it at once. The listing only notes a suspect. The cleanup loop of the
// pod removes the entry after the grace period, when the share is still missing.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
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

// Break: the cleanup removes a grantee entry at the first time that it finds
// no share for it. An older pod writes the share a moment later, but no
// group member sees the share any more.
func TestCleanupKeepsAGroupEntryThatIsWrittenBeforeTheShare(t *testing.T) {
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

	// A new pod lists and cleans in the window before the share is on storage.
	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the share is not on storage yet")
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0], id), "the grace period is not over")

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

// Break: the cleanup ignores the grace period and removes the stale entry
// at once, or it never removes the stale entry.
func TestCleanupRemovesAStaleGroupEntryAfterTheGracePeriod(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	valid := receivedIDs(t, f.m, "einstein", "group1")
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the stale entry is not listed")
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0], ghost), "the first cleanup keeps the stale entry")

	now = now.Add(250 * time.Millisecond)
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "a cleanup after the grace period removes the stale entry")
}

// Break: the cleanup keeps a stale entry when the space file does not exist.
// A failed first Share in a space leaves a grantee entry, but no space file.
// Nothing would remove it.
func TestCleanupRemovesAStaleGroupEntryWhenTheSpaceFileDoesNotExist(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(BeEmpty())
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(ghost), "the first cleanup keeps the stale entry")

	now = now.Add(250 * time.Millisecond)
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(BeEmpty(), "a cleanup after the grace period removes the stale entry")
}

// Break: the cleanup decides from the time of an old loss. The space file was
// away for a short time during a listing, and it is back at the next run. A
// grace period that is over must not remove the valid entry. The cleanup reads
// the space document again before it removes an entry.
func TestCleanupReadsTheSpaceAgainBeforeItRemovesAnEntry(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
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
	g.Expect(f.m.suspectCount()).To(Equal(1))
	now = now.Add(100 * time.Millisecond)
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(f.m.suspectCount()).To(Equal(1), "inside the grace period")

	now = now.Add(250 * time.Millisecond) // the grace period is over
	f.m.CleanupSuspects(ctxOf("admin"))   // the space file is back: nothing to remove

	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "the entry is valid")
	g.Expect(f.m.suspectCount()).To(BeZero())
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
func TestCleanupOfAStaleGroupEntryDoesNotReadTheUserDocument(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	cd := &countingDownloadStorage{Storage: f.storage, path: "/users/einstein/received.json"}
	m, err := New(cd, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	m.StaleShareGracePeriod = 0 // this test checks the cleanup, not the grace period
	_ = receivedIDs(t, m, "einstein", "group1")

	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())
	_ = receivedIDs(t, m, "einstein", "group1")
	before := atomic.LoadInt32(&cd.downloads)

	m.CleanupSuspects(ctxOf("admin"))

	g.Expect(atomic.LoadInt32(&cd.downloads)).To(Equal(before), "the cleanup of a group entry does not read received.json of the user")
	g.Expect(groupIDsOnStorage(t, f, "group1")).ToNot(ContainElement(ghost), "the cleanup removes the stale entry")
}

// Break: the cleanup adds a shareid field to a shared logger for each share
// that it finds. A later log line then has many shareid fields, and a log
// search shows the wrong share.
func TestCleanupLogsOneShareIDForEachStaleShare(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 0 // this test checks the log, not the grace period
	for n := range 8 {
		f.share(t, ctxOf("admin"), n, groupGrantee("group1"), nil)
	}
	ghosts := map[string]bool{}
	for _, name := range []string{"ghost1", "ghost2", "ghost3"} {
		id := shareid.Encode("storageid", "spaceid", name)
		ghosts[id] = true
		g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", id)).To(Succeed())
	}
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())

	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.WarnLevel)
	f.m.CleanupSuspects(appctx.WithLogger(ctxOf("admin"), &logger))

	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, "share not found. cleaning up") {
			continue
		}
		lines++
		g.Expect(strings.Count(line, `"shareid":`)).To(Equal(1), line)
		var entry struct {
			ShareID string `json:"shareid"`
		}
		g.Expect(json.Unmarshal([]byte(line), &entry)).To(Succeed())
		g.Expect(ghosts).To(HaveKey(entry.ShareID), line)
	}
	g.Expect(lines).To(Equal(3))
}

// Break: a new suspect drops the entries older than ten grace periods,
// counted from the first time that the pod found them. With many pods, one
// pod finds the same stale ID less often than that. The pod then never
// removes the stale entry.
func TestSuspectIsKeptForAShareThatThePodFindsRarely(t *testing.T) {
	g := NewWithT(t)
	now := time.Unix(1000, 0)
	m := &Manager{StaleShareGracePeriod: 20 * time.Millisecond, staleForgetAfter: time.Hour, staleClock: func() time.Time { return now }}
	m.noteSuspect(context.Background(), suspectInput{shareID: "ghost", reason: suspectMissing})
	_, missing := m.snapshotSuspects(now)
	g.Expect(missing).To(BeEmpty(), "the first listing starts the grace period")

	now = now.Add(250 * time.Millisecond) // more than ten grace periods
	m.noteSuspect(context.Background(), suspectInput{shareID: "other", reason: suspectMissing})

	_, missing = m.snapshotSuspects(now)
	ids := []string{}
	for _, s := range missing {
		ids = append(ids, s.shareID)
	}
	g.Expect(ids).To(ConsistOf("ghost"), "the pod still knows the stale ID, and the new one starts its grace period")
}

// Break: the pod forgets a suspect that a listing found a short time ago,
// because it counts from the first time that it found the ID.
func TestSuspectsForgetOnlyTheSharesThatNoListingFoundForALongTime(t *testing.T) {
	g := NewWithT(t)
	now := time.Unix(1000, 0)
	m := &Manager{StaleShareGracePeriod: time.Hour, staleForgetAfter: 150 * time.Second, staleClock: func() time.Time { return now }}
	note := func(id string) {
		m.noteSuspect(context.Background(), suspectInput{shareID: id, reason: suspectMissing})
	}
	note("found-again")
	note("not-found-again")
	now = now.Add(100 * time.Second)
	note("found-again")
	now = now.Add(100 * time.Second)
	note("new") // a new suspect starts a sweep

	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	g.Expect(m.suspects.byID).To(HaveKey("found-again"), "a listing found it 100s ago")
	g.Expect(m.suspects.byID).ToNot(HaveKey("not-found-again"), "no listing found it for 200s")
	g.Expect(m.suspects.byID).To(HaveKey("new"))
}

// Break: each new suspect runs the sweep over all entries under the lock. A
// listing that finds many new suspects then runs many full sweeps, and the
// other listings of the pod wait for the lock.
func TestSuspectsSweepAtMostOnceInEachSweepInterval(t *testing.T) {
	g := NewWithT(t)
	now := time.Unix(0, 0)
	m := &Manager{
		StaleShareGracePeriod: time.Minute,
		staleForgetAfter:      time.Hour,
		staleClock:            func() time.Time { return now },
	}
	note := func(id string) {
		m.noteSuspect(context.Background(), suspectInput{shareID: id, reason: suspectMissing})
	}
	note("old") // the first sweep

	now = now.Add(59*time.Minute + 30*time.Second)
	note("a") // a sweep, but "old" is not too old yet
	now = now.Add(40 * time.Second)
	note("b") // "old" is too old now, but the last sweep is 40 seconds ago

	m.suspects.mu.Lock()
	g.Expect(m.suspects.byID).To(HaveKey("old"), "no sweep in the interval after a sweep")
	m.suspects.mu.Unlock()

	now = now.Add(30 * time.Second)
	note("c") // a sweep: 70 seconds after the last sweep

	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	g.Expect(m.suspects.byID).ToNot(HaveKey("old"), "the next sweep forgets it")
	g.Expect(m.suspects.byID).To(HaveKey("a"))
	g.Expect(m.suspects.byID).To(HaveKey("b"))
	g.Expect(m.suspects.byID).To(HaveKey("c"))
}

func TestStaleForgetAfterIsLongerThanTheGracePeriod(t *testing.T) {
	NewWithT(t).Expect(defaultStaleForgetAfter).To(BeNumerically(">", defaultStaleShareGracePeriod))
}

// Break: the cleanup forgets the suspect before the removal runs. When the
// removal fails, the next run starts a new grace period. The stale entry then
// stays for one more grace period after each failed removal.
func TestCleanupRetriesAFailedStaleRemovalWithoutANewGracePeriod(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil) // a valid share, so the space file exists
	valid := receivedIDs(t, f.m, "einstein", "group1")
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	g.Expect(receivedIDs(t, f.m, "einstein", "group1")).To(ConsistOf(valid[0]), "the listing starts the grace period")
	now = now.Add(250 * time.Millisecond)

	f.cs.FailPath = "/groups/group1/received.json"
	f.m.CleanupSuspects(ctxOf("admin"))
	f.cs.FailPath = ""
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0], ghost), "the removal failed")

	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "the next run removes the stale entry without a new grace period")
}

// Break: the cleanup forgets the suspect only when the removal fails, and not
// after a successful removal. A new share with the same ID then has no grace
// period.
func TestCleanupForgetsASuspectAfterTheRemoval(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
	f.m.StaleShareGracePeriod = 200 * time.Millisecond
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	ghost := shareid.Encode("storageid", "spaceid", "ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())

	receivedIDs(t, f.m, "einstein", "group1")
	now = now.Add(250 * time.Millisecond)
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).ToNot(ContainElement(ghost))

	g.Expect(f.m.suspectCount()).To(BeZero())
}
