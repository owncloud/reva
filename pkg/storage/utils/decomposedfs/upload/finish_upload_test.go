package upload

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/aspects"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/options"
	"github.com/owncloud/reva/v2/pkg/storage/utils/decomposedfs/usermapper"
	"github.com/rs/zerolog"
)

// stubMapper satisfies usermapper.Mapper without doing anything real; the
// gid-parsing failure below never reaches ScopeUserByIds, this only needs
// store.um to be non-nil to enter that branch at all.
type stubMapper struct{}

func (stubMapper) RunInBaseScope(f func() error) error { return f() }
func (stubMapper) ScopeBase() (func() error, error)    { return func() error { return nil }, nil }
func (stubMapper) ScopeUser(context.Context) (func() error, error) {
	return func() error { return nil }, nil
}
func (stubMapper) ScopeUserByIds(int, int) (func() error, error) {
	return func() error { return nil }, nil
}

var _ usermapper.Mapper = stubMapper{}

// newTestSession returns a session backed by a real temp-dir store, with its
// bin+info files already created the way InitiateUpload creates them -- the
// two tests below corrupt one specific thing before calling
// FinishUploadDecomposed directly, never reaching CreateNodeForUpload.
func newTestSession(t *testing.T) *OcisSession {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "uploads"), 0755); err != nil {
		t.Fatalf("creating uploads dir: %v", err)
	}
	store := NewSessionStore(nil, aspects.Aspects{}, root, false, options.TokenOptions{}, &zerolog.Logger{})
	session := store.New(context.Background())

	if err := session.TouchBin(); err != nil {
		t.Fatalf("TouchBin: %v", err)
	}
	if err := session.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	return session
}

func assertSessionFilesGone(t *testing.T, session *OcisSession) {
	t.Helper()
	if _, err := os.Stat(session.binPath()); !os.IsNotExist(err) {
		t.Errorf("orphaned upload session binary must be cleaned up, got err=%v", err)
	}
	if _, err := os.Stat(session.infoPath()); !os.IsNotExist(err) {
		t.Errorf("orphaned upload session info file must be cleaned up, got err=%v", err)
	}
}

// TestFinishUploadDecomposedChecksumReadFailureCleansUpSession covers the
// CalculateChecksums error path: the bin file exists but has become
// unreadable (e.g. replaced by a directory) before FinishUploadDecomposed
// gets a chance to read it back for hashing.
func TestFinishUploadDecomposedChecksumReadFailureCleansUpSession(t *testing.T) {
	session := newTestSession(t)

	if err := os.Remove(session.binPath()); err != nil {
		t.Fatalf("removing bin file: %v", err)
	}
	if err := os.Mkdir(session.binPath(), 0755); err != nil {
		t.Fatalf("replacing bin file with a directory: %v", err)
	}

	if err := session.FinishUploadDecomposed(context.Background()); err == nil {
		t.Fatal("expected FinishUploadDecomposed to fail reading a directory as the bin file")
	}

	// the directory we substituted in is not itself the leak under test; only
	// assert the info file -- the "bin path" now holds our directory stand-in.
	if _, err := os.Stat(session.infoPath()); !os.IsNotExist(err) {
		t.Errorf("orphaned upload session info file must be cleaned up, got err=%v", err)
	}
	if err := os.Remove(session.binPath()); err != nil && !os.IsNotExist(err) {
		t.Fatalf("expected the substituted bin directory to still be removable: %v", err)
	}
}

// TestFinishUploadDecomposedInvalidSpaceGidCleansUpSession covers the
// POSIX-usermapping GID-parsing error path.
func TestFinishUploadDecomposedInvalidSpaceGidCleansUpSession(t *testing.T) {
	session := newTestSession(t)
	session.store.um = stubMapper{}
	session.info.Storage["SpaceGid"] = "not-a-number"

	if err := session.FinishUploadDecomposed(context.Background()); err == nil {
		t.Fatal("expected FinishUploadDecomposed to fail parsing a non-numeric SpaceGid")
	}

	assertSessionFilesGone(t, session)
}
