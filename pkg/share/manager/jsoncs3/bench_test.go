package jsoncs3_test

// Benchmarks for the jsoncs3 share manager.
//
// The scenarios use only the share.Manager methods. The constructor and the
// fields of the manager are in bench_shim_test.go. Thus the same scenarios run
// on an old commit and on a new commit, and benchstat can compare the
// results. See README.benchmarks.md for the steps.
//
// The storage is helpers.CASStorage (testhelpers/casstorage.go): an in-memory storage with a
// correct CAS check. countingStorage wraps it. The wrapper counts the calls
// and waits a fixed time for each call (JSONCS3_BENCH_RTT, default 1ms),
// because the in-memory storage has no network cost and a lock convoy would
// not show without it.
//
// Each operation does a fixed amount of work. Each iteration checks its
// result, so that a wrong implementation cannot look fast.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	grouppb "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
)

const (
	benchIDP       = "idp"
	benchStorageID = "storageid"
	benchOwner     = "owner"
)

// benchRTT is the wait for each storage call.
func benchRTT(b *testing.B) time.Duration {
	v := os.Getenv("JSONCS3_BENCH_RTT")
	if v == "" {
		return time.Millisecond
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		b.Fatalf("JSONCS3_BENCH_RTT: %v", err)
	}
	return d
}

// benchConcurrency is the MaxConcurrency of the managers. The ocis default is
// 1, production uses 10 or more.
func benchConcurrency(b *testing.B) int {
	v := os.Getenv("JSONCS3_BENCH_MAX_CONCURRENCY")
	if v == "" {
		return 10
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		b.Fatalf("JSONCS3_BENCH_MAX_CONCURRENCY: %v", err)
	}
	return n
}

// benchFixture is one metadata storage and one event stream that many
// managers share. A manager is one pod.
type benchFixture struct {
	store   *helpers.CASStorage
	storage *countingStorage
	stream  *countingStream
	counts  *callCounts
	pods    []*jsoncs3.Manager
}

func newBenchFixture(b *testing.B) *benchFixture {
	b.Helper()
	counts := &callCounts{}
	store := helpers.NewCASStorage()
	f := &benchFixture{
		store:   store,
		storage: &countingStorage{Storage: store, counts: counts},
		stream:  newCountingStream(counts),
		counts:  counts,
	}
	b.Cleanup(func() {
		for _, m := range f.pods {
			closeBenchManager(m)
		}
	})
	return f
}

// newPod returns a new manager with an empty cache.
func (f *benchFixture) newPod(b *testing.B) *jsoncs3.Manager {
	b.Helper()
	m, err := newBenchManager(f.storage, f.stream, benchConcurrency(b))
	if err != nil {
		b.Fatal(err)
	}
	f.pods = append(f.pods, m)
	return m
}

// newSeedPod returns a manager that uses the storage directly, with no count
// and no wait. Use it for setup work inside the measured loop.
func (f *benchFixture) newSeedPod(b *testing.B) *jsoncs3.Manager {
	b.Helper()
	m, err := newBenchManager(f.store, f.stream, benchConcurrency(b))
	if err != nil {
		b.Fatal(err)
	}
	f.pods = append(f.pods, m)
	return m
}

// measure resets the counts and turns on the wait for each call. Call it one
// time before the measured loop. A reset inside the loop loses the counts of
// the earlier iterations.

func (f *benchFixture) measure(b *testing.B) {
	f.counts.reset()
	f.storage.rtt.Store(int64(benchRTT(b)))
}

// seed turns off the wait for each call, so that the setup is fast.
func (f *benchFixture) seed() {
	f.storage.rtt.Store(0)
}

func userCtx(id string, groups ...string) context.Context {
	return ctxpkg.ContextSetUser(context.Background(), &userpb.User{
		Id:       &userpb.UserId{Idp: benchIDP, OpaqueId: id, Type: userpb.UserType_USER_TYPE_PRIMARY},
		Username: id,
		Groups:   groups,
	})
}

func resource(spaceID string, node int) *provider.ResourceInfo {
	return &provider.ResourceInfo{
		Id:    &provider.ResourceId{StorageId: benchStorageID, SpaceId: spaceID, OpaqueId: fmt.Sprintf("%s-node-%d", spaceID, node)},
		Owner: &userpb.UserId{Idp: benchIDP, OpaqueId: benchOwner},
	}
}

