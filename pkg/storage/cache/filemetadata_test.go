package cache

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shamaton/msgpack/v2"
	microstore "go-micro.dev/v4/store"
)

// newTestFileMetadataCache returns a file metadata cache backed by the given
// store which counts the number of msgpack decodes it performs.
func newTestFileMetadataCache(t *testing.T, s microstore.Store) (*fileMetadataCache, *atomic.Int64) {
	t.Helper()
	c := NewFileMetadataCache(Config{Store: "memory", Database: "db", Table: "table"}).(*fileMetadataCache)
	if s != nil {
		c.s = s
	}
	decodes := &atomic.Int64{}
	c.decode = func(b []byte, dest any) error {
		decodes.Add(1)
		return msgpack.Unmarshal(b, dest)
	}
	return c, decodes
}

// writeRaw writes the attributes directly into the underlying store, simulating
// a write by another process sharing the same (distributed) store.
func writeRaw(t *testing.T, c *fileMetadataCache, key string, attrs map[string][]byte) {
	t.Helper()
	b, err := msgpack.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.s.Write(&microstore.Record{Key: key, Value: b}, microstore.WriteTo(c.database, c.table)); err != nil {
		t.Fatal(err)
	}
}

func pull(t *testing.T, c *fileMetadataCache, key string) map[string][]byte {
	t.Helper()
	attrs := map[string][]byte{}
	if err := c.PullFromCache(key, &attrs); err != nil {
		t.Fatalf("PullFromCache(%q) failed: %v", key, err)
	}
	return attrs
}

func assertAttrs(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d attributes, want %d: %q", len(got), len(want), got)
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			t.Fatalf("attribute %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestFileMetadataCacheHitDoesNotDecode(t *testing.T) {
	c, decodes := newTestFileMetadataCache(t, nil)
	want := map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}
	writeRaw(t, c, "node", want)

	for i := 0; i < 5; i++ {
		assertAttrs(t, pull(t, c, "node"), want)
	}
	if n := decodes.Load(); n != 1 {
		t.Fatalf("expected exactly 1 decode for 5 pulls of unchanged data, got %d", n)
	}
}

func TestFileMetadataCachePushPopulatesDecodedLayer(t *testing.T) {
	c, decodes := newTestFileMetadataCache(t, nil)
	want := map[string][]byte{"foo": []byte("bar")}
	if err := c.PushToCache("node", want); err != nil {
		t.Fatal(err)
	}
	assertAttrs(t, pull(t, c, "node"), want)
	if n := decodes.Load(); n != 0 {
		t.Fatalf("expected no decode after push, got %d", n)
	}
}

func TestFileMetadataCacheMiss(t *testing.T) {
	c, _ := newTestFileMetadataCache(t, nil)
	attrs := map[string][]byte{}
	if err := c.PullFromCache("missing", &attrs); err == nil {
		t.Fatal("expected an error for a missing key")
	}
}

// TestFileMetadataCacheSharedStoreCoherence simulates two processes sharing
// one store: each has its own decoded layer, writes by one must be visible to
// the other immediately.
func TestFileMetadataCacheSharedStoreCoherence(t *testing.T) {
	shared := microstore.NewMemoryStore()
	a, _ := newTestFileMetadataCache(t, shared)
	b, _ := newTestFileMetadataCache(t, shared)

	v1 := map[string][]byte{"foo": []byte("v1")}
	v2 := map[string][]byte{"foo": []byte("v2")}
	if err := a.PushToCache("node", v1); err != nil {
		t.Fatal(err)
	}
	assertAttrs(t, pull(t, a, "node"), v1)
	assertAttrs(t, pull(t, b, "node"), v1)

	// b writes, a must see it
	if err := b.PushToCache("node", v2); err != nil {
		t.Fatal(err)
	}
	assertAttrs(t, pull(t, a, "node"), v2)

	// a raw write that bypasses both decoded layers must be seen by both
	v3 := map[string][]byte{"foo": []byte("v3"), "new": []byte("x")}
	writeRaw(t, a, "node", v3)
	assertAttrs(t, pull(t, a, "node"), v3)
	assertAttrs(t, pull(t, b, "node"), v3)

	// b deletes, a must see the miss
	if err := b.RemoveMetadata("node"); err != nil {
		t.Fatal(err)
	}
	attrs := map[string][]byte{}
	if err := a.PullFromCache("node", &attrs); err == nil {
		t.Fatalf("expected a miss after removal by another instance, got %q", attrs)
	}
}

