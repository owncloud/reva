package providercache

// Golden files for the JSON of a space document.
//
// The files in testdata/golden were written by the code of this commit, before
// any change of the decoder. They protect two changes:
//
//   - A faster decoder for the space document. A different result of the new
//     decoder can change a cleanup decision of a new pod.
//   - The upgrade of the Go toolchain. The upgrade changes the engine of
//     encoding/json.
//
// The files "<name>.json" are inputs. The files "<name>.golden" hold the result of
// the decoder: "ok" and the JSON of the decoded value, "error", or "panic".
// Run the test with GOLDEN_UPDATE=1 to write the .golden files of new inputs.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	groupv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	userv1beta1 "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	types "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const goldenDir = "testdata/golden"

func goldenShares() map[string]*Shares {
	perms := &collaboration.SharePermissions{Permissions: &provider.ResourcePermissions{Stat: true, InitiateFileDownload: true}}
	resource := &provider.ResourceId{StorageId: "storage", SpaceId: "space", OpaqueId: "file"}
	return map[string]*Shares{
		"user_share": {Shares: map[string]*collaboration.Share{
			"storage$space!a": {
				Id:          &collaboration.ShareId{OpaqueId: "storage$space!a"},
				ResourceId:  resource,
				Permissions: perms,
				Grantee: &provider.Grantee{
					Type: provider.GranteeType_GRANTEE_TYPE_USER,
					Id:   &provider.Grantee_UserId{UserId: &userv1beta1.UserId{Idp: "idp", OpaqueId: "einstein"}},
				},
				Owner:   &userv1beta1.UserId{OpaqueId: "owner"},
				Creator: &userv1beta1.UserId{OpaqueId: "creator"},
				Ctime:   &types.Timestamp{Seconds: 1700000000, Nanos: 5},
				Mtime:   &types.Timestamp{Seconds: 1700000100},
			},
		}},
		"group_share": {Shares: map[string]*collaboration.Share{
			"storage$space!b": {
				Id:          &collaboration.ShareId{OpaqueId: "storage$space!b"},
				ResourceId:  resource,
				Permissions: perms,
				Grantee: &provider.Grantee{
					Type: provider.GranteeType_GRANTEE_TYPE_GROUP,
					Id:   &provider.Grantee_GroupId{GroupId: &groupv1beta1.GroupId{Idp: "idp", OpaqueId: "students"}},
				},
				Owner:   &userv1beta1.UserId{OpaqueId: "owner"},
				Creator: &userv1beta1.UserId{OpaqueId: "creator"},
			},
		}},
		"expiration": {Shares: map[string]*collaboration.Share{
			"storage$space!c": {
				Id:         &collaboration.ShareId{OpaqueId: "storage$space!c"},
				ResourceId: resource,
				Grantee: &provider.Grantee{
					Type: provider.GranteeType_GRANTEE_TYPE_USER,
					Id:   &provider.Grantee_UserId{UserId: &userv1beta1.UserId{OpaqueId: "u"}},
				},
				Expiration: &types.Timestamp{Seconds: 1800000000},
			},
		}},
		"opaque": {Shares: map[string]*collaboration.Share{
			"storage$space!d": {
				Id:         &collaboration.ShareId{OpaqueId: "storage$space!d"},
				ResourceId: resource,
				Grantee: &provider.Grantee{
					Type:   provider.GranteeType_GRANTEE_TYPE_USER,
					Id:     &provider.Grantee_UserId{UserId: &userv1beta1.UserId{OpaqueId: "u"}},
					Opaque: &types.Opaque{Map: map[string]*types.OpaqueEntry{"k": {Decoder: "plain", Value: []byte("v")}}},
				},
			},
		}},
		"two_shares": {Shares: map[string]*collaboration.Share{
			"storage$space!a": {
				Id:      &collaboration.ShareId{OpaqueId: "storage$space!a"},
				Grantee: &provider.Grantee{Type: provider.GranteeType_GRANTEE_TYPE_USER, Id: &provider.Grantee_UserId{UserId: &userv1beta1.UserId{OpaqueId: "u"}}},
			},
			"storage$space!b": {
				Id:      &collaboration.ShareId{OpaqueId: "storage$space!b"},
				Grantee: &provider.Grantee{Type: provider.GranteeType_GRANTEE_TYPE_GROUP, Id: &provider.Grantee_GroupId{GroupId: &groupv1beta1.GroupId{OpaqueId: "g"}}},
			},
		}},
	}
}

func readOrWrite(t *testing.T, path string, want []byte) []byte {
	t.Helper()
	got, err := os.ReadFile(path)
	if os.IsNotExist(err) && os.Getenv("GOLDEN_UPDATE") != "" {
		require.NoError(t, os.WriteFile(path, want, 0o644))
		return want
	}
	require.NoError(t, err, "missing golden file, run with GOLDEN_UPDATE=1")
	return got
}

// The encoder writes the same bytes as the golden files, and the decoder reads
// them back to an equal value.
func TestGoldenSpaceDocumentRoundTrip(t *testing.T) {
	for name, shares := range goldenShares() {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(shares)
			require.NoError(t, err)
			golden := readOrWrite(t, filepath.Join(goldenDir, name+".json"), encoded)
			assert.Equal(t, string(golden), string(encoded), "the encoder changed the bytes of the document")

			var decoded Shares
			require.NoError(t, json.Unmarshal(golden, &decoded))
			require.Len(t, decoded.Shares, len(shares.Shares))
			for id, want := range shares.Shares {
				assert.True(t, proto.Equal(want, decoded.Shares[id]), "share %s differs after the decode", id)
			}

			again, err := json.Marshal(&decoded)
			require.NoError(t, err)
			assert.Equal(t, string(golden), string(again), "decode and encode must give the same bytes")
		})
	}
}

// decodeOutcome runs the decoder and describes the result.
func decodeOutcome(data []byte) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = "panic"
		}
	}()
	var s Shares
	if err := json.Unmarshal(data, &s); err != nil {
		return "error"
	}
	b, err := json.Marshal(&s)
	if err != nil {
		return "error on encode"
	}
	return "ok\n" + string(b)
}

// Each input gives the same result as the golden file. This includes the inputs
// that no pod writes, for example a file edited by hand.
func TestGoldenSpaceDocumentOutcome(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, inputs)
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(in)
			require.NoError(t, err)
			got := decodeOutcome(data)
			want := readOrWrite(t, filepath.Join(goldenDir, name+".golden"), []byte(got))
			assert.Equal(t, string(want), got, fmt.Sprintf("the decoder gives a new result for %s", name))
		})
	}
}
