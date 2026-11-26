package metadata_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/owncloud/reva/v2/pkg/storage/cache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
)

// benchNodeAttributes returns a set of attributes resembling a typical
// decomposedfs file node.
func benchNodeAttributes() map[string][]byte {
	return map[string][]byte{
		"user.ocis.type":                []byte("1"),
		"user.ocis.id":                  []byte("4c510ada-c86b-4815-8820-42cdf82c3d51"),
		"user.ocis.parentid":            []byte("2fc0f2a4-1b65-4e4b-9f5e-6f0a6b1f8c11"),
		"user.ocis.name":                []byte("quarterly-report-final-v3.docx"),
		"user.ocis.blobid":              []byte("8b7e4c1a-0f8e-4a3b-9c47-1d7a0e5b2f33"),
		"user.ocis.blobsize":            []byte("1048576"),
		"user.ocis.mtime":               []byte("2026-11-23T10:11:12.123456789Z"),
		"user.ocis.tmtime":              []byte("2026-11-23T10:11:12.123456789Z"),
		"user.ocis.treesize":            []byte("1048576"),
		"user.ocis.propagation":         []byte("1"),
		"user.ocis.cs.sha1":             []byte("0123456789abcdef0123"),
		"user.ocis.cs.md5":              []byte("0123456789abcdef"),
		"user.ocis.cs.adler32":          []byte("0123"),
		"user.ocis.owner.id":            []byte("f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c"),
		"user.ocis.owner.idp":           []byte("https://idp.example.org"),
		"user.ocis.owner.type":          []byte("primary"),
		"user.ocis.space.id":            []byte("f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c"),
		"user.ocis.grant.u:alice":       []byte("\x00\x01\x02 some serialized grant payload of moderate length"),
		"user.ocis.grant.g:engineering": []byte("\x00\x01\x02 some serialized grant payload of moderate length"),
		"user.ocis.md.some.key":         []byte("some arbitrary metadata value"),
		"user.ocis.fav.u:alice":         []byte("1"),
	}
}

func setupBenchBackend(b *testing.B, store string) (metadata.Backend, string) {
	b.Helper()
	tmpdir := b.TempDir()
	backend := metadata.NewMessagePackBackend(tmpdir, cache.Config{
		Store:    store,
		Database: tmpdir,
		Table:    b.Name(),
	})
	file := filepath.Join(tmpdir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		b.Fatal(err)
	}
	if err := backend.SetMultiple(context.Background(), file, benchNodeAttributes(), true); err != nil {
		b.Fatal(err)
	}
	return backend, file
}

// BenchmarkMessagePackBackendAllCacheHit measures All() when the attributes are
// served from the (memory) metadata cache.
func BenchmarkMessagePackBackendAllCacheHit(b *testing.B) {
	backend, file := setupBenchBackend(b, "memory")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		attrs, err := backend.All(ctx, file)
		if err != nil || len(attrs) == 0 {
			b.Fatal(err)
		}
	}
}

// BenchmarkMessagePackBackendGetCacheHit measures Get() of a single attribute
// when the attributes are served from the (memory) metadata cache.
func BenchmarkMessagePackBackendGetCacheHit(b *testing.B) {
	backend, file := setupBenchBackend(b, "memory")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := backend.Get(ctx, file, "user.ocis.name")
		if err != nil || len(v) == 0 {
			b.Fatal(err)
		}
	}
}

// BenchmarkMessagePackBackendAllCacheMiss measures All() when there is no
// cache (noop store), i.e. every call reads and decodes the .mpk file.
func BenchmarkMessagePackBackendAllCacheMiss(b *testing.B) {
	backend, file := setupBenchBackend(b, "noop")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		attrs, err := backend.All(ctx, file)
		if err != nil || len(attrs) == 0 {
			b.Fatal(err)
		}
	}
}

// BenchmarkMessagePackBackendAllCacheHitParallel measures All() cache hits
// from many goroutines, for a single hot node and for many distinct nodes.
func BenchmarkMessagePackBackendAllCacheHitParallel(b *testing.B) {
	for _, nodes := range []int{1, 1024} {
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			backend, file := setupBenchBackend(b, "memory")
			files := make([]string, nodes)
			for i := range files {
				files[i] = fmt.Sprintf("%s-%d", file, i)
				if err := os.WriteFile(files[i], nil, 0600); err != nil {
					b.Fatal(err)
				}
				if err := backend.SetMultiple(context.Background(), files[i], benchNodeAttributes(), true); err != nil {
					b.Fatal(err)
				}
			}
			ctx := context.Background()
			var next atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := int(next.Add(1))
				for pb.Next() {
					i++
					attrs, err := backend.All(ctx, files[i%nodes])
					if err != nil || len(attrs) == 0 {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
