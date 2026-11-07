package httpapi

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/mail"
)

// registerMemberRoutes mounts the org-admin staff invite and member
// management routes, plus the public invite-lookup/accept routes an
// invitee hits before they're a member of anything.
func registerMemberRoutes(mux *http.ServeMux, d Deps) {
	mux.Handle("GET /api/v1/orgs/{org}/members",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listMembers(w, r, d)
		})))
	mux.Handle("PUT /api/v1/orgs/{org}/members/{user_id}",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			updateMemberRole(w, r, d)
		})))
	mux.Handle("DELETE /api/v1/orgs/{org}/members/{user_id}",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			removeMember(w, r, d)
		})))

	mux.Handle("POST /api/v1/orgs/{org}/invites",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			createInvite(w, r, d)
		})))
	mux.Handle("GET /api/v1/orgs/{org}/invites",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			listInvites(w, r, d)
		})))
	mux.Handle("DELETE /api/v1/orgs/{org}/invites/{id}",
		d.Auth.RequireOrg(auth.PermOrgAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			revokeInvite(w, r, d)
		})))

	// Public: an invitee has no session (or the wrong one) until they
	// accept, so these can't sit behind RequireOrg/RequireUser.
	mux.HandleFunc("GET /api/v1/invites/{token}", func(w http.ResponseWriter, r *http.Request) {
		getInviteDetails(w, r, d)
	})
	mux.HandleFunc("POST /api/v1/invites/{token}/accept", func(w http.ResponseWriter, r *http.Request) {
		acceptInvite(w, r, d)
	})
}

// inviteRoles are the roles an invite (and a member PUT) may carry. tenant
// and client memberships are never created by staff invites - portal access
// is handled separately.
func validInviteRole(role string) bool {
	switch role {
	case "owner", "staff", "bookkeeper":
		return true
	default:
		return false
	}
}

// userIDPathValue parses the {user_id} path value as an int64. A malformed
// value is treated like an unknown one: the caller responds 404 rather than
// revealing that the path shape mattered.
func userIDPathValue(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	return id, err == nil
}

// --- response shapes ---

