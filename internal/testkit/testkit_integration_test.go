//go:build integration

package testkit_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"shigoto/internal/auth"
	"shigoto/internal/core"
	"shigoto/internal/testkit"
)

func TestLoginOwnerCanReadOrg(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	h := e.Handler()

	s := e.Login(owner, h)
	if s.Token == "" {
		t.Fatal("Login returned an empty token")
	}
	r := s.Do(http.MethodGet, "/api/v1/orgs/acme/contacts", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET contacts = %d, body %s", r.Code, r.Body)
	}
	var contacts []map[string]any
	r.JSON(&contacts)
}

func TestPortalLoginReachesPortal(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	c, err := core.CreateContact(e.Ctx, e.Pool, org.ID, core.Contact{Kind: "tenant", Name: "Tina"})
	if err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	tenant := e.PortalUser("tina@example.com", org, auth.Tenant, c.ID)
	h := e.Handler()

	r := e.Login(tenant, h).Do(http.MethodGet, "/api/v1/portal/acme/invoices", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET portal invoices = %d, body %s", r.Code, r.Body)
	}
}

func TestAnonymousGets401(t *testing.T) {
	e := testkit.New(t)
	e.Org("acme")
	s := e.Anonymous(e.Handler())
	if s.Token != "" {
		t.Fatalf("Anonymous token = %q, want empty", s.Token)
	}
	r := s.Do(http.MethodGet, "/api/v1/orgs/acme/contacts", nil)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET contacts = %d, want 401; body %s", r.Code, r.Body)
	}
}

func TestModulesOptionDisables(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com", testkit.WithPassword("correct horse battery staple"))
	e.Member(owner, org, auth.Owner)

	on := e.Login(owner, e.Handler()).Do(http.MethodGet, "/api/v1/orgs/acme/properties", nil)
	if on.Code != http.StatusOK {
		t.Fatalf("default handler GET properties = %d, want 200; body %s", on.Code, on.Body)
	}

	off := e.Login(owner, e.Handler(testkit.Modules(map[string]bool{"property": false}))).
		Do(http.MethodGet, "/api/v1/orgs/acme/properties", nil)
	if off.Code != http.StatusNotFound {
		t.Fatalf("property disabled GET properties = %d, want 404; body %s", off.Code, off.Body)
	}
}

func TestSnapshotDetectsChange(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	before := e.Snapshot("contacts", "orgs")
	if !strings.Contains(before, "acme") {
		t.Fatalf("snapshot %s does not include the org row", before)
	}
	if _, err := core.CreateContact(e.Ctx, e.Pool, org.ID, core.Contact{Kind: "client", Name: "Carl"}); err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	after := e.Snapshot("contacts", "orgs")
	if before == after {
		t.Fatalf("snapshot unchanged after insert: %s", after)
	}
	if !strings.Contains(after, "Carl") {
		t.Fatalf("snapshot %s does not include the new contact", after)
	}
}

// recordingTB embeds the real testing.TB to satisfy its private method and
// records Fatalf instead of stopping the test.
type recordingTB struct {
	testing.TB
	fatal   bool
	message string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = true
	r.message = fmt.Sprintf(format, args...)
}

func TestSnapshotUnknownTableFails(t *testing.T) {
	e := testkit.New(t)
	fake := &recordingTB{TB: t}
	e.T = fake
	got := e.Snapshot("no_such_table")
	if !fake.fatal {
		t.Fatalf("Snapshot did not call Fatalf; got %q", got)
	}
	if !strings.Contains(fake.message, "no_such_table") {
		t.Fatalf("Fatalf message %q must name no_such_table", fake.message)
	}
}

func TestSnapshotRejectsNonIdentifier(t *testing.T) {
	e := testkit.New(t)
	fake := &recordingTB{TB: t}
	e.T = fake
	e.Snapshot("orgs; DROP TABLE orgs")
	if !fake.fatal || !strings.Contains(fake.message, "orgs; DROP TABLE orgs") {
		t.Fatalf("Snapshot fatal=%v message %q, want rejection naming the table", fake.fatal, fake.message)
	}
}

func TestCountWhere(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	for _, name := range []string{"A", "B", "C"} {
		kind := "client"
		if name == "C" {
			kind = "tenant"
		}
		if _, err := core.CreateContact(e.Ctx, e.Pool, org.ID, core.Contact{Kind: kind, Name: name}); err != nil {
			t.Fatalf("CreateContact: %v", err)
		}
	}
	if got := e.Count("contacts", ""); got != 3 {
		t.Fatalf("Count(contacts) = %d, want 3", got)
	}
	if got := e.Count("contacts", "kind = $1 AND org_id = $2", "client", org.ID); got != 2 {
		t.Fatalf("Count(contacts, client) = %d, want 2", got)
	}

	fake := &recordingTB{TB: t}
	e.T = fake
	e.Count("no_such_table", "")
	if !fake.fatal || !strings.Contains(fake.message, "no_such_table") {
		t.Fatalf("Count on unknown table fatal=%v message %q, want failure naming the table", fake.fatal, fake.message)
	}
}
