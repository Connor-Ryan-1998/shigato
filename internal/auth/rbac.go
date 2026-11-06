// Package auth handles staff local login, sessions and role-based access
// control.
package auth

import "sort"

// Role is a membership role within an org.
type Role string

const (
	Owner      Role = "owner"
	Staff      Role = "staff"
	Bookkeeper Role = "bookkeeper"
	Tenant     Role = "tenant"
	Client     Role = "client"
)

// Perm is a permission a role may hold.
type Perm string

const (
	PermOrgAdmin      Perm = "org_admin"
	PermContactsWrite Perm = "contacts_write"
	PermInvoicesWrite Perm = "invoices_write"
	PermInvoicesRead  Perm = "invoices_read"
	PermPaymentsWrite Perm = "payments_write"
	PermReportsRead   Perm = "reports_read"
	PermReconWrite    Perm = "recon_write"
	PermPropertyWrite Perm = "property_write"
	PermPropertyRead  Perm = "property_read"
	// PermTimesheetsWrite lets a user write their own time entries.
	PermTimesheetsWrite Perm = "timesheets_write"
	// PermTimesheetsRead lets a user read timesheets/projects data.
	PermTimesheetsRead Perm = "timesheets_read"
	// PermTimesheetsAdmin covers editing anyone's time entries and
	// writing projects/tasks; owner only.
	PermTimesheetsAdmin Perm = "timesheets_admin"
)

// rolePerms is the single source of truth for Can. Owner holds every
// permission; Tenant and Client hold none (portal access arrives in Plan 5).
var rolePerms = map[Role]map[Perm]bool{
	Owner: {
		PermOrgAdmin:        true,
		PermContactsWrite:   true,
		PermInvoicesWrite:   true,
		PermInvoicesRead:    true,
		PermPaymentsWrite:   true,
		PermReportsRead:     true,
		PermReconWrite:      true,
		PermPropertyWrite:   true,
		PermPropertyRead:    true,
		PermTimesheetsWrite: true,
		PermTimesheetsRead:  true,
		PermTimesheetsAdmin: true,
	},
	Staff: {
		PermContactsWrite:   true,
		PermInvoicesWrite:   true,
		PermInvoicesRead:    true,
		PermPaymentsWrite:   true,
		PermReportsRead:     true,
		PermReconWrite:      true,
		PermPropertyWrite:   true,
		PermPropertyRead:    true,
		PermTimesheetsWrite: true,
		PermTimesheetsRead:  true,
	},
	Bookkeeper: {
		PermInvoicesRead:   true,
		PermReportsRead:    true,
		PermReconWrite:     true,
		PermPaymentsWrite:  true,
		PermPropertyRead:   true,
		PermTimesheetsRead: true,
	},
	Tenant: {},
	Client: {},
}

// Can reports whether role r holds permission p.
func Can(r Role, p Perm) bool {
	return rolePerms[r][p]
}

// PermsFor returns every permission role r grants, sorted for stable
// output (e.g. the /api/v1/me response).
func PermsFor(r Role) []Perm {
	granted := rolePerms[r]
	perms := make([]Perm, 0, len(granted))
	for p, ok := range granted {
		if ok {
			perms = append(perms, p)
		}
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}