type memberResponse struct {
	UserID    int64  `json:"user_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ContactID *int64 `json:"contact_id,omitempty"`
	SSOLinked bool   `json:"sso_linked"`
}

type inviteResponse struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

func inviteToResponse(inv auth.Invite) inviteResponse {
	return inviteResponse{ID: inv.ID, Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt.Format(time.RFC3339)}
}

// --- members handlers ---

func listMembers(w http.ResponseWriter, r *http.Request, d Deps) {
	p := auth.From(r.Context())
	rows, err := d.Pool.Query(r.Context(), `
		SELECT u.id, u.name, u.email, m.role, m.contact_id, (u.oidc_subject IS NOT NULL)
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1
		ORDER BY u.name`,
		p.OrgID,
	)
	if err != nil {
		slog.Error("listing members failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	members := []memberResponse{}
	for rows.Next() {
		var m memberResponse
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.ContactID, &m.SSOLinked); err != nil {
			slog.Error("scanning member failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterating members failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, members)
}

// lockOwnerCount locks (FOR UPDATE) and counts orgID's current owner
// membership rows. Callers use it to check, inside the same transaction as
// the write that would remove an owner, that at least one owner remains -
// locking the rows means a second concurrent demotion/removal blocks until
// the first commits, then sees the up-to-date count rather than racing it.
func lockOwnerCount(ctx context.Context, tx pgx.Tx, orgID int64) (int, error) {
	// ORDER BY user_id: every caller locks these rows in the same fixed
	// order, so two concurrent transactions locking the same org's owners
	// (e.g. a demote racing a remove) can never deadlock against each
	// other by having grabbed them in opposite orders.
	rows, err := tx.Query(ctx, "SELECT user_id FROM memberships WHERE org_id = $1 AND role = 'owner' ORDER BY user_id FOR UPDATE", orgID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		n++
	}
	return n, rows.Err()
}

// errLastOwner is the public 409 for demoting or removing an org's last
// owner.
var errLastOwner = &core.PublicError{Err: core.ErrInvalidState, Msg: "an organisation needs at least one owner"}

// errPortalRole is the public 409 for PUTting a staff role (owner/staff/
// bookkeeper) onto a tenant or client membership. Portal access is managed
// separately (it isn't a role this endpoint's invite flow ever grants), so
// a tenant/client row is never a valid target for this write.
var errPortalRole = &core.PublicError{Err: core.ErrInvalidState, Msg: "this member's role is managed through portal access, not here"}

func updateMemberRole(w http.ResponseWriter, r *http.Request, d Deps) {
	userID, ok := userIDPathValue(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validInviteRole(body.Role) {
		writeError(w, http.StatusUnprocessableEntity, "role must be owner, staff or bookkeeper")
		return
	}

	p := auth.From(r.Context())
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var currentRole string
		err := tx.QueryRow(r.Context(), "SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2", p.OrgID, userID).Scan(&currentRole)
		if errors.Is(err, pgx.ErrNoRows) {
			return core.ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentRole == "tenant" || currentRole == "client" {
			return errPortalRole
		}

		if currentRole == "owner" && body.Role != "owner" {
			count, err := lockOwnerCount(r.Context(), tx, p.OrgID)
			if err != nil {
				return err
			}
			if count <= 1 {
				return errLastOwner
			}
		}

		if _, err := tx.Exec(r.Context(), "UPDATE memberships SET role = $1 WHERE org_id = $2 AND user_id = $3", body.Role, p.OrgID, userID); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "membership", userID, "update_role", body)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func removeMember(w http.ResponseWriter, r *http.Request, d Deps) {
	userID, ok := userIDPathValue(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var currentRole string
		err := tx.QueryRow(r.Context(), "SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2", p.OrgID, userID).Scan(&currentRole)
		if errors.Is(err, pgx.ErrNoRows) {
			return core.ErrNotFound
		}
		if err != nil {
			return err
		}

		if currentRole == "owner" {
			count, err := lockOwnerCount(r.Context(), tx, p.OrgID)
			if err != nil {
				return err
			}
			if count <= 1 {
				return errLastOwner
			}
		}

		if _, err := tx.Exec(r.Context(), "DELETE FROM memberships WHERE org_id = $1 AND user_id = $2", p.OrgID, userID); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "membership", userID, "remove", nil)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- invite handlers (org-admin) ---

func createInvite(w http.ResponseWriter, r *http.Request, d Deps) {
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	email := strings.TrimSpace(body.Email)
	if email == "" {
		writeError(w, http.StatusUnprocessableEntity, "email is required")
		return
	}
	addr, err := netmail.ParseAddress(email)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "email is not a valid address")
		return
	}
	email = addr.Address

	if !validInviteRole(body.Role) {
		writeError(w, http.StatusUnprocessableEntity, "role must be owner, staff or bookkeeper")
		return
	}

	p := auth.From(r.Context())
	var inviteID int64
	var token string
	err = runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		var err error
		inviteID, token, err = auth.CreateInvite(r.Context(), tx, p.OrgID, email, body.Role, p.UserID)
		if err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "invite", inviteID, "create", map[string]string{"email": email, "role": body.Role})
	})
	if errors.Is(err, auth.ErrAlreadyMember) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		slog.Error("creating invite failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	link := strings.TrimRight(d.Cfg.BaseURL, "/") + "/invite/" + token
	emailed := sendInviteEmail(r.Context(), d, r.PathValue("org"), email, link)

	writeJSON(w, http.StatusCreated, map[string]any{"invite_id": inviteID, "link": link, "emailed": emailed})
}

// sendInviteEmail sends the invite link to email, if d.Mailer is
// configured, and reports whether it actually went out. A nil Mailer (SMTP
// not configured) or a send failure both return false - neither is treated
// as an error here: the invite link is always returned to the caller in
// createInvite's response regardless, so it can still be shared by hand; a
// send failure is logged, not surfaced, since the invite itself was already
// created successfully.
func sendInviteEmail(ctx context.Context, d Deps, orgSlug, email, link string) bool {
	if d.Mailer == nil {
		return false
	}
	orgName := orgSlug
	if org, err := core.OrgBySlug(ctx, d.Pool, orgSlug); err == nil {
		orgName = org.Name
	}
	// orgName and link are escaped before going into the HTML body: orgName
	// is an operator-chosen org name (not attacker-controlled in practice,
	// but never trusted blindly into markup), and link embeds a
	// base64url-encoded token that can't contain HTML-special characters
	// but is escaped anyway on the same principle.
	safeOrgName := html.EscapeString(orgName)
	safeLink := html.EscapeString(link)
	msg := mail.Message{
		To:      email,
		Subject: fmt.Sprintf("You've been invited to %s on shigoto", orgName),
		Text:    fmt.Sprintf("You've been invited to join %s on shigoto. Open this link to accept: %s", orgName, link),
		HTML:    fmt.Sprintf(`<p>You've been invited to join <strong>%s</strong> on shigoto.</p><p><a href="%s">Accept your invite</a></p>`, safeOrgName, safeLink),
	}
	if err := d.Mailer.Send(ctx, msg); err != nil {
		slog.Error("sending invite email failed", "error", err)
		return false
	}
	return true
}

