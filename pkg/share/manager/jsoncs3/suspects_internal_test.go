package jsoncs3

// A listing only filters. It never writes. It notes a share that it cannot
// use as a suspect. A background loop of the pod works through the suspects
// and removes them from the documents.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"google.golang.org/genproto/protobuf/field_mask"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	typesv1beta1 "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"

	. "github.com/onsi/gomega"
)

func ghostID(name string) string { return shareid.Encode("storageid", "spaceid", name) }

// f holds a valid group share, so the space file exists, and a ghost entry in
// the document of group1.
func newGhostFixture(t *testing.T) (*batchFixture, string) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 0
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	ghost := ghostID("ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())
	return f, ghost
}

func TestListReceivedSharesDoesNotWriteForAGhostEntry(t *testing.T) {
	g := NewWithT(t)
	f, ghost := newGhostFixture(t)

	uploads := f.uploadsDuring(func() {
		received, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(received).To(HaveLen(1), "only the valid share")
	})

	g.Expect(uploads).To(BeZero(), "a listing must not write")
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ContainElement(ghost))
}

func TestCleanupSuspectsRemovesAGhostEntryOfAGroup(t *testing.T) {
	g := NewWithT(t)
	f, ghost := newGhostFixture(t)
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())

	uploads := f.uploadsDuring(func() { f.m.CleanupSuspects(ctxOf("admin")) })

	g.Expect(uploads).To(Equal(int32(1)), "one upload for the document of the group")
	g.Expect(groupIDsOnStorage(t, f, "group1")).ToNot(ContainElement(ghost))
	g.Expect(f.m.suspectCount()).To(BeZero(), "a removed suspect is forgotten")
}

func TestCleanupSuspectsWaitsForTheGracePeriodOfAMissingShare(t *testing.T) {
	g := NewWithT(t)
	f, ghost := newGhostFixture(t)
	now := time.Now()
	f.m.staleClock = func() time.Time { return now }
	f.m.StaleShareGracePeriod = 5 * time.Minute
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())

	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ContainElement(ghost), "inside the grace period")

	now = now.Add(5*time.Minute + time.Second)
	f.m.CleanupSuspects(ctxOf("admin"))
	g.Expect(groupIDsOnStorage(t, f, "group1")).ToNot(ContainElement(ghost), "after the grace period")
}

// An older pod writes the grantee entry and the share at the same time. The
// listing can find the entry first. When the share is on storage later, the
// loop must drop the suspect and keep the entry.
func TestCleanupSuspectsKeepsAnEntryWhenTheShareAppears(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 0
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	id := ghostID("written-later")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", id)).To(Succeed())
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(f.m.suspectCount()).To(Equal(1))

	// The older pod writes the share now.
	g.Expect(f.m.Cache.Add(ctxOf("admin"), "storageid", "spaceid", id, &collaboration.Share{
		Id:         &collaboration.ShareId{OpaqueId: id},
		ResourceId: &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid", OpaqueId: "file9"},
		Grantee:    groupGrantee("group1"),
	})).To(Succeed())

	f.m.CleanupSuspects(ctxOf("admin"))

	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ContainElement(id))
	g.Expect(f.m.suspectCount()).To(BeZero())
}

