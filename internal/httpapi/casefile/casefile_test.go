package casefile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// writeYAML writes body to a temp file named name and returns its path.
func writeYAML(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// validFile is a minimal file that passes Validate; tests mutate a copy.
func validFile() File {
	return File{
		Path: "/tmp/cases/example.yaml",
		Seed: Seed{
			Orgs:     []string{"acme", "other"},
			Users:    map[string]SeedUser{"owner": {Org: "acme", Role: "owner"}},
			Contacts: map[string]SeedContact{"c_foreign": {Org: "other", Name: "Foreign"}},
			Invoices: map[string]SeedInvoice{"inv_foreign": {Org: "other", Contact: "c_foreign"}},
		},
		Cases: []Case{{
			Name:    "owner cannot read foreign invoice",
			As:      Actors{"owner"},
			Request: Request{Method: "GET", Path: "/api/v1/orgs/acme/invoices/{inv_foreign}"},
			Expect:  Expect{Status: 404},
		}},
	}
}

func requireErrContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want one containing %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not contain %q", err, p)
		}
	}
}

func TestValidFilePasses(t *testing.T) {
	if err := validFile().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestInterpolateUnknownTokenFails(t *testing.T) {
	ids := map[string]int64{"inv_foreign": 7}
	_, err := Interpolate("/api/v1/orgs/acme/invoices/{inv_foriegn}", ids)
	requireErrContains(t, err, "inv_foriegn")

	f := validFile()
	f.Cases[0].Request.Path = "/api/v1/orgs/acme/invoices/{inv_foriegn}"
	requireErrContains(t, f.Validate(), "example.yaml", f.Cases[0].Name, "inv_foriegn")
}

func TestInterpolateReplacesKnownTokens(t *testing.T) {
	got, err := Interpolate("/x/{a}/y/{b}", map[string]int64{"a": 1, "b": 22})
	if err != nil || got != "/x/1/y/22" {
		t.Fatalf("Interpolate = %q, %v; want /x/1/y/22", got, err)
	}
	_, err = Interpolate("/x/{a", map[string]int64{"a": 1})
	requireErrContains(t, err, "{a")
}

func TestInterpolateBodyWholeTokenBecomesNumber(t *testing.T) {
	body := map[string]any{
		"contact_id": "{c}",
		"note":       "for {c}",
		"lines":      []any{map[string]any{"ref": "{c}"}},
	}
	got, err := InterpolateBody(body, map[string]int64{"c": 5})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"contact_id": int64(5),
		"note":       "for 5",
		"lines":      []any{map[string]any{"ref": int64(5)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InterpolateBody = %#v, want %#v", got, want)
	}
	_, err = InterpolateBody(map[string]any{"x": []any{"{nope}"}}, nil)
	requireErrContains(t, err, "nope")

	f := validFile()
	f.Cases[0].Request.Body = map[string]any{"contact_id": "{c_foriegn}"}
	requireErrContains(t, f.Validate(), f.Cases[0].Name, "c_foriegn")
}

func TestSubsetNumberNormalisation(t *testing.T) {
	var want any
	if err := yaml.Unmarshal([]byte("amount: 1000"), &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{"amount": float64(1000), "extra": true}
	if ok, diff := Subset(want, got); !ok {
		t.Fatalf("YAML 1000 vs JSON 1000: not ok: %s", diff)
	}
	if ok, _ := Subset(map[string]any{"amount": "1000"}, got); ok {
		t.Fatal(`"1000" matched JSON number 1000`)
	}
	if ok, diff := Subset(int64(9007199254740993), 9007199254740993); !ok {
		t.Fatalf("large int64 did not match itself: %s", diff)
	}
}

func TestSubsetMissingKeyDiffPath(t *testing.T) {
	want := map[string]any{"error": map[string]any{"field": "name"}}
	got := map[string]any{"error": map[string]any{"message": "bad"}}
	ok, diff := Subset(want, got)
	if ok {
		t.Fatal("missing key matched")
	}
	if !strings.Contains(diff, "error.field") {
		t.Fatalf("diff %q does not name error.field", diff)
	}
}

func TestSubsetArrayLengthMismatch(t *testing.T) {
	want := map[string]any{"items": []any{1, 2}}
	got := map[string]any{"items": []any{1, 2, 3}}
	ok, diff := Subset(want, got)
	if ok {
		t.Fatal("length mismatch matched")
	}
	if !strings.Contains(diff, "items") || !strings.Contains(diff, "length") {
		t.Fatalf("diff %q does not explain the items length", diff)
	}
	ok, diff = Subset([]any{map[string]any{"a": 1}}, []any{map[string]any{"a": 2}})
	if ok || !strings.Contains(diff, "[0].a") {
		t.Fatalf("element mismatch: ok=%v diff=%q, want [0].a", ok, diff)
	}
}

func TestValidateRejectsSharedSeedWrite(t *testing.T) {
	f := validFile()
	f.Cases = append(f.Cases, Case{
		Name:    "owner creates contact",
		As:      Actors{"owner"},
		Request: Request{Method: "POST", Path: "/api/v1/orgs/acme/contacts", Body: map[string]any{"name": "X"}},
		Expect:  Expect{Status: 201},
	})
	requireErrContains(t, f.Validate(), "owner creates contact", "isolate")

	f.Cases[1].Isolate = true
	if err := f.Validate(); err != nil {
		t.Fatalf("isolated write rejected: %v", err)
	}
}

func TestValidateRejectsUnknownActor(t *testing.T) {
	f := validFile()
	f.Cases[0].As = Actors{"owner", "keepr"}
	requireErrContains(t, f.Validate(), f.Cases[0].Name, "keepr")

	f.Cases[0].As = Actors{"anonymous"}
	if err := f.Validate(); err != nil {
		t.Fatalf("anonymous rejected: %v", err)
	}
}

func TestValidatePortalUserNeedsContact(t *testing.T) {
	f := validFile()
	f.Seed.Users["tenant"] = SeedUser{Org: "acme", Role: "tenant"}
	requireErrContains(t, f.Validate(), "tenant", "contact")
}

func TestValidateRejectsBadSeedAndCases(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*File)
		want   []string
	}{
		{"empty name", func(f *File) { f.Cases[0].Name = "" }, []string{"name"}},
		{"duplicate name", func(f *File) { f.Cases = append(f.Cases, f.Cases[0]) }, []string{"duplicate", f0name}},
		{"empty as", func(f *File) { f.Cases[0].As = nil }, []string{f0name, "as"}},
		{"zero status", func(f *File) { f.Cases[0].Expect.Status = 0 }, []string{f0name, "status"}},
		{"unknown user org", func(f *File) { f.Seed.Users["owner"] = SeedUser{Org: "nope", Role: "owner"} }, []string{"owner", "nope"}},
		{"unknown role", func(f *File) { f.Seed.Users["owner"] = SeedUser{Org: "acme", Role: "admin"} }, []string{"owner", "admin"}},
		{"unknown invoice contact", func(f *File) { f.Seed.Invoices["inv_foreign"] = SeedInvoice{Org: "other", Contact: "ghost"} }, []string{"inv_foreign", "ghost"}},
		{"bad invoice status", func(f *File) {
			f.Seed.Invoices["inv_foreign"] = SeedInvoice{Org: "other", Contact: "c_foreign", Status: "paid"}
		}, []string{"inv_foreign", "paid"}},
		{"unknown settings org", func(f *File) { f.Seed.Settings = map[string]SeedSettings{"nope": {}} }, []string{"settings", "nope"}},
		{"unknown module", func(f *File) { f.Seed.Modules = map[string]bool{"payroll": false} }, []string{"payroll"}},
		{"duplicate name across maps", func(f *File) {
			f.Seed.Properties = map[string]SeedProperty{"c_foreign": {Org: "other"}}
		}, []string{"c_foreign", "duplicate"}},
		{"unknown lease property", func(f *File) {
			f.Seed.Leases = map[string]SeedLease{"l": {Org: "other", Property: "p", Tenant: "c_foreign"}}
		}, []string{"l", "p"}},
		{"unknown project client", func(f *File) { f.Seed.Projects = map[string]SeedProject{"pr": {Org: "acme", Client: "zz"}} }, []string{"pr", "zz"}},
		{"unknown invite org", func(f *File) {
			f.Seed.Invites = map[string]SeedInvite{"iv": {Org: "nope", Email: "a@example.com", Role: "staff", Token: "t"}}
		}, []string{"invites.iv", "nope"}},
		{"invite portal role", func(f *File) {
			f.Seed.Invites = map[string]SeedInvite{"iv": {Org: "acme", Email: "a@example.com", Role: "tenant", Token: "t"}}
		}, []string{"invites.iv", "tenant"}},
		{"invite missing email and token", func(f *File) {
			f.Seed.Invites = map[string]SeedInvite{"iv": {Org: "acme", Role: "staff"}}
		}, []string{"invites.iv: email is required", "invites.iv: token is required"}},
		{"duplicate invite token", func(f *File) {
			f.Seed.Invites = map[string]SeedInvite{
				"iv1": {Org: "acme", Email: "a@example.com", Role: "staff", Token: "same"},
				"iv2": {Org: "acme", Email: "b@example.com", Role: "staff", Token: "same"},
			}
		}, []string{"invites.iv2", "same", "invites.iv1"}},
		{"invite name collides", func(f *File) {
			f.Seed.Invites = map[string]SeedInvite{"owner": {Org: "acme", Email: "a@example.com", Role: "staff", Token: "t"}}
		}, []string{"owner", "duplicate"}},
		{"unknown task project", func(f *File) {
			f.Seed.Tasks = map[string]SeedTask{"tk": {Org: "acme", Project: "ghost"}}
		}, []string{"tasks.tk", "ghost"}},
		{"cross-org task project", func(f *File) {
			f.Seed.Projects = map[string]SeedProject{"pr": {Org: "other", Client: "c_foreign"}}
			f.Seed.Tasks = map[string]SeedTask{"tk": {Org: "acme", Project: "pr"}}
		}, []string{"tasks.tk", "pr", "other"}},
		{"negative task rate", func(f *File) {
			f.Seed.Projects = map[string]SeedProject{"pr": {Org: "other", Client: "c_foreign"}}
			f.Seed.Tasks = map[string]SeedTask{"tk": {Org: "other", Project: "pr", Rate: -1}}
		}, []string{"tasks.tk", "negative rate"}},
		{"task name collides", func(f *File) {
			f.Seed.Projects = map[string]SeedProject{"pr": {Org: "other", Client: "c_foreign"}}
			f.Seed.Tasks = map[string]SeedTask{"owner": {Org: "other", Project: "pr"}}
		}, []string{"owner", "duplicate"}},
		{"cross-org contact", func(f *File) {
			f.Seed.Users["tenant"] = SeedUser{Org: "acme", Role: "tenant", Contact: "c_foreign"}
		}, []string{"tenant", "c_foreign", "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFile()
			tt.mutate(&f)
			requireErrContains(t, f.Validate(), append([]string{"example.yaml"}, tt.want...)...)
		})
	}
}

const f0name = "owner cannot read foreign invoice"

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeYAML(t, "typo.yaml", `
seed:
  orgs: [acme]
  users:
    owner: {org: acme, role: owner}
cases:
  - name: list
    as: owner
    request: {method: GET, path: /api/v1/orgs/acme/contacts}
    expcet: {status: 200}
`)
	_, err := Load(path)
	requireErrContains(t, err, "typo.yaml", "expcet")
}

