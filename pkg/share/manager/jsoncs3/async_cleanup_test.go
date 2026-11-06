package jsoncs3_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	userv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// latencyStorage sleeps per Download/Upload call to make serialized vs parallel timing observable.
type latencyStorage struct {
	mu    sync.Mutex
	files map[string][]byte

	latency time.Duration
}

func (f *latencyStorage) fileFor(path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[path]
}

func (f *latencyStorage) Backend() string                                    { return "fake" }
func (f *latencyStorage) Init(context.Context, string) error                 { return nil }
func (f *latencyStorage) SimpleUpload(context.Context, string, []byte) error { return nil }
func (f *latencyStorage) SimpleDownload(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (f *latencyStorage) Delete(context.Context, string) error { return nil }
func (f *latencyStorage) Stat(context.Context, string) (*provider.ResourceInfo, error) {
	return nil, nil
}
func (f *latencyStorage) ReadDir(context.Context, string) ([]string, error) { return nil, nil }
func (f *latencyStorage) ListDir(context.Context, string) ([]*provider.ResourceInfo, error) {
	return nil, nil
}
func (f *latencyStorage) CreateSymlink(context.Context, string, string) error { return nil }
func (f *latencyStorage) ResolveSymlink(context.Context, string) (string, error) {
	return "", nil
}
func (f *latencyStorage) MakeDirIfNotExist(context.Context, string) error { return nil }
func (f *latencyStorage) WasRecentlyDeleted(context.Context, string) (bool, error) {
	return false, nil
}

func (f *latencyStorage) Download(_ context.Context, req metadata.DownloadRequest) (*metadata.DownloadResponse, error) {
	time.Sleep(f.latency)
	content := f.fileFor(req.Path)
	if content == nil {
		return nil, errtypes.NotFound(req.Path)
	}
	return &metadata.DownloadResponse{Content: content, Etag: "etag"}, nil
}

func (f *latencyStorage) Upload(_ context.Context, req metadata.UploadRequest) (*metadata.UploadResponse, error) {
	time.Sleep(f.latency)
	f.mu.Lock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[req.Path] = req.Content
	f.mu.Unlock()
	return &metadata.UploadResponse{Etag: "etag"}, nil
}

// seedOrphanedSpaces seeds orphaned entries directly in UserReceivedStates, bypassing the per-group list loop.
func seedOrphanedSpaces(t *testing.T, m *jsoncs3.Manager, numSpaces int) (userID string) {
	t.Helper()
	userID = "6d2f6853-bbf4-47e9-952b-4fff9da08770"
	ctx := context.Background()
	for i := 0; i < numSpaces; i++ {
		spaceID := fmt.Sprintf("space-%d", i)
		shareOpaqueID := shareid.Encode("storage1", spaceID, fmt.Sprintf("share-%d", i))
		rs := &collaboration.ReceivedShare{
			Share: &collaboration.Share{Id: &collaboration.ShareId{OpaqueId: shareOpaqueID}},
			State: collaboration.ShareState_SHARE_STATE_PENDING,
		}
		if err := m.UserReceivedStates.Add(ctx, userID, "storage1"+shareid.IDDelimiter+spaceID, rs); err != nil {
			t.Fatalf("seeding space %s: %v", spaceID, err)
		}
	}
	return userID
}

// TestListReceivedSharesOrphanCleanupDoesNotBlockResponse proves response latency stays constant regardless of N.
func TestListReceivedSharesOrphanCleanupDoesNotBlockResponse(t *testing.T) {
	const (
		numSpaces = 10
		latency   = 200 * time.Millisecond
	)

	storage := &latencyStorage{latency: latency}
	m, err := jsoncs3.New(storage, nil, 3600, nil, numSpaces)
	if err != nil {
		t.Fatalf("jsoncs3.New: %v", err)
	}

	userID := seedOrphanedSpaces(t, m, numSpaces)
	user := &userv1beta1.User{
		Id: &userv1beta1.UserId{OpaqueId: userID, Type: userv1beta1.UserType_USER_TYPE_PRIMARY},
	}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)

	start := time.Now()
	shares, err := m.ListReceivedShares(ctx, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ListReceivedShares: %v", err)
	}
	if len(shares) != 0 {
		t.Fatalf("expected 0 shares (all orphaned), got %d", len(shares))
	}

	// a blocking implementation would take >= numSpaces round trips; an
	// async one stays within a small constant number of them regardless of N.
	wantMax := 3 * latency
	if elapsed >= wantMax {
		t.Fatalf("ListReceivedShares took %v for %d orphaned shares (wanted < %v) -- cleanup writes are blocking the response again", elapsed, numSpaces, wantMax)
	}
}

// TestListReceivedSharesOrphanCleanupEventuallyCompletes proves the async removal still lands.
func TestListReceivedSharesOrphanCleanupEventuallyCompletes(t *testing.T) {
	const latency = 10 * time.Millisecond

	storage := &latencyStorage{latency: latency}
	m, err := jsoncs3.New(storage, nil, 3600, nil, 1)
	if err != nil {
		t.Fatalf("jsoncs3.New: %v", err)
	}

	userID := seedOrphanedSpaces(t, m, 1)
	user := &userv1beta1.User{
		Id: &userv1beta1.UserId{OpaqueId: userID, Type: userv1beta1.UserType_USER_TYPE_PRIMARY},
	}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)

	if _, err := m.ListReceivedShares(ctx, nil, nil); err != nil {
		t.Fatalf("ListReceivedShares: %v", err)
	}

	ssid := "storage1" + shareid.IDDelimiter + "space-0"
	deadline := time.Now().Add(2 * time.Second)
	for {
		spaces, err := m.UserReceivedStates.List(ctx, userID)
		if err != nil {
			t.Fatalf("UserReceivedStates.List: %v", err)
		}
		if _, ok := spaces[ssid]; !ok {
			return // cleaned up
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphaned share %s was not cleaned up from UserReceivedStates within 2s", ssid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