func readPermissions() *collaboration.SharePermissions {
	return &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true, InitiateFileDownload: true}}
}

func userGrant(id string) *collaboration.ShareGrant {
	return &collaboration.ShareGrant{
		Grantee: &provider.Grantee{
			Type: provider.GranteeType_GRANTEE_TYPE_USER,
			Id:   &provider.Grantee_UserId{UserId: &userpb.UserId{Idp: benchIDP, OpaqueId: id}},
		},
		Permissions: readPermissions(),
	}
}

func groupGrant(id string) *collaboration.ShareGrant {
	return &collaboration.ShareGrant{
		Grantee: &provider.Grantee{
			Type: provider.GranteeType_GRANTEE_TYPE_GROUP,
			Id:   &provider.Grantee_GroupId{GroupId: &grouppb.GroupId{Idp: benchIDP, OpaqueId: id}},
		},
		Permissions: readPermissions(),
	}
}

// expired returns the grant with an expiration in the past.
func expired(g *collaboration.ShareGrant) *collaboration.ShareGrant {
	g.Expiration = &types.Timestamp{Seconds: uint64(time.Now().Add(-time.Hour).Unix())}
	return g
}

func shareRef(s *collaboration.Share) *collaboration.ShareReference {
	return &collaboration.ShareReference{Spec: &collaboration.ShareReference_Id{Id: s.GetId()}}
}

func mustShare(b *testing.B, m *jsoncs3.Manager, md *provider.ResourceInfo, g *collaboration.ShareGrant) *collaboration.Share {
	b.Helper()
	s, err := m.Share(userCtx(benchOwner), md, g)
	if err != nil {
		b.Fatalf("share: %v", err)
	}
	return s
}

// listReceived lists the received shares and checks the number. It is safe
// to call from many goroutines.
func listReceived(m *jsoncs3.Manager, ctx context.Context, want func(int) bool) error {
	rs, err := m.ListReceivedShares(ctx, nil, nil)
	if err != nil {
		return fmt.Errorf("list received shares: %w", err)
	}
	if !want(len(rs)) {
		return fmt.Errorf("list received shares: unexpected number of shares: %d", len(rs))
	}
	return nil
}

func exactly(n int) func(int) bool { return func(got int) bool { return got == n } }
func atLeast(n int) func(int) bool { return func(got int) bool { return got >= n } }

func mustListReceived(b *testing.B, m *jsoncs3.Manager, ctx context.Context, want func(int) bool) {
	b.Helper()
	if err := listReceived(m, ctx, want); err != nil {
		b.Fatal(err)
	}
}

// p99 returns the 99th percentile in milliseconds.
func p99(durations []time.Duration) float64 {
	if len(durations) == 0 {
		return 0
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	i := len(durations) * 99 / 100
	if i >= len(durations) {
		i = len(durations) - 1
	}
	return float64(durations[i]) / float64(time.Millisecond)
}

// BenchmarkLogin lists the received shares of a user in 30 groups. 28 groups
// have no shares. The user has 50 received shares in 10 spaces: 30 user
// shares and 20 group shares through 2 groups.
func BenchmarkLogin(b *testing.B) {
	f := newBenchFixture(b)
	f.seed()
	seeder := f.newPod(b)
	groups := make([]string, 30)
	for i := range groups {
		groups[i] = fmt.Sprintf("login-group-%02d", i)
	}
	node := 0
	for space := 0; space < 10; space++ {
		spaceID := fmt.Sprintf("login-space-%d", space)
		for i := 0; i < 3; i++ {
			node++
			mustShare(b, seeder, resource(spaceID, node), userGrant("login-user"))
		}
		for _, g := range groups[:2] {
			node++
			mustShare(b, seeder, resource(spaceID, node), groupGrant(g))
		}
	}
	ctx := userCtx("login-user", groups...)
	// The first listing writes the files that a listing creates. After it,
	// each listing reads the same files.
	mustListReceived(b, seeder, ctx, exactly(50))

	// warm: the same pod lists again, so it has the etags.
	b.Run("warm", func(b *testing.B) {
		b.ReportAllocs()
		m := f.newPod(b)
		f.seed()
		mustListReceived(b, m, ctx, exactly(50))
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			mustListReceived(b, m, ctx, exactly(50))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})

	// cold: each listing runs on a new pod with an empty cache.
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		pods := make([]*jsoncs3.Manager, b.N)
		for i := range pods {
			pods[i] = f.newPod(b)
		}
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			mustListReceived(b, pods[i], ctx, exactly(50))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})
}

