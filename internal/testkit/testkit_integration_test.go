//go:build integration

package testkit_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"shigoto/internal/auth"
	"shigoto/internal/billing"
	"shigoto/internal/core"
	"shigoto/internal/payments"
	"shigoto/internal/property"
	"shigoto/internal/testkit"
	"shigoto/internal/timesheets"
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

func TestDoWithOverridesDefaultHeaders(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	s := e.Login(owner, e.Handler())
	body := map[string]any{"kind": "client", "name": "X"}

	if r := s.DoWith(http.MethodPost, "/api/v1/orgs/acme/contacts", body, map[string]string{"X-Shigoto-CSRF": ""}); r.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF = %d, want 403; body %s", r.Code, r.Body)
	}
	if r := s.DoWith(http.MethodPost, "/api/v1/orgs/acme/contacts", body, map[string]string{"Origin": "https://evil.example"}); r.Code != http.StatusForbidden {
		t.Fatalf("POST with foreign Origin = %d, want 403; body %s", r.Code, r.Body)
	}
	if n := e.Count("contacts", ""); n != 0 {
		t.Fatalf("rejected POSTs left %d contacts", n)
	}
	if r := s.DoWith(http.MethodPost, "/api/v1/orgs/acme/contacts", body, nil); r.Code != http.StatusCreated {
		t.Fatalf("POST with default headers = %d, want 201; body %s", r.Code, r.Body)
	}
}

