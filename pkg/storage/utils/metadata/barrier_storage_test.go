package metadata_test

import (
	"context"
	"testing"
	"time"

	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/stretchr/testify/require"
)

// TestBarrierStorage_BlocksForeverWhenFewerThanNCallersArrive proves that if
// fewer than n goroutines ever reach Upload, an arrived caller still gets an
// error back within a bounded time (barrierTimeout) instead of hanging
// forever, even though its own ctx (context.Background(), as both real call
// sites use) carries no deadline of its own.
func TestBarrierStorage_BlocksForeverWhenFewerThanNCallersArrive(t *testing.T) {
	dir := t.TempDir()
	disk, err := metadata.NewDiskStorage(dir)
	require.NoError(t, err)
	require.NoError(t, disk.Init(context.Background(), "test"))

	// n=5 but only 2 callers will ever arrive.
	bs := metadata.NewBarrierStorage(disk, 5)

	const callers = 2
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(idx int) {
			_, err := bs.Upload(context.Background(), metadata.UploadRequest{
				Path:    "f",
				Content: []byte("v1"),
			})
			done <- err
		}(i)
	}

	select {
	case err := <-done:
		require.Error(t, err, "an under-filled barrier must time out rather than succeed")
	case <-time.After(5 * time.Second):
		t.Fatal("Upload for an arrived caller did not return within 5s -- " +
			"the barrier's internal timeout did not fire")
	}
}

// TestBarrierStorage_SimpleUploadBypassesBarrier proves that SimpleUpload,
// which BarrierStorage does not override, promotes straight through the
// embedded Storage instead of counting toward the n-arrival threshold.
func TestBarrierStorage_SimpleUploadBypassesBarrier(t *testing.T) {
	dir := t.TempDir()
	disk, err := metadata.NewDiskStorage(dir)
	require.NoError(t, err)
	require.NoError(t, disk.Init(context.Background(), "test"))

	// Held as the metadata.Storage interface, like a real caller would.
	var s metadata.Storage = metadata.NewBarrierStorage(disk, 2)

	// Two concurrent SimpleUpload calls -- if they count toward the n=2
	// barrier the same way Upload does, both release together once the
	// second arrives (same pattern as the Upload-only barrier test).
	done := make(chan error, 2)
	for i, path := range []string{"a", "b"} {
		go func(idx int, p string) {
			done <- s.SimpleUpload(context.Background(), p, []byte("v"))
		}(i, path)
	}

	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-timeout:
			t.Fatal("concurrent SimpleUpload calls did not both return -- " +
				"SimpleUpload bypasses BarrierStorage's barrier entirely (promotes to the " +
				"embedded Storage) instead of counting toward arrived")
		}
	}
}
