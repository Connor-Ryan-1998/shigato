//go:build integration

package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/httpapi/casefile"
	"shigoto/internal/testkit"
)

// TestHTTPCases runs every testdata/cases/*.yaml file: it seeds a database
// per file (or per request for isolate: true cases), sends each case's
// request once per actor, and checks status, headers, body subset and the
// unchanged tables. See package casefile for the format.
func TestHTTPCases(t *testing.T) {
	paths, err := filepath.Glob("testdata/cases/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no testdata/cases/*.yaml files found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := casefile.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			runCaseFile(t, f)
		})
	}
}

func runCaseFile(t *testing.T, f casefile.File) {
	var shared *caseWorld
	for _, c := range f.Cases {
		if !c.Isolate {
			shared = seedCaseWorld(t, f.Seed)
			break
		}
	}
	for _, c := range f.Cases {
		for _, actor := range c.As {
			t.Run(c.Name+"/"+actor, func(t *testing.T) {
				w := shared
				if c.Isolate {
					w = seedCaseWorld(t, f.Seed)
				}
				runCase(t, filepath.Base(f.Path), w, c, actor)
			})
		}
	}
}

// caseWorld is a seeded database, the handler over it and each actor's
// session token.
type caseWorld struct {
	env    *testkit.Env
	h      http.Handler
	ids    map[string]int64
	tokens map[string]string
}

// seedCaseWorld applies s in a fixed order (orgs, settings, contacts,
// users, properties, leases, projects, invoices, invites), each map in
// sorted key order so IDs are deterministic, then logs every user in.
func seedCaseWorld(t *testing.T, s casefile.Seed) *caseWorld {
	t.Helper()
	e := testkit.New(t)
	ids := map[string]int64{}

	orgs := map[string]core.Org{}
	for _, slug := range s.Orgs {
		orgs[slug] = e.Org(slug)
	}
	for _, slug := range sortedKeys(s.Settings) {
		st := s.Settings[slug]
		opts := []testkit.SettingsOption{testkit.GSTRegistered(st.GSTRegistered)}
		if st.PayOptions {
			opts = append(opts, testkit.PayOptions())
		}
		e.Settings(orgs[slug], opts...)
	}
	for _, name := range sortedKeys(s.Contacts) {
		c := s.Contacts[name]
		var opts []testkit.ContactOption
		if c.Kind != "" {
			opts = append(opts, testkit.Kind(c.Kind))
		}
		if c.Name != "" {
			opts = append(opts, testkit.Name(c.Name))
		}
		if c.Email != "" {
			opts = append(opts, testkit.Email(c.Email))
		}
		ids[name] = e.Contact(orgs[c.Org], opts...).ID
	}
	users := map[string]testkit.User{}
	for _, name := range sortedKeys(s.Users) {
		u := s.Users[name]
		email := name + "@example.com"
		role := auth.Role(u.Role)
		if role == auth.Tenant || role == auth.Client {
			users[name] = e.PortalUser(email, orgs[u.Org], role, ids[u.Contact])
		} else {
			users[name] = e.User(email)
			e.Member(users[name], orgs[u.Org], role)
		}
		ids[name] = users[name].ID
	}
	for _, name := range sortedKeys(s.Properties) {
		p := s.Properties[name]
		var opts []testkit.PropertyOption
		if p.Name != "" {
			opts = append(opts, testkit.PropertyName(p.Name))
		}
		if p.Commercial {
			opts = append(opts, testkit.Commercial())
		}
		ids[name] = e.Property(orgs[p.Org], opts...).ID
	}
	for _, name := range sortedKeys(s.Leases) {
		l := s.Leases[name]
		var opts []testkit.LeaseOption
		if l.Rent != 0 {
			opts = append(opts, testkit.Rent(core.Cents(l.Rent)))
		}
		ids[name] = e.Lease(orgs[l.Org], ids[l.Property], ids[l.Tenant], opts...).ID
	}
	for _, name := range sortedKeys(s.Projects) {
		p := s.Projects[name]
		ids[name] = e.Project(orgs[p.Org], ids[p.Client]).ID
	}
	for _, name := range sortedKeys(s.Invoices) {
		inv := s.Invoices[name]
		var opts []testkit.InvoiceOption
		if inv.Amount != 0 {
			opts = append(opts, testkit.Amount(inv.Amount))
		}
		if inv.Source != "" {
			opts = append(opts, testkit.Source(inv.Source))
		}
		if inv.Status == "issued" {
			opts = append(opts, testkit.Issued(time.Now()))
		}
		ids[name] = e.Invoice(orgs[inv.Org], ids[inv.Contact], opts...).ID
	}
	for _, name := range sortedKeys(s.Invites) {
		inv := s.Invites[name]
		var opts []testkit.InviteOption
		if inv.Expired {
			opts = append(opts, testkit.Expired())
		}
		ids[name] = e.Invite(orgs[inv.Org], inv.Email, auth.Role(inv.Role), inv.Token, opts...)
	}

	h := e.Handler(testkit.Modules(s.Modules))
	tokens := map[string]string{casefile.Anonymous: ""}
	for _, name := range sortedKeys(users) {
		tokens[name] = e.Login(users[name], h).Token
	}
	return &caseWorld{env: e, h: h, ids: ids, tokens: tokens}
}

