package table_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shigoto/internal/testkit/table"
)

type sampleCase struct {
	Name string `yaml:"name"`
	In   int    `yaml:"in"`
	Want int    `yaml:"want"`
}

func TestLoadDecodesCases(t *testing.T) {
	cases := table.Load[sampleCase](t, filepath.Join("testdata", "sample.yaml"))
	if len(cases) != 2 {
		t.Fatalf("len(cases) = %d, want 2", len(cases))
	}
	if cases[1].Want != 6 {
		t.Fatalf("cases[1].Want = %d, want 6", cases[1].Want)
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

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("- {name: a, wnat: 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &recordingTB{TB: t}
	cases := table.Load[sampleCase](fake, path)
	if !fake.fatal {
		t.Fatalf("Load did not call Fatalf; got %v", cases)
	}
	if !strings.Contains(fake.message, path) || !strings.Contains(fake.message, "wnat") {
		t.Fatalf("Fatalf message %q must contain path %q and key wnat", fake.message, path)
	}
	if cases != nil {
		t.Fatalf("Load returned %v after Fatalf, want nil", cases)
	}
}

func TestLoadRejectsDuplicateOrMissingName(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"duplicate", "- {name: a, in: 1}\n- {name: a, in: 2}\n", `duplicate case name "a"`},
		{"missing", "- {name: a, in: 1}\n- {in: 2}\n", "case 2 has no name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "names.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			fake := &recordingTB{TB: t}
			cases := table.Load[sampleCase](fake, path)
			if !fake.fatal || !strings.Contains(fake.message, path) || !strings.Contains(fake.message, tc.want) {
				t.Fatalf("Fatalf called=%v message %q; want path %q and %q", fake.fatal, fake.message, path, tc.want)
			}
			if cases != nil {
				t.Fatalf("Load returned %v after Fatalf, want nil", cases)
			}
		})
	}
}
