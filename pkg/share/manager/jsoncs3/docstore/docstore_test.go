package docstore_test

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/docstore"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

const docPath = "/docs/a.json"

// set is the test document: a set of strings.
type set struct {
	Items map[string]struct{}
	Etag  string
}

var setCodec = docstore.Codec[set]{
	New: func() *set { return &set{Items: map[string]struct{}{}} },
	Clone: func(s *set) *set {
		c := &set{Items: make(map[string]struct{}, len(s.Items)), Etag: s.Etag}
		for k := range s.Items {
			c.Items[k] = struct{}{}
		}
		return c
	},
	Decode: func(data []byte) (*set, error) {
		s := &set{}
		if err := json.Unmarshal(data, s); err != nil {
			return nil, err
		}
		if s.Items == nil {
			s.Items = map[string]struct{}{}
		}
		return s, nil
	},
	Encode:  func(s *set, etag string) ([]byte, error) { c := *s; c.Etag = etag; return json.Marshal(&c) },
	SetEtag: func(s *set, etag string) { s.Etag = etag },
}

func add(items ...string) func(*set) (bool, error) {
	return func(s *set) (bool, error) {
		changed := false
		for _, i := range items {
			if _, ok := s.Items[i]; !ok {
				s.Items[i] = struct{}{}
				changed = true
			}
		}
		return changed, nil
	}
}

func remove(items ...string) func(*set) (bool, error) {
	return func(s *set) (bool, error) {
		changed := false
		for _, i := range items {
			if _, ok := s.Items[i]; ok {
				delete(s.Items, i)
				changed = true
			}
		}
		return changed, nil
	}
}

func keys(s *set) []string {
	out := make([]string, 0, len(s.Items))
	for k := range s.Items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// probe wraps a storage. It counts the calls and it can inject errors, hold a
// download back, or lose the answer of an upload.
type probe struct {
	metadata.Storage

	dl200, dl304, dl404, uploads, conflicts atomic.Int32

	failDownload atomic.Pointer[error]
	failUpload   atomic.Pointer[error]
	// loseAnswer makes an upload succeed on the storage, but return Unavailable.
	loseAnswer atomic.Bool

	// hold, when set, makes Download read the file at once and answer only
	// after release is closed. The answer is then old when a write comes in
	// between.
	hold atomic.Pointer[holdGate]
}

type holdGate struct {
	reached chan struct{} // closed when a Download is on hold
	release chan struct{}
	once    sync.Once
}

func newProbe() *probe { return &probe{Storage: helpers.NewCASStorage()} }

func (p *probe) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	if errp := p.failDownload.Load(); errp != nil {
		return nil, *errp
	}
	res, err := p.Storage.Download(ctx, req)
	if g := p.hold.Load(); g != nil {
		g.once.Do(func() { close(g.reached) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch err.(type) {
	case nil:
		p.dl200.Add(1)
	case errtypes.NotModified:
		p.dl304.Add(1)
	case errtypes.NotFound:
		p.dl404.Add(1)
	}
	return res, err
}

func (p *probe) Upload(ctx context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	p.uploads.Add(1)
	if errp := p.failUpload.Load(); errp != nil {
		return nil, *errp
	}
	res, err := p.Storage.Upload(ctx, req)
	if err != nil {
		p.conflicts.Add(1)
		return nil, err
	}
	if p.loseAnswer.CompareAndSwap(true, false) {
		return nil, status.Error(codes.Unavailable, "lost answer")
	}
	return res, nil
}

func (p *probe) holdDownloads() *holdGate {
	g := &holdGate{reached: make(chan struct{}), release: make(chan struct{})}
	p.hold.Store(g)
	return g
}

func (p *probe) stored(t *testing.T) *set {
	t.Helper()
	res, err := p.Storage.Download(context.Background(), metadata.DownloadRequest{Path: docPath})
	require.NoError(t, err)
	s, err := setCodec.Decode(res.Content)
	require.NoError(t, err)
	return s
}

func newStore(p *probe) *docstore.Store[set] {
	return docstore.New(p, setCodec)
}

func setErr(p *atomic.Pointer[error], err error) {
	if err == nil {
		p.Store(nil)
		return
	}
	p.Store(&err)
}

func TestGetOfAMissingDocumentGivesAnEmptyDocument(t *testing.T) {
	p := newProbe()
	s := newStore(p)

	e, err := s.Get(context.Background(), docPath)

	require.NoError(t, err)
	assert.Empty(t, e.Doc.Items)
	assert.Empty(t, e.Etag)
	assert.EqualValues(t, 1, p.dl404.Load())
}

func TestSecondGetUsesTheEtag(t *testing.T) {
	p := newProbe()
	require.NoError(t, newStore(p).Update(context.Background(), docPath, add("a")))
	s := newStore(p)

	e1, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	e2, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)

	assert.Equal(t, []string{"a"}, keys(e1.Doc))
	assert.Equal(t, e1.Etag, e2.Etag)
	assert.EqualValues(t, 1, p.dl200.Load())
	assert.EqualValues(t, 1, p.dl304.Load())
}

func TestFailedDownloadKeepsTheOldEntryAndReturnsTheError(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	setErr(&p.failDownload, errtypes.PermissionDenied("no"))

	_, err := s.Get(context.Background(), docPath)
	require.Error(t, err)
	setErr(&p.failDownload, nil)

	e, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(e.Doc), "a failed read must not reset the entry")
	assert.EqualValues(t, 0, p.dl200.Load(), "the entry still has its etag, so the read gets 304")
}

func TestFailedDownloadOfATransientErrorIsRetried(t *testing.T) {
	p := newProbe()
	require.NoError(t, newStore(p).Update(context.Background(), docPath, add("a")))
	s := newStore(p)
	setErr(&p.failDownload, status.Error(codes.Unavailable, "down"))
	go func() {
		time.Sleep(20 * time.Millisecond)
		setErr(&p.failDownload, nil)
	}()

	e, err := s.Get(context.Background(), docPath)

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(e.Doc))
}