func TestLoadValidates(t *testing.T) {
	path := writeYAML(t, "bad.yaml", `
seed:
  orgs: [acme]
cases:
  - name: list
    as: ghost
    request: {method: GET, path: /api/v1/orgs/acme/contacts}
    expect: {status: 200}
`)
	_, err := Load(path)
	requireErrContains(t, err, "bad.yaml", "ghost")
}

func TestActorsScalarOrList(t *testing.T) {
	path := writeYAML(t, "actors.yaml", `
seed:
  orgs: [acme]
  users:
    owner: {org: acme, role: owner}
    keeper: {org: acme, role: bookkeeper}
cases:
  - name: scalar
    as: owner
    request: {method: GET, path: /api/v1/orgs/acme/contacts}
    expect: {status: 200}
  - name: list
    as: [owner, keeper]
    request: {method: GET, path: /api/v1/orgs/acme/contacts}
    expect: {status: 200}
`)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != path {
		t.Errorf("Path = %q, want %q", f.Path, path)
	}
	if !reflect.DeepEqual(f.Cases[0].As, Actors{"owner"}) {
		t.Errorf("scalar as = %v", f.Cases[0].As)
	}
	if !reflect.DeepEqual(f.Cases[1].As, Actors{"owner", "keeper"}) {
		t.Errorf("list as = %v", f.Cases[1].As)
	}

	var a Actors
	err = yaml.Unmarshal([]byte("{x: 1}"), &a)
	requireErrContains(t, err, "as")
}

