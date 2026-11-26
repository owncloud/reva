package providercache

// The decoder of the space document reads each share in one pass. It must give
// the same result as the legacy decoder of legacy_decoder_internal_test.go for
// all input, and it must not panic. A different result can change a cleanup
// decision on a new pod, because a nil share counts as stale.

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// legacyOutcome decodes with the legacy decoder. panicked is true when the
// legacy decoder panics. This happens with a null grantee.
func legacyOutcome(data []byte) (s *Shares, err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	s, err = legacyUnmarshalShares(data)
	return s, err, false
}

// hasDuplicateNames reports if an object of the input has a member name two
// times. No pod writes such a file. The legacy decoder applies the members of a
// grantee one after the other, and the new decoder keeps the last member. The
// difference is accepted, and the comparison skips these inputs.
func hasDuplicateNames(data []byte) bool {
	dec := jsontext.NewDecoder(bytes.NewReader(data)) // rejects duplicate names by default
	for {
		if _, err := dec.ReadToken(); err != nil {
			return err != io.EOF
		}
	}
}

// compareWithLegacy checks that the new decoder gives the legacy result.
func compareWithLegacy(t testing.TB, data []byte) {
	t.Helper()
	if hasDuplicateNames(data) {
		// The new decoder must still not panic.
		var s Shares
		_ = json.Unmarshal(data, &s)
		return
	}
	var got Shares
	gotErr := json.Unmarshal(data, &got)
	// The cache calls decodeShares. Both paths must agree.
	if direct, derr := decodeShares(data); (derr == nil) != (gotErr == nil) || (derr == nil && len(direct.Shares) != len(got.Shares)) {
		t.Fatalf("decodeShares and UnmarshalJSON differ.\ninput: %s", data)
	}

	want, wantErr, panicked := legacyOutcome(data)
	if panicked {
		return // the new decoder must not panic, and it did not
	}
	if wantErr != nil {
		if gotErr == nil {
			t.Fatalf("the legacy decoder fails, the new one does not.\ninput: %s\nerror: %v", data, wantErr)
		}
		return
	}
	if gotErr != nil {
		t.Fatalf("the new decoder fails, the legacy one does not.\ninput: %s\nerror: %v", data, gotErr)
	}
	if len(got.Shares) != len(want.Shares) {
		t.Fatalf("got %d shares, want %d.\ninput: %s", len(got.Shares), len(want.Shares), data)
	}
	for id, w := range want.Shares {
		g, ok := got.Shares[id]
		if !ok {
			t.Fatalf("share %q is missing.\ninput: %s", id, data)
		}
		if (g == nil) != (w == nil) {
			t.Fatalf("share %q: nil mismatch.\ninput: %s", id, data)
		}
		if !proto.Equal(g, w) {
			t.Fatalf("share %q differs.\ninput: %s\ngot:  %v\nwant: %v", id, data, g, w)
		}
	}
	gb, _ := json.Marshal(&got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("the encoded result differs.\ninput: %s\ngot:  %s\nwant: %s", data, gb, wb)
	}
}

func TestDecoderGivesTheLegacyResultForTheGoldenInputs(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, inputs)
	for _, in := range inputs {
		t.Run(filepath.Base(in), func(t *testing.T) {
			data, err := os.ReadFile(in)
			require.NoError(t, err)
			compareWithLegacy(t, data)
		})
	}
}

// A null grantee made the legacy decoder panic. A file that a person edited by
// hand can have it. The new decoder keeps the share and has no grantee.
func TestDecoderDoesNotPanicOnANullGrantee(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(goldenDir, "grantee_null.json"))
	require.NoError(t, err)

	var s Shares
	require.NotPanics(t, func() { require.NoError(t, json.Unmarshal(data, &s)) })

	require.Contains(t, s.Shares, "a")
	assert.Nil(t, s.Shares["a"].GetGrantee())
	assert.Equal(t, "a", s.Shares["a"].GetId().GetOpaqueId())
}

// The decoder does not read the Etag field of the file. The etag comes from the
// response of the storage.
func TestDecoderIgnoresTheEtagOfTheFile(t *testing.T) {
	var s Shares
	require.NoError(t, json.Unmarshal([]byte(`{"Shares":{},"Etag":"from-the-file"}`), &s))
	assert.Empty(t, s.Etag)
}

func FuzzSharesDecoder(f *testing.F) {
	inputs, _ := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	for _, in := range inputs {
		if data, err := os.ReadFile(in); err == nil {
			f.Add(data)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		compareWithLegacy(t, data)
	})
}

// BenchmarkDecodeSpaceDocument decodes a space with 1,000 shares.
func BenchmarkDecodeSpaceDocument(b *testing.B) {
	data := spaceDocument(b, 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decodeShares(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeSpaceDocumentViaJSONUnmarshal is the path of a caller that
// uses json.Unmarshal. The cache does not use it.
func BenchmarkDecodeSpaceDocumentViaJSONUnmarshal(b *testing.B) {
	data := spaceDocument(b, 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var s Shares
		if err := json.Unmarshal(data, &s); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeSpaceDocumentLegacy(b *testing.B) {
	data := spaceDocument(b, 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := legacyUnmarshalShares(data); err != nil {
			b.Fatal(err)
		}
	}
}

func spaceDocument(tb testing.TB, n int) []byte {
	tb.Helper()
	base := goldenShares()["user_share"]
	doc := &Shares{Shares: map[string]*collaboration.Share{}}
	for _, s := range base.Shares {
		for i := 0; i < n; i++ {
			c := proto.Clone(s).(*collaboration.Share)
			c.Id.OpaqueId = "storage$space!" + strconv.Itoa(i)
			doc.Shares[c.Id.OpaqueId] = c
		}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		tb.Fatal(err)
	}
	return data
}