// runCase sends c's request as actor against w and checks the response.
// Failures name the file, case, actor and request.
func runCase(t *testing.T, file string, w *caseWorld, c casefile.Case, actor string) {
	t.Helper()
	// A copy of the Env reporting to this subtest, so a Snapshot failure
	// fails the subtest rather than the shared parent.
	e := *w.env
	e.T = t

	path, err := casefile.Interpolate(c.Request.Path, w.ids)
	if err != nil {
		t.Fatalf("%s: case %q: request.path: %v", file, c.Name, err)
	}
	body, err := casefile.InterpolateBody(c.Request.Body, w.ids)
	if err != nil {
		t.Fatalf("%s: case %q: request.body: %v", file, c.Name, err)
	}
	prefix := fmt.Sprintf("%s: case %q as %s: %s %s", file, c.Name, actor, c.Request.Method, path)

	var before string
	if len(c.Unchanged) > 0 {
		before = e.Snapshot(c.Unchanged...)
	}

	r := e.Resume(w.tokens[actor], w.h).DoWith(c.Request.Method, path, body, c.Request.Headers)

	if r.Code != c.Expect.Status {
		t.Errorf("%s: status got %d want %d\nbody: %s", prefix, r.Code, c.Expect.Status, truncate(string(r.Body)))
	}
	for _, name := range sortedKeys(c.Expect.Headers) {
		want := c.Expect.Headers[name]
		if got := r.Header.Get(name); got != want {
			t.Errorf("%s: header %s got %q want %q", prefix, name, got, want)
		}
	}
	if c.Expect.Body != nil {
		want, err := casefile.InterpolateBody(c.Expect.Body, w.ids)
		if err != nil {
			t.Fatalf("%s: expect.body: %v", prefix, err)
		}
		match := casefile.Subset
		if c.Expect.Exact {
			match = casefile.Equal
		}
		var got any
		if err := json.Unmarshal(r.Body, &got); err != nil {
			t.Errorf("%s: body is not JSON (%v)\nbody: %s", prefix, err, truncate(string(r.Body)))
		} else if ok, diff := match(want, got); !ok {
			t.Errorf("%s: body mismatch at %s\nbody: %s", prefix, diff, truncate(string(r.Body)))
		}
	}
	if len(c.Unchanged) > 0 {
		if after := e.Snapshot(c.Unchanged...); after != before {
			t.Errorf("%s: unchanged tables [%s] changed\nbefore:\n%s\nafter:\n%s",
				prefix, strings.Join(c.Unchanged, ", "), truncate(before), truncate(after))
		}
	}
}

// truncate caps s at about 2KB for failure output.
func truncate(s string) string {
	const limit = 2048
	if len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("... (%d more bytes)", len(s)-limit)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