func TestListSharesDoesNotWriteAndCleanupRemovesAGhostOfCreatedJson(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 0
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	ghost := ghostID("ghost")
	g.Expect(f.m.CreatedCache.Add(ctxOf("admin"), "admin", ghost)).To(Succeed())

	uploads := f.uploadsDuring(func() {
		shares, err := f.m.ListShares(ctxOf("admin"), nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(shares).To(HaveLen(1))
	})
	g.Expect(uploads).To(BeZero(), "a listing must not write")

	f.m.CleanupSuspects(ctxOf("admin"))

	l, err := f.m.CreatedCache.List(ctxOf("admin"), "admin")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(l["storageid:spaceid"].IDs).ToNot(HaveKey(ghost))
	g.Expect(l["storageid:spaceid"].IDs).To(HaveLen(1))
}

func TestListReceivedSharesDoesNotRemoveExpiredSharesAndTheLoopDoes(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	stream := &expiredEventStream{}
	m, err := New(f.cs, nil, 0, stream, 0)
	g.Expect(err).ToNot(HaveOccurred())
	f.m = m
	for i := 0; i < 20; i++ {
		f.share(t, ctxOf("admin"), i, groupGrantee("group1"), expiredAt)
	}

	uploads := f.uploadsDuring(func() {
		received, err := m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(received).To(BeEmpty())
	})
	g.Expect(uploads).To(BeZero(), "a listing must not write")
	g.Expect(atomic.LoadInt32(&stream.expired)).To(BeZero(), "a listing must not send events")

	uploads = f.uploadsDuring(func() { m.CleanupSuspects(ctxOf("admin")) })

	g.Expect(uploads).To(Equal(int32(3)), "one upload for each document")
	g.Expect(atomic.LoadInt32(&stream.expired)).To(Equal(int32(20)))
	f.expectEmpty(t, []string{"admin"}, []string{"group1"}, nil)
}

func TestExpiredSharesAreNotListedByTheOtherListings(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), expiredAt)

	uploads := f.uploadsDuring(func() {
		created, err := f.m.ListShares(ctxOf("admin"), nil)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(created).To(BeEmpty())
		byID, err := f.m.ListShares(ctxOf("admin"), []*collaboration.Filter{{
			Type: collaboration.Filter_TYPE_RESOURCE_ID,
			Term: &collaboration.Filter_ResourceId{ResourceId: &provider.ResourceId{StorageId: "storageid", SpaceId: "spaceid"}},
		}})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(byID).To(BeEmpty())
	})

	g.Expect(uploads).To(BeZero())
	g.Expect(f.m.suspectCount()).To(Equal(1), "both listings found the same share")
}

func TestReadOfAnExpiredShareIsNotFoundAndWritesNothing(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, userGrantee("einstein"), expiredAt)
	shares, err := f.m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	var id *collaboration.ShareId
	for _, s := range shares.Shares {
		id = s.GetId()
	}
	ref := &collaboration.ShareReference{Spec: &collaboration.ShareReference_Id{Id: id}}

	uploads := f.uploadsDuring(func() {
		_, err := f.m.GetShare(ctxOf("admin"), ref)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
		_, err = f.m.GetReceivedShare(ctxOf("einstein"), ref)
		g.Expect(err).To(BeAssignableToTypeOf(errtypes.NotFound("")))
	})
	g.Expect(uploads).To(BeZero())

	// UpdateReceivedShare must not create the grantee entry of an expired share again.
	_, err = f.m.UpdateReceivedShare(ctxOf("einstein"), &collaboration.ReceivedShare{
		Share: &collaboration.Share{Id: id},
		State: collaboration.ShareState_SHARE_STATE_ACCEPTED,
	}, fieldMaskState(), nil)
	g.Expect(err).To(HaveOccurred())
	// The entry that Share wrote stays as it was. The failed update did not write it again.
	l, err := f.m.UserReceivedStates.List(ctxOf("einstein"), "einstein")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(l["storageid:spaceid"].States).To(HaveLen(1))
	for _, state := range l["storageid:spaceid"].States {
		g.Expect(state.State).To(Equal(collaboration.ShareState_SHARE_STATE_PENDING), "the update must not change the state")
	}
}

func TestTheSuspectListHasALimit(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.suspectLimit = 5
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	for i := 0; i < 20; i++ {
		g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghostID(fmt.Sprintf("ghost%d", i)))).To(Succeed())
	}

	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(f.m.suspectCount()).To(Equal(5), "new suspects are dropped when the list is full")
}

func TestTheBackgroundLoopRemovesSuspectsAndStopsOnClose(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.m.StaleShareGracePeriod = 0
	f.m.CleanupInterval = 20 * time.Millisecond // before the first use: the first use starts the loop
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	ghost := ghostID("ghost")
	g.Expect(f.m.GroupReceivedCache.Add(ctxOf("admin"), "group1", ghost)).To(Succeed())
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())

	g.Eventually(func() []string { return groupIDsOnStorage(t, f, "group1") }, 5*time.Second, 20*time.Millisecond).
		ShouldNot(ContainElement(ghost))

	done := make(chan struct{})
	go func() { f.m.Close(); close(done) }()
	g.Eventually(done, 5*time.Second).Should(BeClosed())
	g.Expect(f.m.Close).ToNot(Panic(), "a second Close is safe")
}