func TestUpdateCreatesTheDocumentWithIfNoneMatch(t *testing.T) {
	p := newProbe()
	s := newStore(p)

	require.NoError(t, s.Update(context.Background(), docPath, add("a")))

	assert.Equal(t, []string{"a"}, keys(p.stored(t)))
}

func TestUpdatePublishesTheEntryAfterTheUpload(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	p.dl200.Store(0)
	p.dl304.Store(0)

	e, err := s.Get(context.Background(), docPath)

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(e.Doc))
	assert.EqualValues(t, 1, p.dl304.Load(), "the writer reads its own write with a 304")
	assert.EqualValues(t, 0, p.dl200.Load())
}

func TestFailedUploadDoesNotChangeTheEntry(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	before, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	setErr(&p.failUpload, errtypes.PermissionDenied("no"))

	err = s.Update(context.Background(), docPath, add("b"))
	require.Error(t, err)
	setErr(&p.failUpload, nil)

	after, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(after.Doc))
	assert.Equal(t, []string{"a"}, keys(before.Doc), "the document that a reader holds does not change")
	assert.Equal(t, []string{"a"}, keys(p.stored(t)))
}

func TestPublishedEntryDoesNotChangeAfterALaterUpdate(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	held, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a reader walks over the held document while the update runs
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = keys(held.Doc)
		}
	}()
	require.NoError(t, s.Update(context.Background(), docPath, add("b")))
	wg.Wait()

	assert.Equal(t, []string{"a"}, keys(held.Doc))
}

func TestTwoPodsLoseNoChange(t *testing.T) {
	p := newProbe()
	pods := []*docstore.Store[set]{newStore(p), newStore(p)}
	const writers, each = 4, 25

	var wg sync.WaitGroup
	for pod := range pods {
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					item := strconv.Itoa(pod) + "-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)
					assert.NoError(t, pods[pod].Update(context.Background(), docPath, add(item)))
				}
			}()
		}
	}
	wg.Wait()

	assert.Len(t, p.stored(t).Items, len(pods)*writers*each, "a change got lost")
}

