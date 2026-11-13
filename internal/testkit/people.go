//go:build integration

package testkit

import (
	"bytes"
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
		}
		hash = &h
	}
	u := User{Email: email}
	if err := e.Pool.QueryRow(e.Ctx,
		"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
		email, email, hash,
	).Scan(&u.ID); err != nil {
		e.T.Fatalf("inserting user %s: %v", email, err)
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

// Anonymous returns a Session that sends no cookie.
func (e *Env) Anonymous(h http.Handler) Session {
	return Session{env: e, h: h}
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

// Do sends a request with JSON and CSRF headers, plus the session cookie
// unless the session is anonymous. body is JSON-encoded unless it is nil or
// an io.Reader, which is sent as-is.
func (s Session) Do(method, path string, body any) Response {
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
	if s.Token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: s.Token})
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return Response{Code: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes(), t: t}
}