func TestValidInviteSeedInterpolates(t *testing.T) {
	f := validFile()
	f.Seed.Invites = map[string]SeedInvite{"iv": {Org: "other", Email: "a@example.com", Role: "bookkeeper", Token: "t", Expired: true}}
	f.Cases[0].Request = Request{Method: "DELETE", Path: "/api/v1/orgs/acme/invites/{iv}"}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidTaskSeedInterpolates(t *testing.T) {
	path := writeYAML(t, "tasks.yaml", `
seed:
  orgs: [acme]
  users:
    owner: {org: acme, role: owner}
  contacts:
    c_client: {org: acme, kind: client}
  projects:
    pr: {org: acme, client: c_client}
  tasks:
    tk: {org: acme, project: pr, name: Design, rate: 5000}
cases:
  - name: edit task
    as: owner
    request: {method: PUT, path: "/api/v1/orgs/acme/tasks/{tk}", body: {name: Design}}
    expect: {status: 422}
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := SeedTask{Org: "acme", Project: "pr", Name: "Design", Rate: 5000}
	if got := f.Seed.Tasks["tk"]; got != want {
		t.Fatalf("tasks.tk = %+v, want %+v", got, want)
	}
}

func TestEqualExtraKeyFails(t *testing.T) {
	want := map[string]any{"error": "not found"}
	ok, diff := Equal(want, map[string]any{"error": "not found", "invoice": map[string]any{"id": 7}})
	if ok {
		t.Fatal("Equal passed with an extra key")
	}
	if !strings.Contains(diff, "invoice") || !strings.Contains(diff, "unexpected key") {
		t.Fatalf("diff %q does not name the extra key", diff)
	}
	// Nested: the path names the parent.
	ok, diff = Equal(map[string]any{"a": map[string]any{"b": 1}}, map[string]any{"a": map[string]any{"b": 1, "c": 2}})
	if ok || !strings.Contains(diff, "a.c") {
		t.Fatalf("nested extra key: ok=%v diff=%q, want a failure at a.c", ok, diff)
	}
	// Subset still ignores the extra key.
	if ok, diff := Subset(want, map[string]any{"error": "not found", "invoice": 7}); !ok {
		t.Fatalf("Subset failed on an extra key: %s", diff)
	}
}

func TestEqualEqualPasses(t *testing.T) {
	want := map[string]any{"error": "forbidden", "n": 1000, "xs": []any{1, "a"}}
	got := map[string]any{"error": "forbidden", "n": 1000.0, "xs": []any{1, "a"}}
	if ok, diff := Equal(want, got); !ok {
		t.Fatalf("Equal failed on equal bodies: %s", diff)
	}
	if ok, _ := Equal(map[string]any{"n": 1000}, map[string]any{"n": "1000"}); ok {
		t.Fatal(`Equal matched 1000 to "1000"`)
	}
}

func TestLoadRejectsExactWithoutBody(t *testing.T) {
	path := writeYAML(t, "exact.yaml", `
seed:
  orgs: [acme]
  users:
    owner: {org: acme, role: owner}
cases:
  - name: exact with nothing to match
    as: owner
    request: {method: GET, path: /api/v1/orgs/acme/contacts}
    expect: {status: 200, exact: true}
`)
	_, err := Load(path)
	requireErrContains(t, err, "exact.yaml", "exact with nothing to match", "expect.exact")
}

// TestDecodeBodyKeepsLargeIntegersExact pins the runner's decode path: a
// response integer above 2^53 must not round onto a neighbouring value.
func TestDecodeBodyKeepsLargeIntegersExact(t *testing.T) {
	got, err := DecodeBody([]byte(`{"total": 9007199254740993}`))
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if ok, _ := Subset(map[string]any{"total": int64(9007199254740992)}, got); ok {
		t.Fatal("9007199254740992 matched 9007199254740993 after decoding")
	}
	if ok, diff := Subset(map[string]any{"total": int64(9007199254740993)}, got); !ok {
		t.Fatalf("exact value did not match: %s", diff)
	}
}

func TestDecodeBodyRejectsNonJSON(t *testing.T) {
	if _, err := DecodeBody([]byte(`{"a":1} trailing`)); err == nil {
		t.Fatal("DecodeBody accepted trailing data")
	}
}
