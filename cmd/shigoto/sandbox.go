package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"shigoto/internal/config"
	"shigoto/internal/db"
	"shigoto/internal/sandbox"
)

func adminPopulateSandbox(args []string) error {
	fs := flag.NewFlagSet("populate-sbx", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("populate-sbx takes no arguments")
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	result, err := sandbox.Populate(ctx, pool, time.Now().In(cfg.Location))
	if err != nil {
		return err
	}
	if err := writeSandboxAccess(os.Stdout, cfg.BaseURL, result); err != nil {
		return fmt.Errorf("sandbox committed but access details could not be written; rerun populate-sbx to generate new credentials: %w", err)
	}
	return nil
}

func writeSandboxAccess(w io.Writer, baseURL string, result sandbox.Result) error {
	var out strings.Builder
	fmt.Fprintln(&out, "Local development sandbox populated.")
	fmt.Fprintln(&out, "Rerunning populate-sbx replaces sandbox edits and rotates staff passwords. Unrelated data is preserved.")
	fmt.Fprintln(&out, "Use only fictional data. Payment details are placeholders; do not send real funds or configure live payment credentials.")
	fmt.Fprintf(&out, "\nLogin: %s/login\n", baseURL)
	for _, org := range result.Orgs {
		fmt.Fprintf(&out, "Organisation: %s/%s\n", baseURL, org.Slug)
	}
	fmt.Fprintln(&out, "\nStaff accounts (email | role | password):")
	for _, account := range result.Accounts {
		if account.Password != "" {
			fmt.Fprintf(&out, "%s | %s | %s\n", account.Email, account.Role, account.Password)
		}
	}
	fmt.Fprintln(&out, "The owner belongs to both organisations. Staff and bookkeeper belong to sbx-property.")
	fmt.Fprintln(&out, "\nPortal accounts (email | role | portal):")
	for _, account := range result.Accounts {
		if account.Password == "" {
			fmt.Fprintf(&out, "%s | %s | %s/portal/%s/invoices\n", account.Email, account.Role, baseURL, account.OrgSlug)
		}
	}
	fmt.Fprintln(&out, "Request a magic link on the normal login page; open its email in local Mailpit: http://localhost:8025")
	fmt.Fprintln(&out, "Accounts ending in -empty exercise empty portals. Use separate browser profiles/private windows for different roles.")
	fmt.Fprintln(&out, "Uploaded receipt bytes are not removed from disk by a reset.")
	_, err := io.WriteString(w, out.String())
	return err
}