func TestFileMetadataCacheInvalidation(t *testing.T) {
	for name, remove := range map[string]func(c *fileMetadataCache, key string) error{
		"Delete":         func(c *fileMetadataCache, key string) error { return c.Delete(key) },
		"RemoveMetadata": func(c *fileMetadataCache, key string) error { return c.RemoveMetadata(key) },
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestFileMetadataCache(t, nil)
			if err := c.PushToCache("node", map[string][]byte{"foo": []byte("bar")}); err != nil {
				t.Fatal(err)
			}
			pull(t, c, "node")
			if err := remove(c, "node"); err != nil {
				t.Fatal(err)
			}
			if c.decoded.len() != 0 {
				t.Fatalf("expected the decoded layer to be empty after %s", name)
			}
			attrs := map[string][]byte{}
			if err := c.PullFromCache("node", &attrs); err == nil {
				t.Fatalf("expected a miss after %s, got %q", name, attrs)
			}
		})
	}

	t.Run("overwrite", func(t *testing.T) {
		c, _ := newTestFileMetadataCache(t, nil)
		if err := c.PushToCache("node", map[string][]byte{"foo": []byte("bar")}); err != nil {
			t.Fatal(err)
		}
		pull(t, c, "node")
		want := map[string][]byte{"foo": []byte("baz")}
		if err := c.PushToCache("node", want); err != nil {
			t.Fatal(err)
		}
		assertAttrs(t, pull(t, c, "node"), want)
	})
}

func TestFileMetadataCacheReturnedMapIsNotShared(t *testing.T) {
	want := map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}
	for name, mutate := range map[string]func(got map[string][]byte){
		"keys": func(got map[string][]byte) {
			got["new"] = []byte("value")
			delete(got, "baz")
		},
		"values": func(got map[string][]byte) {
			got["foo"][0] = 'X'
			_ = append(got["foo"][:1], 'Y', 'Y') // must not write into shared memory
		},
	} {
		for setup, populate := range map[string]func(t *testing.T, c *fileMetadataCache){
			"decoded on read": func(t *testing.T, c *fileMetadataCache) {
				writeRaw(t, c, "node", want)
				pull(t, c, "node") // miss: populates the decoded layer
			},
			"pushed": func(t *testing.T, c *fileMetadataCache) {
				if err := c.PushToCache("node", copyAttributes(nil, want)); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name+"/"+setup, func(t *testing.T) {
				c, _ := newTestFileMetadataCache(t, nil)
				populate(t, c)

				mutate(pull(t, c, "node")) // mutate the result of a hit

				assertAttrs(t, pull(t, c, "node"), want)
			})
		}
	}
}

func TestFileMetadataCachePushedMapIsNotShared(t *testing.T) {
	c, _ := newTestFileMetadataCache(t, nil)
	src := map[string][]byte{"foo": []byte("bar")}
	if err := c.PushToCache("node", src); err != nil {
		t.Fatal(err)
	}
	src["foo"][0] = 'X'
	src["new"] = []byte("value")

	assertAttrs(t, pull(t, c, "node"), map[string][]byte{"foo": []byte("bar")})
}

func TestFileMetadataCacheMergesIntoNonEmptyDest(t *testing.T) {
	c, _ := newTestFileMetadataCache(t, nil)
	writeRaw(t, c, "node", map[string][]byte{"foo": []byte("bar")})
	pull(t, c, "node") // populate decoded layer

	attrs := map[string][]byte{"existing": []byte("1")}
	if err := c.PullFromCache("node", &attrs); err != nil {
		t.Fatal(err)
	}
	assertAttrs(t, attrs, map[string][]byte{"foo": []byte("bar"), "existing": []byte("1")})
}

