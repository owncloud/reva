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

func TestNewSpaceViewerRole(t *testing.T) {
	p := NewSpaceViewerRole().CS3ResourcePermissions()

	assert.True(t, p.InitiateFileDownload)

	// should not have trashbin permissions
	assert.False(t, p.ListRecycle)
	assert.False(t, p.RestoreRecycleItem)
	assert.False(t, p.PurgeRecycle)
}

func TestRoleFromResourcePermissions_SpaceViewerIsNotSecureViewer(t *testing.T) {
	got := RoleFromResourcePermissions(NewSpaceViewerRole().CS3ResourcePermissions(), false)
	assert.Equal(t, PermissionRead, got.OCSPermissions())
	assert.NotEqual(t, RoleSecureViewer, got.Name)
	// an X would make the Web frontend hide the download action
	assert.Empty(t, got.WebDAVPermissions(false, false, false, false))
	assert.Empty(t, got.WebDAVPermissions(true, false, false, false))
}

func TestRoleFromResourcePermissions_WithoutTrashbinRolesAreReadable(t *testing.T) {
	for _, constructor := range []func() *Role{
		NewSpaceEditorWithoutTrashbinRole,
		NewSpaceEditorWithoutVersionsWithoutTrashbinRole,
	} {
		role := constructor()
		got := RoleFromResourcePermissions(role.CS3ResourcePermissions(), false)
		assert.Equal(t, PermissionRead|PermissionWrite|PermissionCreate|PermissionDelete, got.OCSPermissions(),
			"expected read, write, create and delete for role %s", role.Name)
	}
}

func TestRoleFromResourcePermissions_SecureViewerCannotDownload(t *testing.T) {
	got := RoleFromResourcePermissions(NewSecureViewerRole().CS3ResourcePermissions(), false)
	assert.Equal(t, RoleSecureViewer, got.Name)
	assert.Contains(t, got.WebDAVPermissions(false, false, false, false), "X")
}

// TestRoleFromResourcePermissions_MixedTrashbinPermissions pins the OCS permissions of editor
// permission sets with mixed trash bin permissions. The read permission does not depend on the
// trash bin permissions. The write permission needs RestoreRecycleItem, or Delete when the trash
// bin cannot be listed. No built-in role that can upload and delete lists the trash bin
// without restoring from it.
func TestRoleFromResourcePermissions_MixedTrashbinPermissions(t *testing.T) {
	tests := []struct {
		name        string
		listRecycle bool
		restore     bool
		delete      bool
		want        Permissions
	}{
		{"no trash bin permissions", false, false, true, PermissionRead | PermissionWrite | PermissionCreate | PermissionDelete},
		{"restore without list", false, true, true, PermissionRead | PermissionWrite | PermissionCreate | PermissionDelete},
		{"list without restore", true, false, true, PermissionRead | PermissionCreate | PermissionDelete},
		{"list and restore", true, true, true, PermissionRead | PermissionWrite | PermissionCreate | PermissionDelete},
		{"restore without delete", false, true, false, PermissionRead | PermissionWrite | PermissionCreate},
		{"neither restore nor delete", false, false, false, PermissionRead | PermissionCreate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewSpaceEditorWithoutVersionsWithoutTrashbinRole().CS3ResourcePermissions()
			p.ListRecycle = tt.listRecycle
			p.RestoreRecycleItem = tt.restore
			p.Delete = tt.delete

			got := RoleFromResourcePermissions(p, false)
			assert.Equal(t, tt.want, got.OCSPermissions())
		})
	}
}
