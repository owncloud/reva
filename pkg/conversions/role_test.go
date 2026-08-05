package conversions

import (
	"testing"

	providerv1beta1 "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
)

func TestSufficientPermissions(t *testing.T) {
	type testData struct {
		Existing   *providerv1beta1.ResourcePermissions
		Requested  *providerv1beta1.ResourcePermissions
		Sufficient bool
	}
	table := []testData{
		{
			Existing:   nil,
			Requested:  nil,
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("editor").CS3ResourcePermissions(),
			Requested:  nil,
			Sufficient: false,
		},
		{
			Existing:   nil,
			Requested:  RoleFromName("viewer").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("editor").CS3ResourcePermissions(),
			Requested:  RoleFromName("viewer").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("viewer").CS3ResourcePermissions(),
			Requested:  RoleFromName("editor").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("spaceviewer").CS3ResourcePermissions(),
			Requested:  RoleFromName("spaceeditor").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("manager").CS3ResourcePermissions(),
			Requested:  RoleFromName("spaceeditor").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("manager").CS3ResourcePermissions(),
			Requested:  RoleFromName("spaceviewer").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("manager").CS3ResourcePermissions(),
			Requested:  RoleFromName("manager").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("manager").CS3ResourcePermissions(),
			Requested:  RoleFromName("denied").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("spaceeditor").CS3ResourcePermissions(),
			Requested:  RoleFromName("denied").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("editor").CS3ResourcePermissions(),
			Requested:  RoleFromName("denied").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("secure-viewer").CS3ResourcePermissions(),
			Requested:  RoleFromName("secure-viewer").CS3ResourcePermissions(),
			Sufficient: true,
		},
		{
			Existing:   RoleFromName("secure-viewer").CS3ResourcePermissions(),
			Requested:  RoleFromName("viewer").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing:   RoleFromName("secure-viewer").CS3ResourcePermissions(),
			Requested:  RoleFromName("editor").CS3ResourcePermissions(),
			Sufficient: false,
		},
		{
			Existing: &providerv1beta1.ResourcePermissions{
				// all permissions, used for personal space owners
				AddGrant:             true,
				CreateContainer:      true,
				Delete:               true,
				GetPath:              true,
				GetQuota:             true,
				InitiateFileDownload: true,
				InitiateFileUpload:   true,
				ListContainer:        true,
				ListFileVersions:     true,
				ListGrants:           true,
				ListRecycle:          true,
				Move:                 true,
				PurgeRecycle:         true,
				RemoveGrant:          true,
				RestoreFileVersion:   true,
				RestoreRecycleItem:   true,
				Stat:                 true,
				UpdateGrant:          true,
				DenyGrant:            true,
			},
			Requested:  RoleFromName("denied").CS3ResourcePermissions(),
			Sufficient: true,
		},
	}
	for _, test := range table {
		assert.Equal(t, test.Sufficient, SufficientCS3Permissions(test.Existing, test.Requested))
	}
}

func TestNewSpaceEditorWithoutVersionsWithoutTrashbinRole(t *testing.T) {
	role := NewSpaceEditorWithoutVersionsWithoutTrashbinRole()
	p := role.CS3ResourcePermissions()

	assert.Equal(t, RoleSpaceEditorWithoutVersionsWithoutTrashbin, role.Name)

	// should have basic editor permissions
	assert.True(t, p.CreateContainer)
	assert.True(t, p.Delete)
	assert.True(t, p.GetPath)
	assert.True(t, p.GetQuota)
	assert.True(t, p.InitiateFileDownload)
	assert.True(t, p.InitiateFileUpload)
	assert.True(t, p.ListContainer)
	assert.True(t, p.ListGrants)
	assert.True(t, p.Move)
	assert.True(t, p.Stat)

	// should not have version permissions
	assert.False(t, p.ListFileVersions)
	assert.False(t, p.RestoreFileVersion)

	// should not have trashbin permissions
	assert.False(t, p.ListRecycle)
	assert.False(t, p.RestoreRecycleItem)
	assert.False(t, p.PurgeRecycle)
}

