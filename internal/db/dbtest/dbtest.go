// Package dbtest gives each test its own PostgreSQL schema, so tests run in
// parallel against one server without seeing each other's rows.
package dbtest

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"aichatdeck/internal/idgen"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// URL returns a database URL whose search_path is a fresh, empty schema that
// is dropped when the test ends. Reopening the same URL reaches the same
// data, which restart tests rely on. Tests skip when TEST_DATABASE_URL is
// unset (`make test` and CI set it).
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test` or start Postgres with docker compose")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "t_" + strings.NewReplacer("-", "_").Replace(strings.ToLower(idgen.New("s")))
	if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		admin, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer admin.Close()
		_, _ = admin.Exec(`DROP SCHEMA ` + name + ` CASCADE`)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}
