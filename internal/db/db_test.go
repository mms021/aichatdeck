package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aichatdeck/internal/schema"
)

func TestOpenMigratesAttemptColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema.SQL, ",\n  claim_id TEXT,\n  submitted_message_id TEXT", "", 1)
	if oldSchema == schema.SQL {
		t.Fatal("legacy schema fixture did not remove columns")
	}
	if _, err := legacy.Exec(oldSchema); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		migrated, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := migrated.Query(`SELECT claim_id, submitted_message_id FROM tasks`)
		if err != nil {
			migrated.Close()
			t.Fatal(err)
		}
		rows.Close()
		migrated.Close()
	}
}

// instance_id identifies the database, not the process: it survives restarts
// and file copies, and a fresh database gets a new one.
func TestInstanceIDIsStablePerDatabase(t *testing.T) {
	dir := t.TempDir()
	read := func(path string) string {
		t.Helper()
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		id, err := InstanceID(d)
		if err != nil || !strings.HasPrefix(id, "instance-") {
			t.Fatalf("instance id %q: %v", id, err)
		}
		return id
	}
	first := filepath.Join(dir, "a.db")
	id := read(first)
	if again := read(first); again != id {
		t.Fatalf("restart changed instance id: %s -> %s", id, again)
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved.db")
	if err := os.WriteFile(moved, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(moved); got != id {
		t.Fatalf("copied database changed instance id: %s -> %s", id, got)
	}
	if other := read(filepath.Join(dir, "b.db")); other == id {
		t.Fatal("different database reused instance id")
	}
}