func TestRoleFromResourcePermissions_WithoutTrashbinRolesAreWritable(t *testing.T) {
	for _, constructor := range []func() *Role{
		NewSpaceEditorWithoutTrashbinRole,
		NewSpaceEditorWithoutVersionsWithoutTrashbinRole,
	} {
		role := constructor()
		got := RoleFromResourcePermissions(role.CS3ResourcePermissions(), false)
		assert.True(t, got.ocsPermissions.Contain(PermissionWrite),
			"expected PermissionWrite for role %s", role.Name)
		assert.Contains(t, got.WebDAVPermissions(false, false, false, false), "W",
			"expected W in WebDAV permissions for role %s", role.Name)
	}
}

// TestRoleFromResourcePermissions_EditorLiteIsWritable guards against the
// editor-lite role ("Can edit" in the web UI) losing the ability to rename and
// change file contents. The role grants Move and InitiateFileUpload, which the
// storage layer accepts, but clients decide which actions to offer from the
// WebDAV permissions string. Without PermissionWrite that string lacks "NV"
// (rename) and "W" (overwrite), so the UI hides both.
func TestRoleFromResourcePermissions_EditorLiteIsWritable(t *testing.T) {
	role := NewEditorLiteRole()
	got := RoleFromResourcePermissions(role.CS3ResourcePermissions(), false)

	assert.Equal(t, RoleEditorLite, got.Name)
	assert.True(t, got.ocsPermissions.Contain(PermissionWrite),
		"expected PermissionWrite for role %s", role.Name)
	assert.Contains(t, got.WebDAVPermissions(false, false, false, false), "W",
		"expected W (overwrite) in WebDAV permissions for role %s", role.Name)
	assert.Contains(t, got.WebDAVPermissions(false, false, false, false), "NV",
		"expected NV (rename) in WebDAV permissions for role %s", role.Name)

	// "Can edit" sits below the "with trashbin" and "with versions" tiers, so it
	// must not gain delete or version permissions.
	assert.False(t, got.ocsPermissions.Contain(PermissionDelete),
		"editor-lite must not grant delete")
	assert.False(t, role.CS3ResourcePermissions().ListFileVersions,
		"editor-lite must not grant version history")
}

// TestRoleFromResourcePermissions_UploaderStaysCreateOnly pins the uploader
// role, which shares editor-lite's create-only OCS permissions but must not
// become writable: it has no Move and no download.
func TestRoleFromResourcePermissions_UploaderStaysCreateOnly(t *testing.T) {
	got := RoleFromResourcePermissions(NewUploaderRole().CS3ResourcePermissions(), false)

	assert.Equal(t, RoleUploader, got.Name)
	assert.Equal(t, PermissionCreate, got.ocsPermissions)
	assert.NotContains(t, got.WebDAVPermissions(false, false, false, false), "W",
		"uploader must not be writable")
}

// TestRoleFromResourcePermissions_DeletableGrantsKeepTheirOCSPermissions pins the
// legacy OCS permission values that survive a round trip through the persisted
// ACE format. That format has a single "w" flag covering both InitiateFileUpload
// and Move, so a stored grant always reads back with Move set once it is
// writable at all. Letting Move alone imply PermissionWrite therefore reported
// every delete+create+read grant as delete+create+read+write, which is what
// ocs:share-permissions exposes to clients.
func TestRoleFromResourcePermissions_DeletableGrantsKeepTheirOCSPermissions(t *testing.T) {
	for _, p := range []Permissions{
		PermissionRead | PermissionCreate | PermissionDelete,
		PermissionRead | PermissionCreate | PermissionDelete | PermissionShare,
	} {
		// A grant with Move set, as it reads back from storage.
		rp := RoleFromOCSPermissions(p, nil).CS3ResourcePermissions()
		rp.Move = true

		got := RoleFromResourcePermissions(rp, false)
		assert.Equal(t, p, got.ocsPermissions,
			"a stored grant with OCS permissions %d must not gain write", p)
		assert.False(t, got.ocsPermissions.Contain(PermissionWrite),
			"a stored grant with OCS permissions %d must not gain write", p)
	}
}