// BenchmarkHotGroup lists the received shares of 200 members of one group at
// the same time on one pod. In each round, 5 writers also add and remove 20
// shares of the group. The work of a round is fixed, so a faster
// implementation does not get more writes. The values are for one listing.
func BenchmarkHotGroup(b *testing.B) {
	const (
		members         = 200
		writers         = 5
		writesPerWriter = 4
	)
	b.ReportAllocs()
	f := newBenchFixture(b)
	f.seed()
	m := f.newPod(b)
	for i := 0; i < 20; i++ {
		mustShare(b, m, resource(fmt.Sprintf("hot-space-%d", i%10), i), groupGrant("hot-group"))
	}
	ctxs := make([]context.Context, members)
	for i := range ctxs {
		ctxs[i] = userCtx(fmt.Sprintf("hot-member-%03d", i), "hot-group")
	}
	mustListReceived(b, m, ctxs[0], exactly(20))

	var (
		mu        sync.Mutex
		durations []time.Duration
		node      = 1000
	)
	f.measure(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			first := node
			node += writesPerWriter
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := first; n < first+writesPerWriter; n++ {
					s, err := m.Share(userCtx(benchOwner), resource(fmt.Sprintf("hot-space-%d", n%10), n), groupGrant("hot-group"))
					if err != nil {
						b.Errorf("writer share: %v", err)
						return
					}
					if err := m.Unshare(userCtx(benchOwner), shareRef(s)); err != nil {
						b.Errorf("writer unshare: %v", err)
						return
					}
				}
			}()
		}
		for _, ctx := range ctxs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				// A listing can see a share of a writer before the unshare.
				if err := listReceived(m, ctx, atLeast(20)); err != nil {
					b.Error(err)
				}
				d := time.Since(start)
				mu.Lock()
				durations = append(durations, d)
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	b.StopTimer()
	b.ReportMetric(p99(durations), "p99-ms/listing")
	f.counts.report(b, b.N*members)
}

// BenchmarkLargeSpace uses one space with 1,000 shares.
func BenchmarkLargeSpace(b *testing.B) {
	const shares = 1000
	f := newBenchFixture(b)
	f.seed()
	seeder := f.newPod(b)
	var first *collaboration.Share
	for i := 0; i < shares; i++ {
		s := mustShare(b, seeder, resource("large-space", i), userGrant(fmt.Sprintf("large-user-%04d", i)))
		if i == 0 {
			first = s
		}
	}
	ctx := userCtx("large-user-0000")
	mustListReceived(b, seeder, ctx, exactly(1))

	// list-cold: a new pod downloads and decodes the full space document.
	b.Run("list-cold", func(b *testing.B) {
		b.ReportAllocs()
		pods := make([]*jsoncs3.Manager, b.N)
		for i := range pods {
			pods[i] = f.newPod(b)
		}
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			mustListReceived(b, pods[i], ctx, exactly(1))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})

	// list-warm: the same pod lists again, so it has the etags.
	b.Run("list-warm", func(b *testing.B) {
		b.ReportAllocs()
		m := f.newPod(b)
		f.seed()
		mustListReceived(b, m, ctx, exactly(1))
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			mustListReceived(b, m, ctx, exactly(1))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})

	// update: the creator changes the permissions of one share. Each update
	// uploads the full space document.
	b.Run("update", func(b *testing.B) {
		b.ReportAllocs()
		m := f.newPod(b)
		ref := shareRef(first)
		mask := &fieldmaskpb.FieldMask{Paths: []string{"permissions"}}
		// Read the space one time, so that the result does not include the
		// start of the pod.
		f.seed()
		if _, err := m.GetShare(userCtx(benchOwner), ref); err != nil {
			b.Fatalf("get share: %v", err)
		}
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := readPermissions()
			p.Permissions.ListContainer = i%2 == 0
			s, err := m.UpdateShare(userCtx(benchOwner), ref, p, &collaboration.Share{Id: first.GetId(), Permissions: p}, mask)
			if err != nil {
				b.Fatalf("update share: %v", err)
			}
			if s.GetPermissions().GetPermissions().GetListContainer() != p.Permissions.ListContainer {
				b.Fatal("update share: the result does not have the new permissions")
			}
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})
}

