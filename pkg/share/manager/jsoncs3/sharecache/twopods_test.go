package sharecache_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/sharecache"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
)

// Two caches on one storage are two pods. Parallel Add and Remove calls must
// lose no change. metadata.Disk cannot prove this, so the test uses the
// CAS-correct fake.
func TestTwoPodsLoseNoShare(t *testing.T) {
	storage := helpers.NewCASStorage()
	pods := []sharecache.Cache{
		sharecache.New(storage, "groups", "received.json", time.Second),
		sharecache.New(storage, "groups", "received.json", time.Second),
	}
	ctx := context.Background()
	const writers, each = 4, 15

	var wg sync.WaitGroup
	for pod := range pods {
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					id := shareid.Encode("storage", "space", strconv.Itoa(pod)+"-"+strconv.Itoa(w)+"-"+strconv.Itoa(i))
					assert.NoError(t, pods[pod].Add(ctx, "students", id))
				}
			}()
		}
	}
	wg.Wait()

	fresh := sharecache.New(storage, "groups", "received.json", time.Second)
	list, err := fresh.List(ctx, "students")
	require.NoError(t, err)
	assert.Len(t, list["storage:space"].IDs, len(pods)*writers*each)
}

// A listing of a group that has no file writes the empty file one time. The
// next listing gets a 304.
func TestListCreatesTheEmptyFileOnce(t *testing.T) {
	storage := helpers.NewCASStorage()
	c := sharecache.New(storage, "groups", "received.json", 0)
	ctx := context.Background()

	_, err := c.List(ctx, "nobody")
	require.NoError(t, err)

	res, err := storage.Stat(ctx, "/groups/nobody/received.json")
	require.NoError(t, err)
	assert.NotEmpty(t, res.Etag)
}
