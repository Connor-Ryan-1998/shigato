// Package table loads YAML test tables into typed case slices.
package table

import (
	"bytes"
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
	return cases
}
