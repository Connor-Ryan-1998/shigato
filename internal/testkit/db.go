//go:build integration

package testkit

import (
	"regexp"
	"strings"
)

// identifier matches a plain, unquoted SQL table name. Table names are
// interpolated into SQL, so anything else is rejected.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Snapshot returns each table's rows as a jsonb array, ordered by the
// table's first column, one "table: [...]" line per table. Comparing two
// snapshots shows whether anything in those tables changed. An unknown or
// invalid table name fails the test, naming the table.
func (e *Env) Snapshot(tables ...string) string {
	e.T.Helper()
	if len(tables) == 0 {
		e.T.Fatalf("Snapshot needs at least one table")
		return ""
	}
	var b strings.Builder
	for _, table := range tables {
		if !identifier.MatchString(table) {
			e.T.Fatalf("Snapshot: invalid table name %q", table)
			return ""
		}
		var rows string
		err := e.Pool.QueryRow(e.Ctx,
			"SELECT coalesce(jsonb_agg(to_jsonb(t)), '[]'::jsonb)::text FROM (SELECT * FROM "+table+" ORDER BY 1) t",
		).Scan(&rows)
		if err != nil {
			e.T.Fatalf("Snapshot of table %q: %v", table, err)
			return ""
		}
		b.WriteString(table)
		b.WriteString(": ")
		b.WriteString(rows)
		b.WriteString("\n")
	}
	return b.String()
}

// Count returns the number of rows in table matching where, a SQL boolean
// expression using $1... placeholders for args. An empty where counts every
// row. An unknown or invalid table name fails the test, naming the table.
func (e *Env) Count(table string, where string, args ...any) int {
	e.T.Helper()
	if !identifier.MatchString(table) {
		e.T.Fatalf("Count: invalid table name %q", table)
		return 0
	}
	query := "SELECT count(*) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	var n int
	if err := e.Pool.QueryRow(e.Ctx, query, args...).Scan(&n); err != nil {
		e.T.Fatalf("Count of table %q where %q %v: %v", table, where, args, err)
		return 0
	}
	return n
}
