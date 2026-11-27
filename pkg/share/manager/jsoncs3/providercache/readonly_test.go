package providercache_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	userv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/providercache"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
)

// tag and setTag keep a string in a field of the share.
func tag(s *collaboration.Share) string       { return s.GetCreator().GetOpaqueId() }
func setTag(s *collaboration.Share, v string) { s.Creator = &userv1beta1.UserId{OpaqueId: v} }

func shareWithID(id string) *collaboration.Share {
	return &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: id}}
}

// A share that ListSpace returns is read-only. A parallel Update of the same
// space must not change it. Run with go test -race.
func TestUpdateDoesNotChangeAShareThatAReaderHolds(t *testing.T) {
	storage := helpers.NewCASStorage()
	c := providercache.New(storage, time.Second)
	ctx := context.Background()
	require.NoError(t, c.Add(ctx, "storage", "space", "s1", shareWithID("s1")))
	held, err := c.ListSpace(ctx, "storage", "space")
	require.NoError(t, err)
	heldShare := held.Shares["s1"]

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = heldShare.GetExpiration()
			_ = len(held.Shares)
		}
	}()
	for i := 0; i < 20; i++ {
		_, err := c.Update(ctx, "storage", "space", "s1", func(s *collaboration.Share) {
			setTag(s, strconv.Itoa(i))
		})
		require.NoError(t, err)
	}
	wg.Wait()

	assert.Empty(t, tag(heldShare), "the share of the reader changed")
	assert.Len(t, held.Shares, 1)
}

// The caller can change its share after Add. The cache keeps its own copy.
func TestAddKeepsACopyOfTheShare(t *testing.T) {
	c := providercache.New(helpers.NewCASStorage(), time.Second)
	ctx := context.Background()
	s := shareWithID("s1")
	require.NoError(t, c.Add(ctx, "storage", "space", "s1", s))

	setTag(s, "changed by the caller")

	got, err := c.Get(ctx, "storage", "space", "s1", false)
	require.NoError(t, err)
	assert.Empty(t, tag(got))
}

// Two pods update different shares of one space in parallel. No change is lost.
func TestTwoPodsLoseNoUpdate(t *testing.T) {
	storage := helpers.NewCASStorage()
	pods := []providercache.Cache{providercache.New(storage, time.Second), providercache.New(storage, time.Second)}
	ctx := context.Background()
	const shares = 10
	for i := 0; i < shares; i++ {
		id := "s" + strconv.Itoa(i)
		require.NoError(t, pods[0].Add(ctx, "storage", "space", id, shareWithID(id)))
	}

	var wg sync.WaitGroup
	for pod := range pods {
		for i := 0; i < shares; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := "s" + strconv.Itoa(i)
				_, err := pods[pod].Update(ctx, "storage", "space", id, func(s *collaboration.Share) {
					setTag(s, tag(s)+strconv.Itoa(pod))
				})
				assert.NoError(t, err)
			}()
		}
	}
	wg.Wait()

	fresh := providercache.New(storage, time.Second)
	list, err := fresh.ListSpace(ctx, "storage", "space")
	require.NoError(t, err)
	require.Len(t, list.Shares, shares)
	for id, s := range list.Shares {
		assert.ElementsMatch(t, []string{"0", "1"}, []string{tag(s)[:1], tag(s)[1:]}, "a change of a pod is lost for %s", id)
	}
}

// Only one pod reports the removal of a share.
func TestOnlyOnePodRemovesAShare(t *testing.T) {
	storage := helpers.NewCASStorage()
	podA, podB := providercache.New(storage, time.Second), providercache.New(storage, time.Second)
	ctx := context.Background()
	require.NoError(t, podA.Add(ctx, "storage", "space", "s1", shareWithID("s1")))
	_, err := podB.ListSpace(ctx, "storage", "space") // both pods have the share in memory
	require.NoError(t, err)

	var wg sync.WaitGroup
	removed := make([][]string, 2)
	for i, pod := range []providercache.Cache{podA, podB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			removed[i], err = pod.RemoveMany(ctx, "storage", "space", []string{"s1"})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, len(removed[0])+len(removed[1]), "removed: %v", removed)
}