func listInvites(w http.ResponseWriter, r *http.Request, d Deps) {
	p := auth.From(r.Context())
	invites, err := auth.ListInvites(r.Context(), d.Pool, p.OrgID)
	if err != nil {
		slog.Error("listing invites failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]inviteResponse, len(invites))
	for i, inv := range invites {
		resp[i] = inviteToResponse(inv)
	}
	writeJSON(w, http.StatusOK, resp)
}

func revokeInvite(w http.ResponseWriter, r *http.Request, d Deps) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	p := auth.From(r.Context())
	err := runInTx(r.Context(), d.Pool, func(tx pgx.Tx) error {
		if err := auth.RevokeInvite(r.Context(), tx, p.OrgID, id); err != nil {
			return err
		}
		return core.Audit(r.Context(), tx, p.OrgID, &p.UserID, "invite", id, "revoke", nil)
	})
	if err != nil {
		writeCoreError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- public invite handlers ---

func getInviteDetails(w http.ResponseWriter, r *http.Request, d Deps) {
	details, err := auth.InviteByToken(r.Context(), d.Pool, r.PathValue("token"))
	if errors.Is(err, core.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("looking up invite failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Ruling 63: a signed-in user with no password (a portal-only account)
	// needs the invite page to show a password field when OIDC is disabled.
	// Computed only for a session, not the invite itself - an anonymous
	// visitor always sees the anonymous form's password field regardless.
	needsPassword := false
	if userID, ok := d.Auth.UserFromRequest(r); ok {
		np, err := d.Auth.NeedsPasswordForInvite(r.Context(), userID)
		if err != nil {
			slog.Error("checking invite password requirement failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		needsPassword = np
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"org_name":       details.OrgName,
		"email":          details.Email,
		"role":           details.Role,
		"expired":        details.Expired,
		"needs_password": needsPassword,
	})
}

func acceptInvite(w http.ResponseWriter, r *http.Request, d Deps) {
	var body struct {
		Name     string `json:"name,omitempty"`
		Password string `json:"password,omitempty"`
	}
	if err := readJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var loggedInUserID *int64
	if id, ok := d.Auth.UserFromRequest(r); ok {
		loggedInUserID = &id
	}

	orgSlug, err := d.Auth.AcceptInvite(r.Context(), w, r.PathValue("token"), loggedInUserID, body.Name, body.Password)
	switch {
	case errors.Is(err, auth.ErrInviteInvalid):
		writeError(w, http.StatusGone, "this invite has already been used or has expired")
	case errors.Is(err, auth.ErrEmailMismatch):
		writeError(w, http.StatusConflict, "this invite is for a different email address")
	case errors.Is(err, auth.ErrAccountExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrNameRequired):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		slog.Error("accepting invite failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"org_slug": orgSlug})
	}
}
