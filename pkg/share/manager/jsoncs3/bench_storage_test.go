package jsoncs3_test

import (
	"context"
	"testing"

	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// The benchmark results depend on the counts of countingStorage. Check that
// it counts each kind of call correctly.
func TestCountingStorageCountsByResult(t *testing.T) {
	counts := &callCounts{}
	s := &countingStorage{Storage: newCASStorage(), counts: counts}
	ctx := context.Background()

	if _, err := s.Download(ctx, metadata.DownloadRequest{Path: "doc.json"}); err == nil {
		t.Fatal("download of a missing file: want an error")
	}
	res, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("{}"), IfNoneMatch: []string{"*"}})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := s.Upload(ctx, metadata.UploadRequest{Path: "doc.json", Content: []byte("{}"), IfNoneMatch: []string{"*"}}); err == nil {
		t.Fatal("second create: want a conflict")
	}
	if _, err := s.Download(ctx, metadata.DownloadRequest{Path: "doc.json"}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if _, err := s.Download(ctx, metadata.DownloadRequest{Path: "doc.json", IfNoneMatch: []string{res.Etag}}); err == nil {
		t.Fatal("download with the current etag: want NotModified")
	}
	if err := s.MakeDirIfNotExist(ctx, "dir"); err != nil {
		t.Fatalf("make dir: %v", err)
	}

	for name, got := range map[string]int64{
		"download404":    counts.download404.Load(),
		"upload":         counts.upload.Load(),
		"uploadConflict": counts.uploadConflict.Load(),
		"download200":    counts.download200.Load(),
		"download304":    counts.download304.Load(),
		"other":          counts.other.Load(),
	} {
		if got != 1 {
			t.Errorf("%s = %d, want 1", name, got)
		}
	}
	if got := counts.total(); got != 6 {
		t.Errorf("total = %d, want 6", got)
	}
}
