package helpers

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/owncloud/reva/v2/pkg/errtypes"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

func isConflict(err error) bool {
	switch err.(type) {
	case errtypes.Aborted, errtypes.PreconditionFailed, errtypes.AlreadyExists:
		return true
	}
	return false
}

func TestCASStorageRefusesASecondCreate(t *testing.T) {
	s := NewCASStorage()
	ctx := context.Background()
	if _, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("a"), IfNoneMatch: []string{"*"}}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("b"), IfNoneMatch: []string{"*"}})
	if !isConflict(err) {
		t.Fatalf("second create: want a conflict, got %v", err)
	}
}

// metadata.Disk accepts an If-Match on a missing file. The fake must refuse
// it, because the file that the writer read is gone.
func TestCASStorageRefusesIfMatchOnAMissingFile(t *testing.T) {
	s := NewCASStorage()
	_, err := s.Upload(context.Background(), metadata.UploadRequest{Path: "doc.json", Content: []byte("a"), IfMatchEtag: "old"})
	if !isConflict(err) {
		t.Fatalf("want a conflict, got %v", err)
	}
}

func TestCASStorageRefusesAStaleIfMatch(t *testing.T) {
	s := NewCASStorage()
	ctx := context.Background()
	first, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("b"), IfMatchEtag: first.Etag}); err != nil {
		t.Fatalf("write with the current etag: %v", err)
	}
	_, err = s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("c"), IfMatchEtag: first.Etag})
	if !isConflict(err) {
		t.Fatalf("write with a stale etag: want a conflict, got %v", err)
	}
}

// metadata.Disk computes the etag from mtime and size. Two writes of the same
// size can then get the same etag. The fake gives each write a new etag.
func TestCASStorageGivesEachWriteANewEtag(t *testing.T) {
	s := NewCASStorage()
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		res, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("same")})
		if err != nil {
			t.Fatal(err)
		}
		if seen[res.Etag] {
			t.Fatalf("etag %q came two times", res.Etag)
		}
		seen[res.Etag] = true
	}
}

func TestCASStorageDownloadResults(t *testing.T) {
	s := NewCASStorage()
	ctx := context.Background()
	if _, err := s.Download(ctx, metadata.DownloadRequest{Path: "doc.json"}); err == nil {
		t.Fatal("missing file: want NotFound")
	} else if _, ok := err.(errtypes.NotFound); !ok {
		t.Fatalf("missing file: want NotFound, got %T", err)
	}
	up, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("abc")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Download(ctx, metadata.DownloadRequest{Path: "doc.json", IfNoneMatch: []string{up.Etag}})
	if _, ok := err.(errtypes.NotModified); !ok {
		t.Fatalf("current etag: want NotModified, got %v", err)
	}
	res, err := s.Download(ctx, metadata.DownloadRequest{Path: "doc.json"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Content) != "abc" || res.Etag != up.Etag {
		t.Fatalf("got %q with etag %q", res.Content, res.Etag)
	}
	// The caller owns the returned bytes.
	res.Content[0] = 'x'
	res, _ = s.Download(ctx, metadata.DownloadRequest{Path: "doc.json"})
	if string(res.Content) != "abc" {
		t.Fatalf("a change of the returned bytes changed the file: %q", res.Content)
	}
}

// Many writers increment a counter with read, change, and If-Match write. No
// increment may get lost.
func TestCASStorageLosesNoIncrement(t *testing.T) {
	s := NewCASStorage()
	ctx := context.Background()
	if _, err := s.Upload(ctx, metadata.UploadRequest{Path: "n", Content: []byte("0")}); err != nil {
		t.Fatal(err)
	}
	const writers, increments = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < increments; i++ {
				for {
					res, err := s.Download(ctx, metadata.DownloadRequest{Path: "n"})
					if err != nil {
						t.Error(err)
						return
					}
					n, _ := strconv.Atoi(string(res.Content))
					_, err = s.Upload(ctx, metadata.UploadRequest{Path: "n", Content: []byte(strconv.Itoa(n + 1)), IfMatchEtag: res.Etag})
					if err == nil {
						break
					}
					if !isConflict(err) {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	res, _ := s.Download(ctx, metadata.DownloadRequest{Path: "n"})
	if string(res.Content) != strconv.Itoa(writers*increments) {
		t.Fatalf("counter = %s, want %d", res.Content, writers*increments)
	}
}
