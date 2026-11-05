package auth

import "testing"

func TestCanMatrix(t *testing.T) {
	allPerms := []Perm{
		PermOrgAdmin,
		PermContactsWrite,
		PermInvoicesWrite,
		PermInvoicesRead,
		PermPaymentsWrite,
		PermReportsRead,
		PermReconWrite,
	}

	staffPerms := map[Perm]bool{
		PermContactsWrite: true,
		PermInvoicesWrite: true,
		PermInvoicesRead:  true,
		PermPaymentsWrite: true,
		PermReportsRead:   true,
		PermReconWrite:    true,
	}

	bookkeeperPerms := map[Perm]bool{
		PermInvoicesRead:  true,
		PermReportsRead:   true,
		PermReconWrite:    true,
		PermPaymentsWrite: true,
	}

	tests := []struct {
		role  Role
		allow map[Perm]bool
	}{
		{Owner, nil}, // nil means "every permission"
		{Staff, staffPerms},
		{Bookkeeper, bookkeeperPerms},
		{Tenant, map[Perm]bool{}},
		{Client, map[Perm]bool{}},
	}

	for _, tt := range tests {
		for _, p := range allPerms {
			want := tt.allow == nil || tt.allow[p]
			if got := Can(tt.role, p); got != want {
				t.Errorf("Can(%s, %s) = %v, want %v", tt.role, p, got, want)
			}
		}
	}
}
