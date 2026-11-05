// Package auth handles staff local login, sessions and role-based access
// control.
package auth

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
)

// rolePerms is the single source of truth for Can. Owner holds every
// permission; Tenant and Client hold none (portal access arrives in Plan 5).
var rolePerms = map[Role]map[Perm]bool{
	Owner: {
		PermOrgAdmin:      true,
		PermContactsWrite: true,
		PermInvoicesWrite: true,
		PermInvoicesRead:  true,
		PermPaymentsWrite: true,
		PermReportsRead:   true,
		PermReconWrite:    true,
		PermPropertyWrite: true,
		PermPropertyRead:  true,
	},
	Staff: {
		PermContactsWrite: true,
		PermInvoicesWrite: true,
		PermInvoicesRead:  true,
		PermPaymentsWrite: true,
		PermReportsRead:   true,
		PermReconWrite:    true,
		PermPropertyWrite: true,
		PermPropertyRead:  true,
	},
	Bookkeeper: {
		PermInvoicesRead:  true,
		PermReportsRead:   true,
		PermReconWrite:    true,
		PermPaymentsWrite: true,
		PermPropertyRead:  true,
	},
	Tenant: {},
	Client: {},
}

// Can reports whether role r holds permission p.
func Can(r Role, p Perm) bool {
	return rolePerms[r][p]
}
