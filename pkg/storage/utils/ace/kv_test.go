package ace

import (
	"encoding/csv"
	"reflect"
	"strings"
	"testing"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
)

// csvKVFields is the reference implementation: the key value pairs have always been
// parsed as a single csv record with ':' as separator.
func csvKVFields(s string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(s))
	r.Comma = ':'
	r.Comment = 0
	r.FieldsPerRecord = -1
	r.LazyQuotes = false
	r.TrimLeadingSpace = false
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, errMultipleRows
	}
	return records[0], nil
}

var kvFieldsSeeds = []string{
	"",
	":",
	"::",
	"t=A:f=:p=rw:c=creator:e=0",
	"t=A:f=:p=rw:c=creator:e=0\n",
	"t=A:f=:p=rw:c=creator:e=0\n\n",
	"a:\n",
	":\n",
	"t=A:f=g:p=rw:c=:e=1700000000",
	" t=A : f=g ",
	"t=A:l=\"a:b\"",
	"t=A:l=\"a\"\"b\"",
	"t=A:l=a\"b",
	"\"unterminated",
	"t=A\nf=g",
	"t=A\r\nf=g",
	"t=A\rf=g",
	"t=A\n",
	"\n",
	"#comment",
	"t=\xff\xfe:p=r",
	"t=A:p=r=w",
}

func TestKVFieldsMatchesCSV(t *testing.T) {
	for _, s := range kvFieldsSeeds {
		assertKVFieldsMatchCSV(t, s)
	}
}

func FuzzKVFields(f *testing.F) {
	for _, s := range kvFieldsSeeds {
		f.Add(s)
	}
	f.Fuzz(assertKVFieldsMatchCSV)
}

func assertKVFieldsMatchCSV(t *testing.T, s string) {
	want, wantErr := csvKVFields(s)
	got, gotErr := kvFields(s)
	if (wantErr != nil) != (gotErr != nil) {
		t.Fatalf("kvFields(%q) error = %v, csv error = %v", s, gotErr, wantErr)
	}
	if wantErr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("kvFields(%q) = %q, csv = %q", s, got, want)
	}
}

func TestUnmarshalKVAllocations(t *testing.T) {
	principal, value := FromGrant(&provider.Grant{
		Grantee: &provider.Grantee{
			Type: provider.GranteeType_GRANTEE_TYPE_USER,
			Id:   &provider.Grantee_UserId{UserId: &userpb.UserId{OpaqueId: "4c510ada-c86b-4815-8820-42cdf82c3d51"}},
		},
		Permissions: &provider.ResourcePermissions{Stat: true, ListContainer: true, InitiateFileDownload: true},
		Creator:     &userpb.UserId{OpaqueId: "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c"},
	}).Marshal()

	allocs := testing.AllocsPerRun(100, func() {
		if _, err := Unmarshal(principal, value); err != nil {
			t.Fatal(err)
		}
	})
	// Parsing a regular ACE needs a handful of allocations (the ace, the copied
	// value, the split fields). Setting up a csv reader per ACE needed about 20.
	if allocs >= 10 {
		t.Fatalf("Unmarshal allocates %v times, want less than 10", allocs)
	}
}

// TestUnmarshalStoredACE feeds Unmarshal the bytes as they are stored in the
// node metadata: a leading 0x00 type byte, then the csv record that Marshal
// writes, terminated by a newline.
func TestUnmarshalStoredACE(t *testing.T) {
	const principal = "u:ef756a89-ebab-49c7-941a-9eb8ed2fd3ca"
	for name, value := range map[string]string{
		"as stored":           "\x00t=A:f=:p=txrwadCcoOuUPvVq:c=:e=0\n",
		"without the newline": "\x00t=A:f=:p=txrwadCcoOuUPvVq:c=:e=0",
	} {
		t.Run(name, func(t *testing.T) {
			e, err := Unmarshal(principal, []byte(value))
			if err != nil {
				t.Fatalf("Unmarshal(%q) error = %v", value, err)
			}
			if e._type != "A" || e.flags != "" || e.permissions != "txrwadCcoOuUPvVq" || e.creator != "" || e.expires != 0 || e.principal != principal {
				t.Fatalf("Unmarshal(%q) = %+v", value, e)
			}
			// same fields as the csv reader
			assertKVFieldsMatchCSV(t, value[1:])
		})
	}

	t.Run("without the type byte", func(t *testing.T) {
		if _, err := Unmarshal(principal, []byte("t=A:f=:p=txrwadCcoOuUPvVq:c=:e=0")); err == nil || err.Error() != "unknown ace encoding" {
			t.Fatalf("Unmarshal without type byte error = %v, want unknown ace encoding", err)
		}
	})
}
