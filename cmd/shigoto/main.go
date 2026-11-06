// Command shigoto runs the shigoto server.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"shigoto/internal/auth"
	"shigoto/internal/config"
	"shigoto/internal/core"
	"shigoto/internal/db"
	"shigoto/internal/httpapi"
	"shigoto/internal/scheduler"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			log.Fatal(err)
		}
	case "admin":
		if err := admin(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(1)
	}
}

func admin(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "create-owner":
		return adminCreateOwner(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q", sub)
	}
}

// slugPattern is what an org slug may contain: it appears in URLs and in
// SHIGOTO_STRIPE_<SLUG>_* env var names.
var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func adminCreateOwner(args []string) error {
	fs := flag.NewFlagSet("create-owner", flag.ContinueOnError)
	orgSlug := fs.String("org-slug", "", "org slug (required)")
	orgName := fs.String("org-name", "", "org name (required)")
	email := fs.String("email", "", "owner email (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *orgSlug == "" || *orgName == "" || *email == "" {
		return fmt.Errorf("--org-slug, --org-name and --email are required")
	}
	if !slugPattern.MatchString(*orgSlug) {
		return fmt.Errorf("--org-slug must match %s", slugPattern)
	}

	password, err := readPassword(os.Stdin)
	if err != nil {
		return err
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

	if err := createOwner(ctx, pool, *orgSlug, *orgName, *email, password); err != nil {
		return err
	}
	fmt.Printf("owner %s ready for org %s\n", *email, *orgSlug)
	return nil
}

// readPassword reads the first line from r, trims it, and rejects an empty
// password.
func readPassword(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return "", fmt.Errorf("password must not be empty")
	}
	password := strings.TrimSpace(scanner.Text())
	if password == "" {
		return "", fmt.Errorf("password must not be empty")
	}
	return password, nil
}

// createOwner creates the org (if it doesn't already exist), the user (if
// they don't already exist) and an owner membership between them. It is
// idempotent on email plus slug: rerunning it does not error, does not
// duplicate rows, and does not change an existing user's password.
func createOwner(ctx context.Context, pool *pgxpool.Pool, slug, name, email, password string) error {
	org, err := core.OrgBySlug(ctx, pool, slug)
	if errors.Is(err, core.ErrNotFound) {
		org, err = core.CreateOrg(ctx, pool, slug, name)
	}
	if err != nil {
		return fmt.Errorf("resolving org %q: %w", slug, err)
	}

	var userID int64
	err = pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		hash, hashErr := auth.HashPassword(password)
		if hashErr != nil {
			return fmt.Errorf("hashing password: %w", hashErr)
		}
		err = pool.QueryRow(ctx,
			"INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id",
			email, email, hash,
		).Scan(&userID)
	}
	if err != nil {
		return fmt.Errorf("resolving user %q: %w", email, err)
	}

	if _, err := pool.Exec(ctx,
		"INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'owner') ON CONFLICT (user_id, org_id) DO NOTHING",
		userID, org.ID,
	); err != nil {
		return fmt.Errorf("creating owner membership: %w", err)
	}

	return nil
}

func serve() error {
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

	a := auth.New(pool, strings.HasPrefix(cfg.BaseURL, "https://"), auth.WithTrustedProxy(cfg.TrustedProxy))
	deps := httpapi.Deps{Pool: pool, Auth: a, Cfg: cfg}
	billingSvc := httpapi.DefaultBilling(deps)
	deps.Billing = billingSvc
	handler := httpapi.New(deps)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	var schedWG sync.WaitGroup
	if cfg.SchedulerEnabled {
		schedWG.Add(1)
		go func() {
			defer schedWG.Done()
			scheduler.Run(schedCtx, scheduler.Jobs{
				Pool:            pool,
				Billing:         billingSvc,
				Location:        cfg.Location,
				PropertyEnabled: cfg.Modules["property"],
			}, time.Hour, time.Now)
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		schedCancel()
		schedWG.Wait()
		return err
	case <-ctx.Done():
	}

	// Stop the scheduler and wait for its goroutine to exit before
	// shutting down the HTTP server and (via the deferred pool.Close
	// above) closing the pool.
	schedCancel()
	schedWG.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