// The run of the loop has no request, so it starts a root span. The span has a
// link to the span of the listing that found the suspect.
func TestCleanupRunLinksToTheListingThatFoundTheSuspect(t *testing.T) {
	g := NewWithT(t)
	f, _ := newGhostFixture(t)
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	listCtx := appctx.WithTracerProvider(ctxOf("einstein", "group1"), tp)
	_, err := f.m.ListReceivedShares(listCtx, nil, nil)
	g.Expect(err).ToNot(HaveOccurred())
	var listSpan tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == "ListReceivedShares" {
			listSpan = s
		}
	}
	g.Expect(listSpan.SpanContext.IsValid()).To(BeTrue())

	f.m.CleanupSuspects(appctx.WithTracerProvider(ctxOf("admin"), tp))

	var run *tracetest.SpanStub
	for _, s := range exp.GetSpans() {
		if s.Name == "CleanupSuspects" {
			run = &s
		}
	}
	g.Expect(run).ToNot(BeNil())
	g.Expect(run.Parent.IsValid()).To(BeFalse(), "a root span")
	g.Expect(run.Links).ToNot(BeEmpty())
	g.Expect(run.Links[0].SpanContext.TraceID()).To(Equal(listSpan.SpanContext.TraceID()))
}

func fieldMaskState() *field_mask.FieldMask {
	return &field_mask.FieldMask{Paths: []string{"state"}}
}

// Break: a user renews an expired share on another pod before the loop runs.
// The suspect holds the old share. The loop must read the share again and keep
// a share that is not expired any more.
func TestCleanupKeepsAShareThatWasRenewedAfterTheListing(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), expiredAt)
	_, err := f.m.ListReceivedShares(ctxOf("einstein", "group1"), nil, nil)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(f.m.suspectCount()).To(Equal(1))

	// Another pod moves the expiration into the future.
	shares, err := f.m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	var id string
	for k := range shares.Shares {
		id = k
	}
	_, err = f.m.Cache.Update(ctxOf("admin"), "storageid", "spaceid", id, func(s *collaboration.Share) {
		s.Expiration = &typesv1beta1.Timestamp{Seconds: uint64(time.Now().Add(time.Hour).Unix())}
	})
	g.Expect(err).ToNot(HaveOccurred())

	f.m.CleanupSuspects(ctxOf("admin"))

	after, err := f.m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(after.Shares).To(HaveKey(id), "a renewed share must stay")
	g.Expect(f.m.suspectCount()).To(BeZero())
}

// Break: the cleanup removes the entries when the space read got a NotFound
// that is not real. An empty etag means that the download got NotFound. That
// can be temporary. A Stat that also gets NotFound is the proof.
func TestCleanupKeepsEntriesWhenTheSpaceReadGetsATemporaryNotFound(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), nil)
	toggle := &spaceNotFoundStorage{Storage: f.storage}
	m, err := New(toggle, nil, 0, nopStream{}, 0)
	g.Expect(err).ToNot(HaveOccurred())
	m.StaleShareGracePeriod = 0
	valid := receivedIDs(t, m, "einstein", "group1")
	g.Expect(valid).To(HaveLen(1))

	toggle.fail.Store(true) // the space file exists, but the read says NotFound
	g.Expect(receivedIDs(t, m, "einstein", "group1")).To(BeEmpty())
	m.CleanupSuspects(ctxOf("admin"))
	toggle.fail.Store(false)

	g.Expect(groupIDsOnStorage(t, f, "group1")).To(ConsistOf(valid[0]), "the share is valid, the entry must stay")
}

// spaceNotFoundStorage answers NotFound for the downloads of space files while
// fail is true. Stat shows that the file exists.
type spaceNotFoundStorage struct {
	metadata.Storage
	fail atomic.Bool
}

func (s *spaceNotFoundStorage) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if s.fail.Load() && strings.HasPrefix(req.Path, "/storages/") {
		return nil, errtypes.NotFound("injected temporary not found: " + req.Path)
	}
	return s.Storage.Download(ctx, req)
}