// BenchmarkPods runs shares and unshares on many pods at the same time. The
// pods use the same space and the same creator, so they write the same
// documents. One operation is one share, and every second share is removed
// again. After the run, a new pod checks that no change is lost.
func BenchmarkPods(b *testing.B) {
	for _, n := range []int{2, 10} {
		b.Run(fmt.Sprintf("pods=%d", n), func(b *testing.B) {
			benchmarkPods(b, n)
		})
	}
}

func benchmarkPods(b *testing.B, n int) {
	b.ReportAllocs()
	f := newBenchFixture(b)
	pods := make([]*jsoncs3.Manager, n)
	for i := range pods {
		pods[i] = f.newPod(b)
	}

	type result struct {
		id      *collaboration.ShareId
		grantee string
		removed bool
	}
	var (
		mu      sync.Mutex
		results []result
	)
	f.measure(b)
	b.ResetTimer()
	var wg sync.WaitGroup
	for p, m := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := p; i < b.N; i += len(pods) {
				grantee := fmt.Sprintf("pods-user-%d", i)
				s, err := m.Share(userCtx(benchOwner), resource("pods-space", i), userGrant(grantee))
				if err != nil {
					b.Errorf("share: %v", err)
					continue
				}
				r := result{id: s.GetId(), grantee: grantee}
				if i%2 == 1 {
					if err := m.Unshare(userCtx(benchOwner), shareRef(s)); err != nil {
						b.Errorf("unshare: %v", err)
						continue
					}
					r.removed = true
				}
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	b.StopTimer()
	f.counts.report(b, b.N)

	// A new pod reads the result. A share that exists must be in the space
	// document, in the created shares of the creator and in the received
	// shares of its grantee. A removed share must be in none of them.
	f.seed()
	check := f.newPod(b)
	created, err := check.ListShares(userCtx(benchOwner), nil)
	if err != nil {
		b.Fatalf("list shares: %v", err)
	}
	inCreated := map[string]bool{}
	for _, s := range created {
		inCreated[s.GetId().GetOpaqueId()] = true
	}
	lost := 0
	for _, r := range results {
		_, err := check.GetShare(userCtx(benchOwner), &collaboration.ShareReference{Spec: &collaboration.ShareReference_Id{Id: r.id}})
		inSpace := err == nil
		rs, err := check.ListReceivedShares(userCtx(r.grantee), nil, nil)
		if err != nil {
			b.Fatalf("list received shares: %v", err)
		}
		received := len(rs) == 1
		if inSpace == r.removed || received == r.removed || inCreated[r.id.GetOpaqueId()] == r.removed {
			lost++
		}
	}
	b.ReportMetric(float64(lost), "lost")
	if lost > 0 {
		b.Errorf("%d of %d shares are not as expected", lost, len(results))
	}
}

// BenchmarkGhosts lists the received shares of a user with 20 stale group
// entries: the group document has the share IDs, but the space document does
// not have the shares.
func BenchmarkGhosts(b *testing.B) {
	const ghosts = 20
	ctx := userCtx("ghost-user", "ghost-group")

	// makeGhosts adds 20 group shares and then empties their space document.
	makeGhosts := func(b *testing.B, f *benchFixture, seeder *jsoncs3.Manager, first int) {
		for i := first; i < first+ghosts; i++ {
			mustShare(b, seeder, resource("ghost-space", i), groupGrant("ghost-group"))
		}
		if err := f.store.SimpleUpload(context.Background(), "storages/"+benchStorageID+"/ghost-space.json", []byte(`{"Shares":{}}`)); err != nil {
			b.Fatal(err)
		}
	}

	// in-grace: the listing runs inside the grace period, so it only skips
	// the ghosts.
	b.Run("in-grace", func(b *testing.B) {
		b.ReportAllocs()
		f := newBenchFixture(b)
		f.seed()
		seeder := f.newPod(b)
		mustShare(b, seeder, resource("ghost-valid-space", 0), groupGrant("ghost-group"))
		makeGhosts(b, f, seeder, 0)
		m := f.newPod(b)
		mustListReceived(b, m, ctx, exactly(1))
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			mustListReceived(b, m, ctx, exactly(1))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})

	// after-grace: the grace period is over. Each iteration first makes 20
	// new ghosts (not measured), then lists. Today the listing removes the
	// ghosts. A design that removes them in the background moves this work
	// out of the listing.
	b.Run("after-grace", func(b *testing.B) {
		b.ReportAllocs()
		f := newBenchFixture(b)
		f.seed()
		seeder := f.newSeedPod(b)
		mustShare(b, seeder, resource("ghost-valid-space", 0), groupGrant("ghost-group"))
		m := f.newPod(b)
		setStaleGracePeriod(m, 0)
		mustListReceived(b, m, ctx, exactly(1))
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			makeGhosts(b, f, seeder, 1000+i*ghosts)
			b.StartTimer()
			mustListReceived(b, m, ctx, exactly(1))
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})
}

// BenchmarkExpired lists the received shares of a user with 10 valid shares
// and 10 expired shares. Each iteration first adds 10 new expired shares (not
// measured), then lists. Today the listing removes the expired shares and
// publishes one ShareExpired event for each.
func BenchmarkExpired(b *testing.B) {
	const valid, expiredShares = 10, 10
	b.ReportAllocs()
	f := newBenchFixture(b)
	f.seed()
	seeder := f.newSeedPod(b)
	for i := 0; i < valid; i++ {
		mustShare(b, seeder, resource(fmt.Sprintf("expired-space-%d", i%5), i), userGrant("expired-user"))
	}
	ctx := userCtx("expired-user")
	m := f.newPod(b)
	mustListReceived(b, m, ctx, exactly(valid))

	f.measure(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for j := 0; j < expiredShares; j++ {
			n := 1000 + i*expiredShares + j
			mustShare(b, seeder, resource(fmt.Sprintf("expired-space-%d", j%5), n), expired(userGrant("expired-user")))
		}
		b.StartTimer()
		mustListReceived(b, m, ctx, exactly(valid))
	}
	b.StopTimer()
	f.counts.report(b, b.N)
}

// BenchmarkAcceptGroupShare shares one resource with a group of 50 members.
// Then each member accepts the share, 10 at the same time. With auto-accept,
// the frontend does this for each member. One operation is one share and 50
// accepts.
func BenchmarkAcceptGroupShare(b *testing.B) {
	const (
		members  = 50
		parallel = 10
	)
	b.ReportAllocs()
	f := newBenchFixture(b)
	m := f.newPod(b)
	mask := &fieldmaskpb.FieldMask{Paths: []string{"state"}}
	f.measure(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := mustShare(b, m, resource(fmt.Sprintf("accept-space-%d", i%10), i), groupGrant("accept-group"))
		work := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < parallel; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for member := range work {
					ctx := userCtx(fmt.Sprintf("accept-member-%02d", member), "accept-group")
					rs, err := m.UpdateReceivedShare(ctx, &collaboration.ReceivedShare{Share: s, State: collaboration.ShareState_SHARE_STATE_ACCEPTED}, mask, nil)
					if err != nil {
						b.Errorf("accept: %v", err)
						continue
					}
					if rs.GetState() != collaboration.ShareState_SHARE_STATE_ACCEPTED {
						b.Errorf("accept: state is %v", rs.GetState())
					}
				}
			}()
		}
		for member := 0; member < members; member++ {
			work <- member
		}
		close(work)
		wg.Wait()
	}
	b.StopTimer()
	f.counts.report(b, b.N)
}

// BenchmarkListCreated lists the 100 shares that one user created in 10
// spaces.
func BenchmarkListCreated(b *testing.B) {
	const shares = 100
	f := newBenchFixture(b)
	f.seed()
	seeder := f.newPod(b)
	for i := 0; i < shares; i++ {
		mustShare(b, seeder, resource(fmt.Sprintf("created-space-%d", i%10), i), userGrant(fmt.Sprintf("created-user-%03d", i)))
	}
	list := func(b *testing.B, m *jsoncs3.Manager) {
		ss, err := m.ListShares(userCtx(benchOwner), nil)
		if err != nil {
			b.Fatalf("list shares: %v", err)
		}
		if len(ss) != shares {
			b.Fatalf("list shares: got %d, want %d", len(ss), shares)
		}
	}

	b.Run("warm", func(b *testing.B) {
		b.ReportAllocs()
		m := f.newPod(b)
		f.seed()
		list(b, m)
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			list(b, m)
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		pods := make([]*jsoncs3.Manager, b.N)
		for i := range pods {
			pods[i] = f.newPod(b)
		}
		f.measure(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			list(b, pods[i])
		}
		b.StopTimer()
		f.counts.report(b, b.N)
	})
}
