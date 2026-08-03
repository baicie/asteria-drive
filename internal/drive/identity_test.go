package drive

import "testing"

func TestRBACPermissionMatrix(t *testing.T) {
	tests := []struct {
		role        AccessRole
		permissions []Permission
	}{
		{RoleOwner, []Permission{PermissionTenantRead, PermissionFilesRead, PermissionFilesWrite, PermissionFilesDelete}},
		{RoleAdmin, []Permission{PermissionTenantRead, PermissionFilesRead, PermissionFilesWrite, PermissionFilesDelete}},
		{RoleEditor, []Permission{PermissionFilesRead, PermissionFilesWrite}},
		{RoleViewer, []Permission{PermissionFilesRead}},
	}
	for _, test := range tests {
		permissions := PermissionsForRole(test.role)
		if len(permissions) != len(test.permissions) {
			t.Fatalf("role %s has %d permissions, want %d", test.role, len(permissions), len(test.permissions))
		}
		for _, permission := range test.permissions {
			if _, ok := permissions[permission]; !ok {
				t.Errorf("role %s is missing permission %s", test.role, permission)
			}
		}
	}
	if len(PermissionsForRole(AccessRole("unknown"))) != 0 {
		t.Fatal("unknown role must have no permissions")
	}
}
