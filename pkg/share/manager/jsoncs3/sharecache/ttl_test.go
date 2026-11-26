package sharecache_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/sharecache"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

type downloadCounter struct {
	metadata.Storage
	downloads atomic.Int32
}

func (d *downloadCounter) Download(ctx context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	d.downloads.Add(1)
	return d.Storage.Download(ctx, req)
}

// A cache with a TTL trusts its entry inside the TTL. This is the cache of the
// group documents. Other pods see a new share up to the TTL later.
func TestListInsideTheTTLMakesNoDownload(t *testing.T) {
	cas := helpers.NewCASStorage()
	storage := &downloadCounter{Storage: cas} // the storage of the reader only
	ctx := context.Background()
	id1 := shareid.Encode("storage", "space", "s1")
	id2 := shareid.Encode("storage", "space", "s2")
	reader := sharecache.New(storage, "groups", "received.json", time.Hour)
	writer := sharecache.New(cas, "groups", "received.json", 0)
	require.NoError(t, writer.Add(ctx, "students", id1))

	list, err := reader.List(ctx, "students")
	require.NoError(t, err)
	require.Len(t, list["storage:space"].IDs, 1)
	before := storage.downloads.Load()

	require.NoError(t, writer.Add(ctx, "students", id2)) // another pod adds a share
	list, err = reader.List(ctx, "students")
	require.NoError(t, err)

	assert.Len(t, list["storage:space"].IDs, 1, "inside the TTL the reader does not see the new share")
	assert.EqualValues(t, 0, storage.downloads.Load()-before, "the reader makes no download inside the TTL")
}

func TestWriterWithATTLReadsItsOwnWrite(t *testing.T) {
	storage := helpers.NewCASStorage()
	ctx := context.Background()
	c := sharecache.New(storage, "groups", "received.json", time.Hour)
	_, err := c.List(ctx, "students")
	require.NoError(t, err)

	require.NoError(t, c.Add(ctx, "students", shareid.Encode("storage", "space", "s1")))

	list, err := c.List(ctx, "students")
	require.NoError(t, err)
	assert.Len(t, list["storage:space"].IDs, 1)
}
