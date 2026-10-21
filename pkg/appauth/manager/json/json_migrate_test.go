package json

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	apppb "github.com/cs3org/go-cs3apis/cs3/auth/applications/v1beta1"
	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	"golang.org/x/crypto/bcrypt"
)

// protobuf-go varies the whitespace of UserId.String() between binaries, so a
// state file written by two builds holds one user under two keys.
const (
	legacyOneSpace  = `idp:"https://idp.example" opaque_id:"u1" type:USER_TYPE_PRIMARY`
	legacyTwoSpaces = `idp:"https://idp.example"  opaque_id:"u1"  type:USER_TYPE_PRIMARY`
	legacyOtherUser = `idp:"https://idp.example" opaque_id:"u2" type:USER_TYPE_PRIMARY`
)

func newLegacyState(t *testing.T) (string, *userpb.User) {
	t.Helper()
	user := &userpb.User{Id: &userpb.UserId{Idp: "https://idp.example", OpaqueId: "u1", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	other := &userpb.UserId{Idp: "https://idp.example", OpaqueId: "u2", Type: userpb.UserType_USER_TYPE_PRIMARY}

	state := map[string]map[string]*apppb.AppPassword{}
	add := func(key, pw, label string, owner *userpb.UserId) {
		hash, _ := bcrypt.GenerateFromPassword([]byte(pw), 4)
		if state[key] == nil {
			state[key] = map[string]*apppb.AppPassword{}
		}
		state[key][string(hash)] = &apppb.AppPassword{Password: string(hash), Label: label, User: owner, Ctime: &typespb.Timestamp{Seconds: 1}, Utime: &typespb.Timestamp{Seconds: 1}}
	}
	add(legacyOneSpace, "1234", "from build A", user.Id)
	add(legacyTwoSpaces, "5678", "from build B", user.Id)
	add(userKey(user.Id), "9999", "already stable", user.Id)
	add(legacyOtherUser, "2222", "other user", other)
	add("not a user id", "3333", "foreign key", user.Id)

	file := t.TempDir() + "/appauth.json"
	data, _ := json.Marshal(state)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file, user
}

func newManagerFor(t *testing.T, file string) *jsonManager {
	t.Helper()
	m, err := New(map[string]interface{}{"file": file, "token_strength": 10, "password_hash_cost": 4})
	if err != nil {
		t.Fatalf("creating the manager: %v", err)
	}
	return m.(*jsonManager)
}

func readState(t *testing.T, file string) map[string]map[string]*apppb.AppPassword {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]map[string]*apppb.AppPassword{}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLegacyKeysAreMigratedOnLoad(t *testing.T) {
	file, user := newLegacyState(t)
	mgr := newManagerFor(t, file)
	ctx := ctxpkg.ContextSetUser(context.Background(), user)
	other := &userpb.UserId{Idp: "https://idp.example", OpaqueId: "u2", Type: userpb.UserType_USER_TYPE_PRIMARY}

	for name, state := range map[string]map[string]map[string]*apppb.AppPassword{"memory": mgr.passwords, "disk": readState(t, file)} {
		if len(state) != 3 {
			t.Errorf("%s: expected 3 buckets, got %d", name, len(state))
		}
		for _, key := range []string{legacyOneSpace, legacyTwoSpaces, legacyOtherUser} {
			if _, ok := state[key]; ok {
				t.Errorf("%s: legacy key %q still present", name, key)
			}
		}
		if got := len(state[userKey(user.Id)]); got != 3 {
			t.Errorf("%s: expected 3 tokens under the stable key, got %d", name, got)
		}
		if got := len(state[userKey(other)]); got != 1 {
			t.Errorf("%s: expected 1 token for the other user, got %d", name, got)
		}
		if _, ok := state["not a user id"]; !ok {
			t.Errorf("%s: a key that is not a user id was dropped", name)
		}
	}

	for _, pw := range []string{"1234", "5678", "9999"} {
		if _, err := mgr.GetAppPassword(ctx, user.GetId(), pw); err != nil {
			t.Errorf("token %q is not usable after the migration: %v", pw, err)
		}
	}
	// A token never authenticates another user.
	if _, err := mgr.GetAppPassword(ctx, user.GetId(), "2222"); err == nil {
		t.Error("another user's token authenticated this user")
	}
	if listed, err := mgr.ListAppPasswords(ctx); err != nil || len(listed) != 3 {
		t.Errorf("expected 3 listed app passwords, got %d (err=%v)", len(listed), err)
	}

	// New tokens land under the stable key.
	if _, err := mgr.GenerateAppPassword(ctx, nil, "new", nil); err != nil {
		t.Fatal(err)
	}
	if got := len(readState(t, file)[userKey(user.Id)]); got != 4 {
		t.Errorf("expected 4 tokens under the stable key, got %d", got)
	}
}

func TestMigrationDoesNotRewriteStableState(t *testing.T) {
	file, user := newLegacyState(t)
	newManagerFor(t, file)

	// The second start finds nothing to migrate and leaves the file alone.
	before, _ := os.ReadFile(file)
	mgr := newManagerFor(t, file)
	mgr.config.KeepExpiredTokensOnLoad = true
	if mgr.migrateLegacyKeys() {
		t.Error("second migration reported a change")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Error("the state file changed although nothing was migrated")
	}
	if _, err := mgr.GetAppPassword(context.Background(), user.GetId(), "1234"); err != nil {
		t.Errorf("token not usable after the second start: %v", err)
	}
}

func TestStableTokenWinsOverLegacyOnConflict(t *testing.T) {
	user := &userpb.UserId{Idp: "https://idp.example", OpaqueId: "u1", Type: userpb.UserType_USER_TYPE_PRIMARY}
	hash, _ := bcrypt.GenerateFromPassword([]byte("1234"), 4)
	stable := &apppb.AppPassword{Password: string(hash), Label: "stable"}
	legacy := &apppb.AppPassword{Password: string(hash), Label: "legacy"}
	mgr := &jsonManager{passwords: map[string]map[string]*apppb.AppPassword{
		userKey(user):  {string(hash): stable},
		legacyOneSpace: {string(hash): legacy},
	}}
	if !mgr.migrateLegacyKeys() {
		t.Fatal("expected the migration to report a change")
	}
	if got := mgr.passwords[userKey(user)][string(hash)].GetLabel(); got != "stable" {
		t.Errorf("expected the stable token to win, got %q", got)
	}
}

func TestMigrationWithEmptyPasswords(t *testing.T) {
	user := &userpb.User{Id: &userpb.UserId{Idp: "https://idp.example", OpaqueId: "u1", Type: userpb.UserType_USER_TYPE_PRIMARY}}
	ctx := ctxpkg.ContextSetUser(context.Background(), user)

	// A new file, an empty object and a JSON null all load as no passwords.
	for name, content := range map[string]*string{
		"missing file": nil,
		"empty file":   ptr(""),
		"empty object": ptr("{}"),
		"null":         ptr("null"),
	} {
		t.Run(name, func(t *testing.T) {
			file := t.TempDir() + "/appauth.json"
			if content != nil {
				if err := os.WriteFile(file, []byte(*content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mgr := newManagerFor(t, file)
			if mgr.passwords == nil || len(mgr.passwords) != 0 {
				t.Fatalf("expected an empty, non-nil password map, got %v", mgr.passwords)
			}
			if mgr.migrateLegacyKeys() {
				t.Error("the migration reported a change on an empty state")
			}
			if len(mgr.passwords) != 0 {
				t.Errorf("the migration added buckets to an empty state: %v", mgr.passwords)
			}
			if _, err := mgr.GetAppPassword(ctx, user.GetId(), "1234"); err == nil {
				t.Error("a token authenticated against an empty state")
			}
			// An empty state still accepts new tokens under the stable key.
			if _, err := mgr.GenerateAppPassword(ctx, nil, "new", nil); err != nil {
				t.Fatal(err)
			}
			if got := len(readState(t, file)[userKey(user.Id)]); got != 1 {
				t.Errorf("expected 1 token under the stable key, got %d", got)
			}
		})
	}
}

func ptr(s string) *string { return &s }
