// Package dbtest gives each test its own PostgreSQL schema, so tests run in
// parallel against one server without seeing each other's rows.
package dbtest

import (
	"os"
	"testing"

	"aichatdeck/internal/db"
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
	u, drop, err := db.ScratchSchema(base, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	return u
}
