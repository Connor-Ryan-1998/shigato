// Package table loads YAML test tables into typed case slices.
package table

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Load decodes the top-level YAML sequence at path into []T. Unknown keys are
// rejected. On failure it calls t.Fatalf with the path and error, and returns
// nil if Fatalf does not stop the goroutine.
func Load[T any](t testing.TB, path string) []T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("table: read %s: %v", path, err)
		return nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cases []T
	if err := decoder.Decode(&cases); err != nil {
		t.Fatalf("table: decode %s: %v", path, err)
		return nil
	}
	if err := checkNames(data); err != nil {
		t.Fatalf("table: %s: %v", path, err)
		return nil
	}
	return cases
}

// checkNames requires every case to carry a unique, non-empty name, so each
// subtest is addressable and a copy-pasted case can't hide another.
func checkNames(data []byte) error {
	var named []struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &named); err != nil {
		return err
	}
	seen := make(map[string]bool, len(named))
	for i, c := range named {
		if c.Name == "" {
			return fmt.Errorf("case %d has no name", i+1)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate case name %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}
