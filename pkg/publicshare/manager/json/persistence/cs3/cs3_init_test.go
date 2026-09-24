package cs3_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/owncloud/reva/v2/pkg/publicshare/manager/json/persistence/cs3"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
	"github.com/stretchr/testify/require"
)

// slowStatStorage wraps a real metadata.Storage but adds a fixed delay to
// Stat, standing in for a slow network round trip (e.g. against a loaded or
// stalling storage-system). It lets a test observe whether a warm Init
// overlaps with a concurrent, in-flight Read instead of queuing behind
// whatever lock Read holds for the duration of that delay.
type slowStatStorage struct {
	metadata.Storage
	delay time.Duration
}

func (s *slowStatStorage) Stat(ctx context.Context, path string) (*provider.ResourceInfo, error) {
	time.Sleep(s.delay)
	return s.Storage.Stat(ctx, path)
}

// TestInitDoesNotQueueBehindRead guards against a regression where a warm
// Init (i.e. one called after the persistence layer already reports itself
// initialized) shares cs3's mu with Read, and so queues behind whatever Read
// is doing - even though a warm Init only needs to check a bool. That would
// defeat the point of removing the manager's own equivalent lock (see
// json.go's init): moving that lock out of the way only helps if calls
// coming in right behind it don't just pile up on this one instead.
func TestInitDoesNotQueueBehindRead(t *testing.T) {
	tmpdir, err := os.MkdirTemp("", "cs3-init-test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	disk, err := metadata.NewDiskStorage(tmpdir)
	require.NoError(t, err)

	const delay = 200 * time.Millisecond
	slow := &slowStatStorage{Storage: disk, delay: delay}

	p := cs3.New(slow)
	require.NoError(t, p.Init(context.Background()))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = p.Read(context.Background())
	}()

	// Give the Read goroutine time to be inside its (slow) Stat call,
	// holding mu, before Init races it.
	time.Sleep(delay / 5)

	initStart := time.Now()
	require.NoError(t, p.Init(context.Background()))
	initElapsed := time.Since(initStart)

	wg.Wait()

	if max := delay / 2; initElapsed > max {
		t.Fatalf("warm Init queued behind a concurrent Read: took %s while Read's own Stat delay is %s (want < %s)",
			initElapsed, delay, max)
	}
}
