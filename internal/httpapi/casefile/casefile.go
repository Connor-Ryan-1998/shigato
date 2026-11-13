// Package casefile loads and validates the YAML HTTP case files under
// internal/httpapi/testdata/cases. A file declares a seed (orgs, users and
// records, each named) and a list of cases: one request sent as one or more
// actors, with the expected status, body subset and headers, plus the
// tables the request must leave unchanged.
//
// The format is deliberately flat: no loops, conditionals or step
// sequencing. The only expansion is a list under `as:`; the only
// interpolation is `{seedName}`, which resolves to the seeded row's ID.
// Unknown keys, unknown names and shared-seed writes are load errors, so a
// typo cannot silently turn into a passing "not found" case.
package casefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"shigoto/internal/config"
)

// Anonymous is the actor name for a request sent without a session.
const Anonymous = "anonymous"

// File is one YAML case file.
type File struct {
	Path  string `yaml:"-"`
	Seed  Seed   `yaml:"seed"`
	Cases []Case `yaml:"cases"`
}

// Seed is the data created before a file's cases run. Every map key is a
// seed name; names are unique across Users, Contacts, Invoices, Properties,
// Leases, Projects and Invites and resolve to row IDs in `{name}` tokens.
type Seed struct {
	Orgs       []string                `yaml:"orgs"`
	Modules    map[string]bool         `yaml:"modules"` // nil: all enabled
	Users      map[string]SeedUser     `yaml:"users"`   // key is the actor name
	Contacts   map[string]SeedContact  `yaml:"contacts"`
	Invoices   map[string]SeedInvoice  `yaml:"invoices"`
	Properties map[string]SeedProperty `yaml:"properties"`
	Leases     map[string]SeedLease    `yaml:"leases"`
	Projects   map[string]SeedProject  `yaml:"projects"`
	Invites    map[string]SeedInvite   `yaml:"invites"`
	Settings   map[string]SeedSettings `yaml:"settings"` // key is the org slug
}

// SeedUser is a member of Org with Role. Contact (a seed contact name in
// the same org) is required for tenant and client, and rejected otherwise.
type SeedUser struct {
	Org     string `yaml:"org"`
	Role    string `yaml:"role"`
	Contact string `yaml:"contact"`
}

// SeedContact is a contact in Org. Empty fields take testkit defaults.
type SeedContact struct {
	Org   string `yaml:"org"`
	Kind  string `yaml:"kind"`
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

// SeedInvoice is an invoice in Org to the seed contact Contact. Status is
// draft (the default) or issued; Amount 0 takes the testkit default.
type SeedInvoice struct {
	Org     string `yaml:"org"`
	Contact string `yaml:"contact"`
	Status  string `yaml:"status"`
	Source  string `yaml:"source"`
	Amount  int64  `yaml:"amount"`
}

// SeedProperty is a property in Org, residential unless Commercial.
type SeedProperty struct {
	Org        string `yaml:"org"`
	Name       string `yaml:"name"`
	Commercial bool   `yaml:"commercial"`
}

// SeedLease is a lease on the seed property Property with the seed contact
// Tenant as primary tenant. Rent 0 takes the testkit default.
type SeedLease struct {
	Org      string `yaml:"org"`
	Property string `yaml:"property"`
	Tenant   string `yaml:"tenant"`
	Rent     int64  `yaml:"rent"`
}

// SeedProject is a timesheets project for the seed contact Client.
type SeedProject struct {
	Org    string `yaml:"org"`
	Client string `yaml:"client"`
}

// SeedInvite is an outstanding staff invite to Org for Email with Role.
// Token is the plaintext invite token, written literally into accept paths
// (`/api/v1/invites/<token>`); it must be unique within the file. Expired
// makes the invite already past its expiry.
type SeedInvite struct {
	Org     string `yaml:"org"`
	Email   string `yaml:"email"`
	Role    string `yaml:"role"`
	Token   string `yaml:"token"`
	Expired bool   `yaml:"expired"`
}

// SeedSettings sets org flags directly.
type SeedSettings struct {
	GSTRegistered bool `yaml:"gst_registered"`
	PayOptions    bool `yaml:"pay_options"`
}

// Case is one request, run once per actor in As.
type Case struct {
	Name      string   `yaml:"name"`
	As        Actors   `yaml:"as"` // scalar or list; "anonymous" allowed
	Request   Request  `yaml:"request"`
	Expect    Expect   `yaml:"expect"`
	Unchanged []string `yaml:"unchanged"`
	Isolate   bool     `yaml:"isolate"`
}

// Actors is the `as:` list. It accepts a scalar or a sequence.
type Actors []string

// UnmarshalYAML accepts `as: owner` or `as: [owner, keeper]`.
func (a *Actors) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*a = Actors{n.Value}
		return nil
	case yaml.SequenceNode:
		out := make(Actors, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: as: each actor must be a name", c.Line)
			}
			out = append(out, c.Value)
		}
		*a = out
		return nil
	default:
		return fmt.Errorf("line %d: as: want an actor name or a list of names", n.Line)
	}
}