func TestFileMetadataCacheNonMapDestination(t *testing.T) {
	c, decodes := newTestFileMetadataCache(t, nil)
	if err := c.PushToCache("node", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := c.PullFromCache("node", &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected value %q", got)
	}
	if decodes.Load() != 0 {
		t.Fatal("non attribute map values must use the plain cache path")
	}
	if c.decoded.len() != 0 {
		t.Fatal("non attribute map values must not be kept in the decoded layer")
	}
}

func TestFileMetadataCacheDecodedLayerIsBounded(t *testing.T) {
	c, _ := newTestFileMetadataCache(t, nil)
	c.decoded = newDecodedAttributesCache(10, 1<<20)
	for i := 0; i < 100; i++ {
		if err := c.PushToCache(fmt.Sprintf("node-%d", i), map[string][]byte{"foo": []byte("bar")}); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.decoded.len(); n != 10 {
		t.Fatalf("expected the decoded layer to hold 10 entries, got %d", n)
	}
	// evicted entries are still served correctly from the store
	assertAttrs(t, pull(t, c, "node-0"), map[string][]byte{"foo": []byte("bar")})

	// byte budget
	c.decoded = newDecodedAttributesCache(1000, 4096)
	big := map[string][]byte{"foo": bytes.Repeat([]byte("x"), 1024)}
	for i := 0; i < 100; i++ {
		if err := c.PushToCache(fmt.Sprintf("big-%d", i), big); err != nil {
			t.Fatal(err)
		}
	}
	if s := c.decoded.size(); s > 4096 {
		t.Fatalf("decoded layer exceeds its byte budget: %d", s)
	}
	// entries larger than the whole budget are not kept at all
	c.decoded = newDecodedAttributesCache(1000, 512)
	if err := c.PushToCache("huge", big); err != nil {
		t.Fatal(err)
	}
	if c.decoded.len() != 0 {
		t.Fatal("expected an entry larger than the budget to be skipped")
	}
	assertAttrs(t, pull(t, c, "huge"), big)
}

func TestFileMetadataCacheConcurrentAccess(t *testing.T) {
	shared := microstore.NewMemoryStore()
	a, _ := newTestFileMetadataCache(t, shared)
	b, _ := newTestFileMetadataCache(t, shared)
	a.decoded = newDecodedAttributesCache(4, 1<<20) // force evictions

	const versions = 8
	valid := func(attrs map[string][]byte) bool {
		// every version is internally consistent: "a" and "b" carry the same value
		return len(attrs) == 2 && bytes.Equal(attrs["a"], attrs["b"])
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("node-%d", i%6)
				c := a
				if (g+i)%2 == 0 {
					c = b
				}
				switch i % 5 {
				case 0:
					v := []byte(fmt.Sprintf("v%d", (g*i)%versions))
					_ = c.PushToCache(key, map[string][]byte{"a": v, "b": v})
				case 1:
					_ = c.Delete(key)
				default:
					attrs := map[string][]byte{}
					if err := c.PullFromCache(key, &attrs); err == nil {
						if !valid(attrs) {
							t.Errorf("inconsistent attributes: %q", attrs)
						}
						attrs["a"] = append(attrs["a"], 'z') // mutate the copy
					}
				}
			}
		}(g)
	}
	wg.Wait()

	// after the dust settles both instances must agree with the store
	for i := 0; i < 6; i++ {
		key := fmt.Sprintf("node-%d", i)
		v := map[string][]byte{"a": []byte("final"), "b": []byte("final")}
		writeRaw(t, a, key, v)
		assertAttrs(t, pull(t, a, key), v)
		assertAttrs(t, pull(t, b, key), v)
	}
}

// BenchmarkCopyAttributes measures the cost of handing out a private copy of a
// decoded attribute map, to compare against BenchmarkDecodeAttributes.
func BenchmarkCopyAttributes(b *testing.B) {
	attrs := benchAttributes()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = copyAttributes(nil, attrs)
	}
}

// BenchmarkDecodeAttributes measures the cost of decoding an attribute map
// from its msgpack representation.
func BenchmarkDecodeAttributes(b *testing.B) {
	raw, err := msgpack.Marshal(benchAttributes())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		attrs := map[string][]byte{}
		if err := msgpack.Unmarshal(raw, &attrs); err != nil {
			b.Fatal(err)
		}
	}
}