func TestResumeReusesSessionToken(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	h := e.Handler()
	s := e.Login(owner, h)

	t.Run("child", func(t *testing.T) {
		child := *e
		child.T = t
		r := child.Resume(s.Token, h).Do(http.MethodGet, "/api/v1/orgs/acme/contacts", nil)
		if r.Code != http.StatusOK {
			t.Fatalf("resumed GET contacts = %d, want 200; body %s", r.Code, r.Body)
		}
		if r := child.Resume("", h).Do(http.MethodGet, "/api/v1/orgs/acme/contacts", nil); r.Code != http.StatusUnauthorized {
			t.Fatalf("empty-token GET contacts = %d, want 401", r.Code)
		}
	})
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

func TestSnapshotOrdersByFirstColumn(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	var ids []int64
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		c, err := core.CreateContact(e.Ctx, e.Pool, org.ID, core.Contact{Kind: "client", Name: name})
		if err != nil {
			t.Fatalf("CreateContact: %v", err)
		}
		ids = append(ids, c.ID)
	}
	before := e.Snapshot("contacts")
	// A no-op update writes a new row version at the end of the heap, so an
	// unordered scan would now return Alpha last.
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE contacts SET name = name WHERE id = $1", ids[0]); err != nil {
		t.Fatalf("touching contact: %v", err)
	}
	after := e.Snapshot("contacts")
	if before != after {
		t.Fatalf("snapshot changed after a no-op update:\nbefore %s\nafter  %s", before, after)
	}
	a, b, c := strings.Index(after, "Alpha"), strings.Index(after, "Bravo"), strings.Index(after, "Charlie")
	if a < 0 || a > b || b > c {
		t.Fatalf("snapshot rows not in id order: %s", after)
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

func TestInvoiceIssuedHasNumber(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	c := e.Contact(org)
	if c.Kind != "client" || c.Name != "Test Contact" {
		t.Fatalf("contact defaults = %q/%q, want client/Test Contact", c.Kind, c.Name)
	}

	draft := e.Invoice(org, c.ID)
	if draft.Status != "draft" || draft.Number != nil || draft.Total != 1000 || draft.Source != "manual" || draft.DueInDays != 14 {
		t.Fatalf("draft = status %q number %v total %d source %q due_in %d, want draft/nil/1000/manual/14",
			draft.Status, draft.Number, draft.Total, draft.Source, draft.DueInDays)
	}

	inv := e.Invoice(org, c.ID, testkit.Amount(2500), testkit.Issued(testkit.Date("2026-02-01")))
	if inv.Status != "open" || inv.Number == nil || inv.Total != 2500 {
		t.Fatalf("issued = status %q number %v total %d, want open/non-nil/2500", inv.Status, inv.Number, inv.Total)
	}
	if inv.IssueDate == nil || !inv.IssueDate.Equal(testkit.Date("2026-02-01")) {
		t.Fatalf("issue date = %v, want 2026-02-01", inv.IssueDate)
	}
}

func TestLeaseDefaultsWeeklyPrimary(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	tenant := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Tina"))
	prop := e.Property(org)
	if !prop.Residential || prop.Name != "1 Test St" {
		t.Fatalf("property defaults = %q residential=%v, want 1 Test St/true", prop.Name, prop.Residential)
	}

	l := e.Lease(org, prop.ID, tenant.ID)
	if l.Frequency != property.Weekly || !l.StartDate.Equal(testkit.Date("2026-01-05")) {
		t.Fatalf("lease = %s from %v, want weekly from 2026-01-05", l.Frequency, l.StartDate)
	}
	if len(l.Tenants) != 1 || l.Tenants[0].ContactID != tenant.ID || !l.Tenants[0].IsPrimary {
		t.Fatalf("tenants = %+v, want only %d as primary", l.Tenants, tenant.ID)
	}
	if len(l.RentChanges) != 1 || l.RentChanges[0].Amount != 50000 {
		t.Fatalf("rent changes = %+v, want one of 50000", l.RentChanges)
	}

	co := e.Contact(org, testkit.Kind("tenant"), testkit.Name("Colin"))
	shop := e.Property(org, testkit.Commercial(), testkit.PropertyName("2 Shop Rd"))
	if shop.Residential || shop.Name != "2 Shop Rd" {
		t.Fatalf("commercial property = %q residential=%v", shop.Name, shop.Residential)
	}
	l2 := e.Lease(org, shop.ID, tenant.ID, testkit.CoTenant(co.ID), testkit.Frequency(property.Monthly),
		testkit.Start(testkit.Date("2026-03-01")), testkit.Rent(200000), testkit.Bond(100000))
	if len(l2.Tenants) != 2 || l2.Frequency != property.Monthly || l2.BondAmount != 100000 {
		t.Fatalf("lease with options = %+v", l2)
	}
}

func TestSettingsPayOptionsAllowIssue(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	c := e.Contact(org)
	// The QLD pay-options rule applies only to rent and bond invoices.
	draft := e.Invoice(org, c.ID, testkit.Source("rent"))

	if _, err := e.BillingService().Issue(e.Ctx, org.ID, org.Slug, draft.ID); !errors.Is(err, billing.ErrTooFewPayOptions) {
		t.Fatalf("Issue without pay options = %v, want ErrTooFewPayOptions", err)
	}

	e.Settings(org, testkit.GSTRegistered(true), testkit.PayOptions())
	inv, err := e.BillingService().Issue(e.Ctx, org.ID, org.Slug, draft.ID)
	if err != nil {
		t.Fatalf("Issue with pay options: %v", err)
	}
	if inv.Number == nil {
		t.Fatalf("issued invoice has no number: %+v", inv)
	}
	if got := e.Count("orgs", "id = $1 AND gst_registered", org.ID); got != 1 {
		t.Fatalf("org gst_registered not set")
	}
}

func TestProjectTaskEntry(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	u := e.User("worker@example.com")
	e.Member(u, org, auth.Owner)
	client := e.Contact(org)
	p := e.Project(org, client.ID)
	if p.Name != "Website rebuild" || p.HourlyRate != 12000 || p.GSTCode != core.GST {
		t.Fatalf("project defaults = %+v", p)
	}
	task := e.Task(org, p.ID)
	if task.ProjectID != p.ID || task.Name == "" {
		t.Fatalf("task = %+v", task)
	}
	entry := e.Entry(org, u.ID, timesheets.Entry{TaskID: task.ID, WorkDate: testkit.Date("2026-02-02"), Minutes: 90},
		testkit.Date("2026-02-03"))
	if entry.ID == 0 || entry.Minutes != 90 || entry.UserID != u.ID {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestDateParsesUTC(t *testing.T) {
	d := testkit.Date("2026-01-05")
	if !d.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) || d.Location() != time.UTC {
		t.Fatalf("Date = %v (%v), want 2026-01-05 UTC", d, d.Location())
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Date on a bad literal did not panic")
		}
	}()
	testkit.Date("05/01/2026")
}

func TestGetEnvOptionReachesHandler(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	c := e.Contact(org)
	owner := e.User("owner@example.com")
	e.Member(owner, org, auth.Owner)
	// Bank details alone are one pay option; Stripe keys from GetEnv add a
	// second, so a rent invoice can be issued only when GetEnv is applied.
	if _, err := e.Pool.Exec(e.Ctx, "UPDATE orgs SET bank_bsb = '062-000', bank_account = '12345678' WHERE id = $1", org.ID); err != nil {
		t.Fatalf("setting bank details: %v", err)
	}
	stripeEnv := func(key string) string {
		switch key {
		case "SHIGOTO_STRIPE_ACME_SECRET_KEY":
			return "sk_test_dummy"
		case "SHIGOTO_STRIPE_ACME_WEBHOOK_SECRET":
			return "whsec_test_dummy"
		}
		return ""
	}
	fake := testkit.Payments(func(string) (payments.Provider, error) { return &payments.Fake{Secret: "whsec_test_dummy"}, nil })
	issue := func(opts ...testkit.HandlerOption) int {
		draft := e.Invoice(org, c.ID, testkit.Source("rent"))
		return e.Login(owner, e.Handler(opts...)).Do(http.MethodPost, fmt.Sprintf("/api/v1/orgs/acme/invoices/%d/issue", draft.ID), nil).Code
	}

	if got := issue(fake); got != http.StatusUnprocessableEntity {
		t.Fatalf("issue with default GetEnv = %d, want 422", got)
	}
	if got := issue(fake, testkit.GetEnv(stripeEnv)); got != http.StatusOK {
		t.Fatalf("issue with Stripe GetEnv = %d, want 200", got)
	}
}

func TestInviteTokenResolves(t *testing.T) {
	e := testkit.New(t)
	org := e.Org("acme")
	if id := e.Invite(org, "live@example.com", auth.Staff, "live-token"); id == 0 {
		t.Fatal("Invite returned id 0")
	}
	e.Invite(org, "late@example.com", auth.Staff, "late-token", testkit.Expired())
	s := e.Anonymous(e.Handler())
	for token, wantExpired := range map[string]bool{"live-token": false, "late-token": true} {
		r := s.Do(http.MethodGet, "/api/v1/invites/"+token, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("GET invite %s = %d, body %s", token, r.Code, r.Body)
		}
		var got struct {
			Expired bool `json:"expired"`
		}
		r.JSON(&got)
		if got.Expired != wantExpired {
			t.Errorf("invite %s expired = %v, want %v", token, got.Expired, wantExpired)
		}
	}
}