// Request is the HTTP request a case sends. Path and Body may contain
// `{seedName}` tokens. Headers are applied after the runner's defaults.
type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Body    any               `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
}

// Expect is what the response must look like. Body is a subset match (see
// Subset), or with Exact a whole-body match (see Equal), and may contain
// `{seedName}` tokens; Headers are exact values for the listed names only.
type Expect struct {
	Status  int               `yaml:"status"`
	Body    any               `yaml:"body"`
	Exact   bool              `yaml:"exact"`
	Headers map[string]string `yaml:"headers"`
}

var methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

var roles = []string{"owner", "staff", "bookkeeper", "tenant", "client"}

var inviteRoles = []string{"owner", "staff", "bookkeeper"}

// Load reads and strictly decodes path (unknown keys fail), then validates it.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	f.Path = path
	if err := f.Validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

// seedNames returns every interpolatable seed name, each mapped to the name
// of the seed map it came from.
func (s Seed) seedNames() map[string]string {
	names := map[string]string{}
	add := func(kind string, keys []string) {
		for _, k := range keys {
			if _, dup := names[k]; !dup {
				names[k] = kind
			}
		}
	}
	add("users", sortedKeys(s.Users))
	add("contacts", sortedKeys(s.Contacts))
	add("invoices", sortedKeys(s.Invoices))
	add("properties", sortedKeys(s.Properties))
	add("leases", sortedKeys(s.Leases))
	add("projects", sortedKeys(s.Projects))
	add("invites", sortedKeys(s.Invites))
	return names
}

// Validate reports every problem in f, each prefixed with the file name and
// the seed entry or case it concerns.
func (f File) Validate() error {
	file := filepath.Base(f.Path)
	var errs []error
	seedErr := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: seed: %s", file, fmt.Sprintf(format, args...)))
	}
	s := f.Seed

	orgs := map[string]bool{}
	for _, o := range s.Orgs {
		if o == "" {
			seedErr("orgs: empty slug")
		}
		if orgs[o] {
			seedErr("orgs: duplicate org %q", o)
		}
		orgs[o] = true
	}
	checkOrg := func(where, org string) {
		if !orgs[org] {
			seedErr("%s: unknown org %q", where, org)
		}
	}
	for _, m := range sortedKeys(s.Modules) {
		if !slices.Contains(config.Known, m) {
			seedErr("modules: unknown module %q (known: %s)", m, strings.Join(config.Known, ", "))
		}
	}

	// Names must be unique across the interpolatable maps.
	seen := map[string]string{}
	for _, group := range []struct {
		kind string
		keys []string
	}{
		{"users", sortedKeys(s.Users)},
		{"contacts", sortedKeys(s.Contacts)},
		{"invoices", sortedKeys(s.Invoices)},
		{"properties", sortedKeys(s.Properties)},
		{"leases", sortedKeys(s.Leases)},
		{"projects", sortedKeys(s.Projects)},
		{"invites", sortedKeys(s.Invites)},
	} {
		for _, k := range group.keys {
			if prev, dup := seen[k]; dup {
				seedErr("duplicate name %q in %s and %s", k, prev, group.kind)
				continue
			}
			seen[k] = group.kind
		}
	}
	if _, ok := s.Users[Anonymous]; ok {
		seedErr("users: %q is reserved for requests without a session", Anonymous)
	}

	// checkContact requires ref to be a seed contact in org.
	checkContact := func(where, field, ref, org string) {
		c, ok := s.Contacts[ref]
		switch {
		case ref == "":
			seedErr("%s: %s is required", where, field)
		case !ok:
			seedErr("%s: %s: unknown contact %q", where, field, ref)
		case c.Org != org:
			seedErr("%s: %s: contact %q is in org %q, not %q", where, field, ref, c.Org, org)
		}
	}

	for _, name := range sortedKeys(s.Users) {
		u := s.Users[name]
		where := "users." + name
		checkOrg(where, u.Org)
		if !slices.Contains(roles, u.Role) {
			seedErr("%s: unknown role %q (known: %s)", where, u.Role, strings.Join(roles, ", "))
			continue
		}
		if u.Role == "tenant" || u.Role == "client" {
			checkContact(where, "contact", u.Contact, u.Org)
		} else if u.Contact != "" {
			seedErr("%s: contact is only for tenant and client users, not %s", where, u.Role)
		}
	}
	for _, name := range sortedKeys(s.Contacts) {
		checkOrg("contacts."+name, s.Contacts[name].Org)
	}
	for _, name := range sortedKeys(s.Invoices) {
		inv := s.Invoices[name]
		where := "invoices." + name
		checkOrg(where, inv.Org)
		checkContact(where, "contact", inv.Contact, inv.Org)
		switch inv.Status {
		case "", "draft", "issued":
		default:
			seedErr("%s: status %q must be draft or issued", where, inv.Status)
		}
		if inv.Amount < 0 {
			seedErr("%s: negative amount %d", where, inv.Amount)
		}
	}
	for _, name := range sortedKeys(s.Properties) {
		checkOrg("properties."+name, s.Properties[name].Org)
	}
	for _, name := range sortedKeys(s.Leases) {
		l := s.Leases[name]
		where := "leases." + name
		checkOrg(where, l.Org)
		if p, ok := s.Properties[l.Property]; !ok {
			seedErr("%s: property: unknown property %q", where, l.Property)
		} else if p.Org != l.Org {
			seedErr("%s: property: property %q is in org %q, not %q", where, l.Property, p.Org, l.Org)
		}
		checkContact(where, "tenant", l.Tenant, l.Org)
		if l.Rent < 0 {
			seedErr("%s: negative rent %d", where, l.Rent)
		}
	}
	for _, name := range sortedKeys(s.Projects) {
		p := s.Projects[name]
		where := "projects." + name
		checkOrg(where, p.Org)
		checkContact(where, "client", p.Client, p.Org)
	}
	tokens := map[string]string{}
	for _, name := range sortedKeys(s.Invites) {
		inv := s.Invites[name]
		where := "invites." + name
		checkOrg(where, inv.Org)
		if inv.Email == "" {
			seedErr("%s: email is required", where)
		}
		if !slices.Contains(inviteRoles, inv.Role) {
			seedErr("%s: role %q must be one of %s", where, inv.Role, strings.Join(inviteRoles, ", "))
		}
		switch prev, dup := tokens[inv.Token]; {
		case inv.Token == "":
			seedErr("%s: token is required", where)
		case dup:
			seedErr("%s: token %q is already used by invites.%s", where, inv.Token, prev)
		default:
			tokens[inv.Token] = name
		}
	}
	for _, org := range sortedKeys(s.Settings) {
		checkOrg("settings."+org, org)
	}

	// Cases. Interpolating against zero IDs checks every token is a name.
	ids := map[string]int64{}
	for name := range s.seedNames() {
		ids[name] = 0
	}
	names := map[string]bool{}
	for i, c := range f.Cases {
		label := fmt.Sprintf("case %q", c.Name)
		if c.Name == "" {
			label = fmt.Sprintf("case #%d", i+1)
		}
		caseErr := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: %s: %s", file, label, fmt.Sprintf(format, args...)))
		}
		if c.Name == "" {
			caseErr("name is required")
		} else if names[c.Name] {
			caseErr("duplicate case name")
		}
		names[c.Name] = true

		if len(c.As) == 0 {
			caseErr("as: at least one actor is required")
		}
		actors := map[string]bool{}
		for _, a := range c.As {
			if _, ok := s.Users[a]; !ok && a != Anonymous {
				caseErr("as: unknown actor %q (not in seed.users and not %q)", a, Anonymous)
			}
			if actors[a] {
				caseErr("as: duplicate actor %q", a)
			}
			actors[a] = true
		}

		if !slices.Contains(methods, c.Request.Method) {
			caseErr("request.method %q must be one of %s", c.Request.Method, strings.Join(methods, ", "))
		}
		if !strings.HasPrefix(c.Request.Path, "/") {
			caseErr("request.path %q must start with /", c.Request.Path)
		}
		if _, err := Interpolate(c.Request.Path, ids); err != nil {
			caseErr("request.path: %v", err)
		}
		if _, err := InterpolateBody(c.Request.Body, ids); err != nil {
			caseErr("request.body: %v", err)
		}
		if _, err := InterpolateBody(c.Expect.Body, ids); err != nil {
			caseErr("expect.body: %v", err)
		}

		if c.Expect.Exact && c.Expect.Body == nil {
			caseErr("expect.exact needs an expect.body")
		}
		if c.Expect.Status == 0 {
			caseErr("expect.status is required")
		} else if c.Expect.Status < 100 || c.Expect.Status > 599 {
			caseErr("expect.status %d is not an HTTP status", c.Expect.Status)
		}
		write := c.Request.Method != "GET" && c.Request.Method != "HEAD"
		if write && c.Expect.Status >= 200 && c.Expect.Status < 300 && !c.Isolate {
			caseErr("%s expecting %d writes to the shared seed; set isolate: true", c.Request.Method, c.Expect.Status)
		}
		for _, tbl := range c.Unchanged {
			if tbl == "" {
				caseErr("unchanged: empty table name")
			}
		}
	}
	return errors.Join(errs...)
}

// Interpolate replaces each `{name}` in s with ids[name]. A `{` that does
// not open a known name (or is never closed) is an error naming the token.
func Interpolate(s string, ids map[string]int64) (string, error) {
	var b strings.Builder
	for {
		open := strings.IndexByte(s, '{')
		if open < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		b.WriteString(s[:open])
		end := strings.IndexByte(s[open:], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed token %q", s[open:])
		}
		token := s[open+1 : open+end]
		id, ok := ids[token]
		if !ok {
			return "", fmt.Errorf("unknown seed name {%s}", token)
		}
		b.WriteString(strconv.FormatInt(id, 10))
		s = s[open+end+1:]
	}
}

// InterpolateBody returns a copy of v with every string interpolated. A
// string that is exactly one `{name}` token becomes the int64 ID, so
// `contact_id: "{c_acme}"` sends a JSON number. Map keys are not
// interpolated.
func InterpolateBody(v any, ids map[string]int64) (any, error) {
	switch t := v.(type) {
	case string:
		if len(t) > 2 && t[0] == '{' && t[len(t)-1] == '}' && !strings.ContainsAny(t[1:len(t)-1], "{}") {
			id, ok := ids[t[1:len(t)-1]]
			if !ok {
				return nil, fmt.Errorf("unknown seed name %s", t)
			}
			return id, nil
		}
		return Interpolate(t, ids)
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range sortedKeys(t) {
			iv, err := InterpolateBody(t[k], ids)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = iv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			iv, err := InterpolateBody(e, ids)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = iv
		}
		return out, nil
	default:
		return v, nil
	}
}

// Equal reports whether got equals want exactly, after the same JSON
// normalisation as Subset: extra keys in got objects fail too. diff names
// the JSON path of the first difference.
func Equal(want, got any) (ok bool, diff string) {
	return compare(want, got, true)
}

// Subset reports whether want is contained in got, after normalising both
// through a JSON round trip: every wanted map key must be present with a
// matching value (extra keys in got are ignored), slices must have equal
// length and match element-wise, and scalars must be equal, with numbers
// compared exactly by value and never equal to strings. diff names the
// JSON path of the first mismatch.
func Subset(want, got any) (ok bool, diff string) {
	return compare(want, got, false)
}

func compare(want, got any, exact bool) (ok bool, diff string) {
	w, err := normalise(want)
	if err != nil {
		return false, fmt.Sprintf("encoding expected body: %v", err)
	}
	g, err := normalise(got)
	if err != nil {
		return false, fmt.Sprintf("encoding actual body: %v", err)
	}
	if d := subset(w, g, "", exact); d != "" {
		return false, d
	}
	return true, ""
}

func normalise(v any) (any, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// subset returns the first place got does not contain want, or "". With
// exact, a key in a got object that want lacks is also a difference.
func subset(want, got any, path string, exact bool) string {
	at := path
	if at == "" {
		at = "(root)"
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: got %s, want an object", at, show(got))
		}
		for _, k := range sortedKeys(w) {
			child := k
			if path != "" {
				child = path + "." + k
			}
			gv, present := g[k]
			if !present {
				return fmt.Sprintf("%s: missing key (want %s)", child, show(w[k]))
			}
			if d := subset(w[k], gv, child, exact); d != "" {
				return d
			}
		}
		if exact {
			for _, k := range sortedKeys(g) {
				if _, wanted := w[k]; !wanted {
					child := k
					if path != "" {
						child = path + "." + k
					}
					return fmt.Sprintf("%s: unexpected key (got %s)", child, show(g[k]))
				}
			}
		}
		return ""
	case []any:
		g, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("%s: got %s, want an array", at, show(got))
		}
		if len(g) != len(w) {
			return fmt.Sprintf("%s: array length got %d want %d (got %s)", at, len(g), len(w), show(got))
		}
		for i := range w {
			if d := subset(w[i], g[i], fmt.Sprintf("%s[%d]", path, i), exact); d != "" {
				return d
			}
		}
		return ""
	case json.Number:
		g, ok := got.(json.Number)
		if !ok || !numbersEqual(w, g) {
			return fmt.Sprintf("%s: got %s, want %s", at, show(got), show(want))
		}
		return ""
	default:
		if want != got {
			return fmt.Sprintf("%s: got %s, want %s", at, show(got), show(want))
		}
		return ""
	}
}

func numbersEqual(a, b json.Number) bool {
	x, okx := new(big.Rat).SetString(a.String())
	y, oky := new(big.Rat).SetString(b.String())
	return okx && oky && x.Cmp(y) == 0
}

// show renders a normalised value as compact JSON, with its type for scalars
// so `"1000" (string)` and `1000 (number)` are distinguishable.
func show(v any) string {
	buf, _ := json.Marshal(v)
	s := string(buf)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	switch v.(type) {
	case string:
		return s + " (string)"
	case json.Number:
		return s + " (number)"
	case bool:
		return s + " (bool)"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