func benchAttributes() map[string][]byte {
	attrs := map[string][]byte{}
	for i := 0; i < 21; i++ {
		attrs[fmt.Sprintf("user.ocis.attribute.%d", i)] = bytes.Repeat([]byte("v"), 36)
	}
	return attrs
}

// TestFileMetadataCacheDecodedLayerAccounting checks that the estimated cost
// of the decoded layer roughly matches the heap it actually retains, for
// entries populated by reads (values alias the raw bytes) and by pushes
// (values are owned copies), with small and large attribute values.
func TestFileMetadataCacheDecodedLayerAccounting(t *testing.T) {
	const n = 2000
	for _, valueSize := range []int{36, 1024} {
		attrs := map[string][]byte{}
		for i := 0; i < 21; i++ {
			attrs[fmt.Sprintf("user.ocis.attribute.%d", i)] = bytes.Repeat([]byte("v"), valueSize)
		}
		for fill, populate := range map[string]func(t *testing.T, c *fileMetadataCache, key string){
			"read": func(t *testing.T, c *fileMetadataCache, key string) {
				attrs := map[string][]byte{}
				if err := c.PullFromCache(key, &attrs); err != nil {
					t.Fatal(err)
				}
			},
			"push": func(t *testing.T, c *fileMetadataCache, key string) {
				if err := c.PushToCache(key, attrs); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(fmt.Sprintf("%s/%dB", fill, valueSize), func(t *testing.T) {
				// retained heap of n entries with and without the decoded layer
				measure := func(maxBytes int) (int64, *fileMetadataCache) {
					c, _ := newTestFileMetadataCache(t, nil)
					c.decoded = newDecodedAttributesCache(n, maxBytes)
					keys := make([]string, n)
					for i := range keys {
						keys[i] = fmt.Sprintf("spaces/ab/cdef/nodes/ab/cd/ef/gh/node-%06d", i)
						if fill == "read" {
							writeRaw(t, c, keys[i], attrs)
						}
					}
					var before, after runtime.MemStats
					runtime.GC()
					runtime.ReadMemStats(&before)
					for _, k := range keys {
						populate(t, c, k)
					}
					runtime.GC()
					runtime.ReadMemStats(&after)
					runtime.KeepAlive(keys)
					return int64(after.HeapAlloc) - int64(before.HeapAlloc), c
				}
				// heap measurements are noisy, accept the best of a few attempts
				var ratio float64
				for attempt := 0; attempt < 3; attempt++ {
					without, _ := measure(0)
					with, c := measure(1 << 30)
					if c.decoded.len() != n {
						t.Fatalf("expected %d entries, got %d", n, c.decoded.len())
					}
					retained := float64(with) - float64(without)
					ratio = float64(c.decoded.size()) / retained
					t.Logf("%s/%dB: estimated %d B, retained %.0f B per entry, ratio %.2f", fill, valueSize, c.decoded.size()/n, retained/n, ratio)
					runtime.KeepAlive(c)
					if ratio >= 0.85 && ratio <= 1.3 {
						return
					}
				}
				t.Fatalf("estimated cost is off: estimated/retained = %.2f", ratio)
			})
		}
	}
}

func TestFileMetadataCacheNoopStoreSkipsDecodedLayer(t *testing.T) {
	c := NewFileMetadataCache(Config{Store: "noop", Database: "db", Table: "table"}).(*fileMetadataCache)
	if err := c.PushToCache("node", map[string][]byte{"foo": []byte("bar")}); err != nil {
		t.Fatal(err)
	}
	if n := c.decoded.len(); n != 0 {
		t.Fatalf("expected no decoded entries with a noop store, got %d", n)
	}
	attrs := map[string][]byte{}
	if err := c.PullFromCache("node", &attrs); err == nil {
		t.Fatalf("expected a miss from a noop store, got %q", attrs)
	}
}

func TestFileMetadataCacheDecodedLayerConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		maxEntries, maxBytes   int
		disabled               bool
		wantEntries, wantBytes int
	}{
		"zero uses defaults":       {0, 0, false, defaultDecodedAttributesEntries, defaultDecodedAttributesBytes},
		"positive values are used": {10, 4096, false, 10, 4096},
		"mixed":                    {10, 0, false, 10, defaultDecodedAttributesBytes},
		"negative entries disable": {-1, 0, true, 0, 0},
		"negative bytes disable":   {0, -1, true, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewFileMetadataCache(Config{
				Store:                       "memory",
				Database:                    "db",
				Table:                       "table",
				DecodedAttributesMaxEntries: tc.maxEntries,
				DecodedAttributesMaxBytes:   tc.maxBytes,
			}).(*fileMetadataCache)
			if tc.disabled {
				if c.decoded != nil {
					t.Fatal("expected the decoded layer to be disabled")
				}
				want := map[string][]byte{"foo": []byte("bar")}
				if err := c.PushToCache("node", want); err != nil {
					t.Fatal(err)
				}
				assertAttrs(t, pull(t, c, "node"), want)
				if n := c.decoded.len(); n != 0 {
					t.Fatalf("expected no decoded entries, got %d", n)
				}
				return
			}
			if c.decoded == nil {
				t.Fatal("expected the decoded layer to be enabled")
			}
			if c.decoded.maxEntries != tc.wantEntries || c.decoded.maxBytes != tc.wantBytes {
				t.Fatalf("got limits %d entries / %d bytes, want %d / %d", c.decoded.maxEntries, c.decoded.maxBytes, tc.wantEntries, tc.wantBytes)
			}
		})
	}
}

