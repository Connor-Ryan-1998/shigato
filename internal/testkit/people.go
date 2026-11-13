//go:build integration

package testkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/core"
)

// Org creates an org with the given slug, named slug + " org".
func (e *Env) Org(slug string) core.Org {
	e.T.Helper()
	org, err := core.CreateOrg(e.Ctx, e.Pool, slug, slug+" org")
	if err != nil {
		e.T.Fatalf("creating org %q: %v", slug, err)
		return core.Org{}
	}
	return org
}

// User is a row in users.
type User struct {
	ID    int64
	Email string
}

type userConfig struct {
	password string
}

// UserOption customises Env.User.
type UserOption func(*userConfig)

// WithPassword gives the user a local password. Users are passwordless by
// default.
func WithPassword(pw string) UserOption {
	return func(c *userConfig) { c.password = pw }
}

// User inserts a user named after its email. It has no password
// (password_hash NULL) unless WithPassword is given.
func (e *Env) User(email string, opts ...UserOption) User {
	e.T.Helper()
	var c userConfig
	for _, opt := range opts {
		opt(&c)
	}
	var hash *string
	if c.password != "" {
		h, err := auth.HashPassword(c.password)
		if err != nil {
			e.T.Fatalf("hashing password for %s: %v", email, err)
			return User{}
		}
		hash = &h
	}
	u := User{Email: email}
	if err := e.Pool.QueryRow(e.Ctx,
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		email, email, hash,
	).Scan(&u.ID); err != nil {
		e.T.Fatalf("inserting user %s: %v", email, err)
		return User{}
	}
	return u
}

// Member gives u role in org, with no contact.
func (e *Env) Member(u User, org core.Org, role auth.Role) {
	e.T.Helper()
	if _, err := e.Pool.Exec(e.Ctx,
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)",
		u.ID, org.ID, string(role),
	); err != nil {
		e.T.Fatalf("adding %s as %s of %s: %v", u.Email, role, org.Slug, err)
	}
}

// PortalUser inserts a passwordless user with a portal membership (role
// auth.Tenant or auth.Client) tied to contactID, the shape portal invites
// leave behind.
func (e *Env) PortalUser(email string, org core.Org, role auth.Role, contactID int64) User {
	e.T.Helper()
	u := e.User(email)
	if _, err := e.Pool.Exec(e.Ctx,
		"INSERT INTO memberships (user_id, org_id, role, contact_id) VALUES ($1, $2, $3, $4)",
		u.ID, org.ID, string(role), contactID,
	); err != nil {
		e.T.Fatalf("adding portal %s %s of %s for contact %d: %v", role, email, org.Slug, contactID, err)
	}
	return u
}

type inviteConfig struct {
	expired bool
}

// InviteOption customises Env.Invite.
type InviteOption func(*inviteConfig)

// Expired makes the invite already past its expiry.
func Expired() InviteOption { return func(c *inviteConfig) { c.expired = true } }

// Invite inserts an outstanding staff invite to org for email with role,
// whose plaintext token is token, so a test can build the accept path
// itself. It expires in a day unless Expired is given. Only the token's
// SHA-256 is stored, matching auth's hashing.
func (e *Env) Invite(org core.Org, email string, role auth.Role, token string, opts ...InviteOption) int64 {
	e.T.Helper()
	var c inviteConfig
	for _, opt := range opts {
		opt(&c)
	}
	expiry := "now() + interval '1 day'"
	if c.expired {
		expiry = "now() - interval '1 hour'"
	}
	sum := sha256.Sum256([]byte(token))
	var id int64
	if err := e.Pool.QueryRow(e.Ctx,
		"INSERT INTO invites (org_id, email, role, token_hash, expires_at) VALUES ($1, $2, $3, $4, "+expiry+") RETURNING id",
		org.ID, email, string(role), sum[:],
	).Scan(&id); err != nil {
		e.T.Fatalf("inserting invite for %s to %s: %v", email, org.Slug, err)
		return 0
	}
	return id
}

// Session sends requests to h as one actor. An empty Token is anonymous.
type Session struct {
	Token string
	env   *Env
	h     http.Handler
}

// Login starts a real session for u through auth's StartSession and returns
// a Session that sends its cookie to h.
func (e *Env) Login(u User, h http.Handler) Session {
	e.T.Helper()
	rec := httptest.NewRecorder()
	if err := auth.New(e.Pool, false).StartSession(e.Ctx, e.Pool, rec, u.ID); err != nil {
		e.T.Fatalf("starting session for %s: %v", u.Email, err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return Session{Token: c.Value, env: e, h: h}
		}
	}
	e.T.Fatalf("StartSession for %s set no %s cookie", u.Email, auth.SessionCookieName)
	return Session{env: e, h: h}
}

// OwnerSession creates org slug, a passwordless user owner@<slug>.example
// who owns it, and returns the org with a logged-in Session to h.
func (e *Env) OwnerSession(slug string, h http.Handler) (core.Org, Session) {
	e.T.Helper()
	org := e.Org(slug)
	owner := e.User("owner@" + slug + ".example")
	e.Member(owner, org, auth.Owner)
	return org, e.Login(owner, h)
}

// Anonymous returns a Session that sends no cookie.
func (e *Env) Anonymous(h http.Handler) Session {
	return Session{env: e, h: h}
}

// Resume returns a Session that sends the existing session token to h and
// reports failures to e.T. It lets a subtest reuse a session started under
// its parent without failing the parent from the subtest's goroutine. An
// empty token is anonymous.
func (e *Env) Resume(token string, h http.Handler) Session {
	return Session{Token: token, env: e, h: h}
}

// Response is a recorded HTTP response.
type Response struct {
	Code   int
	Header http.Header
	Body   []byte
	t      testing.TB
}

// JSON decodes the body into v, failing the test (showing the body) on error.
func (r Response) JSON(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decoding response body %s: %v", r.Body, err)
	}
}

// Cookies parses the response's Set-Cookie headers.
func (r Response) Cookies() []*http.Cookie {
	return (&http.Response{Header: r.Header}).Cookies()
}

// Do sends a request with JSON and CSRF headers, plus the session cookie
// unless the session is anonymous. body is JSON-encoded unless it is nil or
// an io.Reader, which is sent as-is.
func (s Session) Do(method, path string, body any) Response {
	s.env.T.Helper()
	return s.DoWith(method, path, body, nil)
}

// DoWith is Do plus extra request headers, set after the defaults so they
// can override Content-Type or X-Shigoto-CSRF. An empty value deletes the
// header.
func (s Session) DoWith(method, path string, body any, headers map[string]string) Response {
	t := s.env.T
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case io.Reader:
		reader = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("encoding %s %s body: %v", method, path, err)
			return Response{t: t}
		}
		reader = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shigoto-CSRF", "1")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	if s.Token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Token})
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return Response{Code: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes(), t: t}
}