func TestUpdateWithNoChangeOnAStaleBaseFindsTheEntry(t *testing.T) {
	p := newProbe()
	podA, podB := newStore(p), newStore(p)
	require.NoError(t, podA.Update(context.Background(), docPath, add("x")))
	_, err := podA.Get(context.Background(), docPath) // A caches {x}
	require.NoError(t, err)
	require.NoError(t, podB.Update(context.Background(), docPath, add("s"))) // B adds s

	require.NoError(t, podA.Update(context.Background(), docPath, remove("s")))

	assert.Equal(t, []string{"x"}, keys(p.stored(t)), "s is a ghost, because A did not look at the file")
}

// The fresh download of an Update must not join a read that is already
// running. That read has the old body.
func TestUpdateWithNoChangeDoesNotJoinARunningRead(t *testing.T) {
	p := newProbe()
	podA, podB := newStore(p), newStore(p)
	require.NoError(t, podA.Update(context.Background(), docPath, add("x")))
	_, err := podA.Get(context.Background(), docPath)
	require.NoError(t, err)
	require.NoError(t, podB.Update(context.Background(), docPath, add("s")))

	// A read starts on pod A. It has the old body ({x}, etag 1) and is on hold.
	// The new etag of the file is different, so the read does not get a 304.
	gate := p.holdDownloads()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = podA.Get(context.Background(), docPath)
	}()
	<-gate.reached
	// From now on, downloads are not held.
	p.hold.Store(nil)

	done := make(chan error, 1)
	go func() { done <- podA.Update(context.Background(), docPath, remove("s")) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the update waits for the running read")
	}
	close(gate.release)
	<-readDone

	assert.Equal(t, []string{"x"}, keys(p.stored(t)))
}

func TestSlowGetDoesNotReplaceTheEntryOfALaterUpdate(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	// A second pod changes the file, so that the slow read gets a 200.
	other := newStore(p)
	require.NoError(t, other.Update(context.Background(), docPath, add("b")))

	gate := p.holdDownloads()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = s.Get(context.Background(), docPath) // reads {a,b}, on hold
	}()
	<-gate.reached
	p.hold.Store(nil)
	require.NoError(t, s.Update(context.Background(), docPath, add("c"))) // publishes {a,b,c}
	close(gate.release)
	<-readDone

	p.dl200.Store(0)
	p.dl304.Store(0)
	e, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, keys(e.Doc))
	assert.EqualValues(t, 1, p.dl304.Load(), "the entry of the update must stay")
	assert.EqualValues(t, 0, p.dl200.Load())
}

func TestParallelReadsOfOnePathMakeOneDownload(t *testing.T) {
	p := newProbe()
	require.NoError(t, newStore(p).Update(context.Background(), docPath, add("a")))
	s := newStore(p)
	p.dl200.Store(0)
	p.dl304.Store(0)
	p.dl404.Store(0)
	gate := p.holdDownloads()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := s.Get(context.Background(), docPath)
			assert.NoError(t, err)
			assert.Equal(t, []string{"a"}, keys(e.Doc))
		}()
	}
	<-gate.reached
	time.Sleep(50 * time.Millisecond) // the other readers join the download
	close(gate.release)
	wg.Wait()

	assert.EqualValues(t, 1, p.dl200.Load()+p.dl304.Load()+p.dl404.Load())
}

func TestWaiterWithACancelledContextLeaves(t *testing.T) {
	p := newProbe()
	require.NoError(t, newStore(p).Update(context.Background(), docPath, add("a")))
	s := newStore(p)
	gate := p.holdDownloads()

	other := make(chan error, 1)
	go func() {
		_, err := s.Get(context.Background(), docPath)
		other <- err
	}()
	<-gate.reached

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Get(ctx, docPath)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)

	close(gate.release)
	select {
	case err := <-other:
		require.NoError(t, err, "the other waiter must get the result")
	case <-time.After(5 * time.Second):
		t.Fatal("the other waiter got no result")
	}
}