// stubReadStore is a store whose Read result can be controlled, all other
// operations go to the embedded store.
type stubReadStore struct {
	microstore.Store
	readErr   error
	readEmpty bool
}

func (s *stubReadStore) Read(key string, opts ...microstore.ReadOption) ([]*microstore.Record, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.readEmpty {
		return []*microstore.Record{}, nil
	}
	return s.Store.Read(key, opts...)
}

func TestFileMetadataCacheMissRemovesDecodedEntry(t *testing.T) {
	for name, fail := range map[string]func(s *stubReadStore){
		"read error":   func(s *stubReadStore) { s.readErr = fmt.Errorf("boom") },
		"empty result": func(s *stubReadStore) { s.readEmpty = true },
		"not found":    func(s *stubReadStore) { _ = s.Store.Delete("node", microstore.DeleteFrom("db", "table")) },
	} {
		t.Run(name, func(t *testing.T) {
			s := &stubReadStore{Store: microstore.NewMemoryStore()}
			c, _ := newTestFileMetadataCache(t, s)
			if err := c.PushToCache("node", map[string][]byte{"foo": []byte("bar")}); err != nil {
				t.Fatal(err)
			}
			if c.decoded.len() != 1 {
				t.Fatal("expected the push to populate the decoded layer")
			}
			fail(s)
			attrs := map[string][]byte{}
			if err := c.PullFromCache("node", &attrs); err == nil {
				t.Fatalf("expected an error, got %q", attrs)
			}
			if n := c.decoded.len(); n != 0 {
				t.Fatalf("expected the decoded entry to be removed after a failed read, got %d entries", n)
			}
		})
	}
}

func TestFileMetadataCacheConcurrentHits(t *testing.T) {
	c, decodes := newTestFileMetadataCache(t, nil)
	want := map[string][]byte{"foo": []byte("bar"), "baz": []byte("qux")}
	writeRaw(t, c, "node", want)
	assertAttrs(t, pull(t, c, "node"), want) // populate the decoded layer

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				attrs := map[string][]byte{}
				if err := c.PullFromCache("node", &attrs); err != nil {
					t.Error(err)
					return
				}
				if !bytes.Equal(attrs["foo"], want["foo"]) || !bytes.Equal(attrs["baz"], want["baz"]) {
					t.Errorf("unexpected attributes %q", attrs)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := decodes.Load(); n != 1 {
		t.Fatalf("expected concurrent hits not to decode again, got %d decodes", n)
	}
	if c.decoded.len() != 1 {
		t.Fatalf("expected the hot entry to stay in the decoded layer")
	}
}
