package metadata_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/owncloud/reva/v2/pkg/storage/cache"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/metadata"
	"github.com/pkg/xattr"
)

func newCachedTestBackend(t *testing.T) (metadata.Backend, string) {
	t.Helper()
	tmpdir := t.TempDir()
	backend := metadata.NewMessagePackBackend(tmpdir, cache.Config{
		Store:    "memory",
		Database: tmpdir,
		Table:    t.Name(),
	})
	file := filepath.Join(tmpdir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return backend, file
}

func mustGet(t *testing.T, b metadata.Backend, path, key string) string {
	t.Helper()
	v, err := b.Get(context.Background(), path, key)
	if err != nil {
		t.Fatalf("Get(%q) failed: %v", key, err)
	}
	return string(v)
}

func TestMessagePackBackendCachedWritePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("set", func(t *testing.T) {
		b, file := newCachedTestBackend(t)
		if err := b.Set(ctx, file, "foo", []byte("1")); err != nil {
			t.Fatal(err)
		}
		_ = mustGet(t, b, file, "foo") // warm the cache
		if err := b.Set(ctx, file, "foo", []byte("2")); err != nil {
			t.Fatal(err)
		}
		if v := mustGet(t, b, file, "foo"); v != "2" {
			t.Fatalf("expected updated value, got %q", v)
		}
		if err := b.SetMultiple(ctx, file, map[string][]byte{"foo": []byte("3"), "bar": []byte("4")}, false); err != nil {
			t.Fatal(err)
		}
		if v := mustGet(t, b, file, "foo") + mustGet(t, b, file, "bar"); v != "34" {
			t.Fatalf("expected updated values, got %q", v)
		}
	})

	t.Run("remove", func(t *testing.T) {
		b, file := newCachedTestBackend(t)
		if err := b.SetMultiple(ctx, file, map[string][]byte{"foo": []byte("1"), "bar": []byte("2")}, true); err != nil {
			t.Fatal(err)
		}
		_ = mustGet(t, b, file, "foo")
		if err := b.Remove(ctx, file, "foo", true); err != nil {
			t.Fatal(err)
		}
		_, err := b.Get(ctx, file, "foo")
		if !errors.Is(err, xattr.ENOATTR) {
			t.Fatalf("expected ENOATTR after remove, got %v", err)
		}
		if v := mustGet(t, b, file, "bar"); v != "2" {
			t.Fatalf("unexpected value %q", v)
		}
	})

	t.Run("lock and write", func(t *testing.T) {
		b, file := newCachedTestBackend(t)
		if err := b.Set(ctx, file, "foo", []byte("1")); err != nil {
			t.Fatal(err)
		}
		_ = mustGet(t, b, file, "foo")
		unlock, err := b.Lock(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.SetMultiple(ctx, file, map[string][]byte{"foo": []byte("2")}, false); err != nil {
			t.Fatal(err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
		if v := mustGet(t, b, file, "foo"); v != "2" {
			t.Fatalf("expected updated value, got %q", v)
		}
	})

	t.Run("purge", func(t *testing.T) {
		b, file := newCachedTestBackend(t)
		if err := b.Set(ctx, file, "foo", []byte("1")); err != nil {
			t.Fatal(err)
		}
		_ = mustGet(t, b, file, "foo")
		if err := b.Purge(ctx, file); err != nil {
			t.Fatal(err)
		}
		attrs, err := b.All(ctx, file)
		if err != nil {
			t.Fatal(err)
		}
		if len(attrs) != 0 {
			t.Fatalf("expected no attributes after purge, got %q", attrs)
		}
	})

	t.Run("rename", func(t *testing.T) {
		b, file := newCachedTestBackend(t)
		if err := b.Set(ctx, file, "foo", []byte("1")); err != nil {
			t.Fatal(err)
		}
		_ = mustGet(t, b, file, "foo")
		newFile := file + "-renamed"
		if err := os.Rename(file, newFile); err != nil {
			t.Fatal(err)
		}
		if err := b.Rename(file, newFile); err != nil {
			t.Fatal(err)
		}
		if v := mustGet(t, b, newFile, "foo"); v != "1" {
			t.Fatalf("expected attributes at the new path, got %q", v)
		}
		if _, err := b.All(ctx, file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected the old path to be gone, got %v", err)
		}
		// a new node at the old path must not inherit the old attributes
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		attrs, err := b.All(ctx, file)
		if err != nil {
			t.Fatal(err)
		}
		if len(attrs) != 0 {
			t.Fatalf("expected no attributes at the old path, got %q", attrs)
		}
	})
}

func TestMessagePackBackendReturnedAttributesAreCopies(t *testing.T) {
	ctx := context.Background()
	b, file := newCachedTestBackend(t)
	if err := b.SetMultiple(ctx, file, map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}, true); err != nil {
		t.Fatal(err)
	}

	attrs, err := b.All(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	attrs["foo"][0] = 'X'
	attrs["new"] = []byte("1")
	delete(attrs, "baz")
	v, err := b.Get(ctx, file, "baz")
	if err != nil {
		t.Fatal(err)
	}
	v[0] = 'Y'

	attrs, err = b.All(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 2 || string(attrs["foo"]) != "bar" || string(attrs["baz"]) != "qux" {
		t.Fatalf("cached attributes were modified through a returned value: %q", attrs)
	}
}

func TestMessagePackBackendConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	b, file := newCachedTestBackend(t)
	if err := b.Set(ctx, file, "n", []byte("0")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if i%4 == 0 {
					if err := b.SetMultiple(ctx, file, map[string][]byte{"n": []byte(fmt.Sprint(g, i)), "m": []byte(fmt.Sprint(g, i))}, true); err != nil {
						t.Error(err)
					}
					continue
				}
				attrs, err := b.All(ctx, file)
				if err != nil {
					t.Error(err)
					continue
				}
				if m, ok := attrs["m"]; ok && string(m) != string(attrs["n"]) {
					t.Errorf("inconsistent attributes %q", attrs)
				}
				attrs["n"] = append(attrs["n"], 'x')
			}
		}(g)
	}
	wg.Wait()
}