func TestCancelOfTheLeaderDoesNotCancelTheDownloadOfTheOthers(t *testing.T) {
	p := newProbe()
	require.NoError(t, newStore(p).Update(context.Background(), docPath, add("a")))
	s := newStore(p)
	gate := p.holdDownloads()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := s.Get(leaderCtx, docPath)
		leader <- err
	}()
	<-gate.reached
	follower := make(chan error, 1)
	go func() {
		_, err := s.Get(context.Background(), docPath)
		follower <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancelLeader()
	require.Error(t, <-leader)
	close(gate.release)

	select {
	case err := <-follower:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the follower got no result")
	}
}

func TestRetryAfterALostAnswerGivesOneChange(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	p.loseAnswer.Store(true)
	p.uploads.Store(0)

	require.NoError(t, s.Update(context.Background(), docPath, add("b")))

	assert.Equal(t, []string{"a", "b"}, keys(p.stored(t)))
	assert.EqualValues(t, 2, p.uploads.Load(), "the upload with the lost answer, and the repeat that gets a conflict. The fresh download finds the change and stops")
}

func TestChangeFunctionResultsComeFromTheLastCall(t *testing.T) {
	p := newProbe()
	podA, podB := newStore(p), newStore(p)
	require.NoError(t, podA.Update(context.Background(), docPath, add("x", "y")))
	_, err := podA.Get(context.Background(), docPath)
	require.NoError(t, err)
	// B removes y. A still has {x,y} cached.
	require.NoError(t, podB.Update(context.Background(), docPath, remove("y")))

	var calls int
	var removed []string
	err = podA.Update(context.Background(), docPath, func(d *set) (bool, error) {
		calls++
		removed = nil // each call starts with empty results
		for _, k := range []string{"x", "y"} {
			if _, ok := d.Items[k]; ok {
				delete(d.Items, k)
				removed = append(removed, k)
			}
		}
		return len(removed) > 0, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the first call on the stale base conflicts")
	assert.Equal(t, []string{"x"}, removed, "only the last call counts: y was gone on the fresh base")
	assert.Empty(t, p.stored(t).Items)
}

func TestUpdateStopsWhenTheContextIsDone(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.Update(ctx, docPath, add("a"))

	require.Error(t, err)
	assert.EqualValues(t, 0, p.uploads.Load())
}

func TestUpdateReturnsTheErrorOfTheChangeFunction(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	want := errtypes.BadRequest("no")

	err := s.Update(context.Background(), docPath, func(*set) (bool, error) { return false, want })

	assert.Equal(t, want, err)
	assert.EqualValues(t, 0, p.uploads.Load())
}

func TestCreateWritesAnEmptyDocumentOnce(t *testing.T) {
	p := newProbe()
	s := newStore(p)

	require.NoError(t, s.Create(context.Background(), docPath))
	require.NoError(t, s.Create(context.Background(), docPath), "a second create finds the document and stops")

	assert.Empty(t, p.stored(t).Items)
	e, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.NotEmpty(t, e.Etag)
}

// A read that starts after a write must not join a read that started before
// the write. That read can return the old body.
func TestGetAfterAnUpdateDoesNotJoinAnOlderRead(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	other := newStore(p)
	require.NoError(t, other.Update(context.Background(), docPath, add("b")))

	gate := p.holdDownloads()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = s.Get(context.Background(), docPath) // old read, on hold
	}()
	<-gate.reached
	p.hold.Store(nil)
	require.NoError(t, s.Update(context.Background(), docPath, add("c")))

	got := make(chan *set, 1)
	go func() {
		e, err := s.Get(context.Background(), docPath)
		if err == nil {
			got <- e.Doc
		}
	}()
	select {
	case d := <-got:
		assert.Equal(t, []string{"a", "b", "c"}, keys(d))
	case <-time.After(3 * time.Second):
		t.Error("the read joined the older read that is on hold")
	}
	close(gate.release)
	<-readDone
}

// ttlStore returns a store with a TTL and a clock that the test controls.
func ttlStore(p *probe, ttl time.Duration, now *time.Time) *docstore.Store[set] {
	return docstore.New(p, setCodec, docstore.Options{TTL: ttl, Now: func() time.Time { return *now }})
}

func (p *probe) downloads() int32 { return p.dl200.Load() + p.dl304.Load() + p.dl404.Load() }

func TestGetWithinTheTTLMakesNoDownload(t *testing.T) {
	p := newProbe()
	now := time.Now()
	s := ttlStore(p, time.Minute, &now)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	_, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	before := p.downloads()

	now = now.Add(30 * time.Second)
	e, err := s.Get(context.Background(), docPath)

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(e.Doc))
	assert.Equal(t, before, p.downloads(), "a read inside the TTL needs no download")
}

