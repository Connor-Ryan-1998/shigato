package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"shigoto/internal/core"
	"shigoto/internal/sandbox"
)

func TestPopulateSandboxRejectsArguments(t *testing.T) {
	t.Setenv("SHIGOTO_DATABASE_URL", "")
	for _, args := range [][]string{{"--reset-all"}, {"production"}} {
		if err := adminPopulateSandbox(args); err == nil || strings.Contains(err.Error(), "SHIGOTO_DATABASE_URL") {
			t.Fatalf("arguments %v reached config/database access: %v", args, err)
		}
	}
}

func TestSandboxAccessOutput(t *testing.T) {
	var out bytes.Buffer
	err := writeSandboxAccess(&out, "http://localhost:8080", sandbox.Result{
		Orgs: []core.Org{{Slug: "sbx-property"}, {Slug: "sbx-consulting"}},
		Accounts: []sandbox.Account{
			{Email: "owner@example.test", Role: "owner", OrgSlug: "sbx-property", Password: "runtime-only-test-password"},
			{Email: "tenant@example.test", Role: "tenant", OrgSlug: "sbx-property"},
			{Email: "client@example.test", Role: "client", OrgSlug: "sbx-consulting"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"http://localhost:8080/sbx-property", "http://localhost:8080/sbx-consulting",
		"http://localhost:8080/login", "owner@example.test", "runtime-only-test-password",
		"tenant@example.test", "client@example.test", "http://localhost:8025",
		"magic link", "replaces", "sandbox edits", "development",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("access output missing %q", want)
		}
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if (strings.Contains(line, "tenant@example.test") || strings.Contains(line, "client@example.test")) && strings.Contains(strings.ToLower(line), "password") {
			t.Errorf("portal account printed with password: %s", line)
		}
	}
}

type failingSandboxWriter struct{}

func (failingSandboxWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestSandboxAccessOutputFailure(t *testing.T) {
	if err := writeSandboxAccess(failingSandboxWriter{}, "http://localhost:8080", sandbox.Result{}); err == nil {
		t.Fatal("output failure was ignored")
	}
}