func TestGetAfterTheTTLChecksTheStorageAgain(t *testing.T) {
	p := newProbe()
	now := time.Now()
	s := ttlStore(p, time.Minute, &now)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	other := newStore(p)
	require.NoError(t, other.Update(context.Background(), docPath, add("b")))

	now = now.Add(30 * time.Second)
	e, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, keys(e.Doc), "another pod wrote, this pod sees it up to the TTL later")

	now = now.Add(31 * time.Second)
	e, err = s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, keys(e.Doc), "after the TTL")
}

func TestWriterReadsItsOwnWriteInsideTheTTL(t *testing.T) {
	p := newProbe()
	now := time.Now()
	s := ttlStore(p, time.Minute, &now)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	_, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	before := p.downloads()

	require.NoError(t, s.Update(context.Background(), docPath, add("b")))
	e, err := s.Get(context.Background(), docPath)

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, keys(e.Doc))
	assert.Equal(t, before, p.downloads(), "the write published the new entry")
}

// An Unshare on pod A removes an item. Pod B has the old document in its entry,
// and the TTL is not over. The removal on pod B must still find the file state,
// and the entry of pod B must show the removal afterwards.
func TestUpdateWithNoChangeOnAStaleBaseFixesTheEntryInsideTheTTL(t *testing.T) {
	p := newProbe()
	now := time.Now()
	podA, podB := ttlStore(p, time.Minute, &now), ttlStore(p, time.Minute, &now)
	require.NoError(t, podA.Update(context.Background(), docPath, add("x", "ghost")))
	_, err := podB.Get(context.Background(), docPath) // B caches {x, ghost}
	require.NoError(t, err)
	require.NoError(t, podA.Update(context.Background(), docPath, remove("ghost")))

	require.NoError(t, podB.Update(context.Background(), docPath, remove("ghost"))) // no change on the stale base

	now = now.Add(time.Second)
	e, err := podB.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, keys(e.Doc), "the fresh download of the update fixed the entry")
}

func TestWithoutATTLEachGetChecksTheStorage(t *testing.T) {
	p := newProbe()
	s := newStore(p)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))
	before := p.downloads()

	_, err := s.Get(context.Background(), docPath)
	require.NoError(t, err)
	_, err = s.Get(context.Background(), docPath)
	require.NoError(t, err)

	assert.EqualValues(t, 2, p.downloads()-before)
}

func TestAFailedReadInsideTheTTLWindowDoesNotExtendTheEntry(t *testing.T) {
	p := newProbe()
	now := time.Now()
	s := ttlStore(p, time.Minute, &now)
	require.NoError(t, s.Update(context.Background(), docPath, add("a")))

	now = now.Add(2 * time.Minute) // the TTL is over
	setErr(&p.failDownload, errtypes.PermissionDenied("no"))
	_, err := s.Get(context.Background(), docPath)
	require.Error(t, err)
	setErr(&p.failDownload, nil)
	before := p.downloads()

	_, err = s.Get(context.Background(), docPath)
	require.NoError(t, err)
	assert.EqualValues(t, 1, p.downloads()-before, "the failed read did not renew the TTL")
}
