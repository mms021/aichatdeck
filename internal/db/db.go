// Package db opens the PostgreSQL database (through pgx) and applies the
// embedded schema.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"

	"aichatdeck/internal/idgen"
	"aichatdeck/internal/schema"
)

// DB is *sql.DB with `?` placeholders rewritten to PostgreSQL's `$n`, so the
// stores can keep composing WHERE clauses from reusable fragments without
// tracking argument positions (the same approach as sqlx.Rebind).
type DB struct{ *sql.DB }

// Tx is the transaction counterpart of DB.
type Tx struct{ *sql.Tx }

// Open connects to databaseURL, applies schema.SQL and seeds the instance
// identity. The schema is idempotent, so every process start re-applies it.
func Open(databaseURL string) (*DB, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	sqlDB := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(_ context.Context, c *pgx.Conn) error {
		// Scan timestamptz as UTC rather than the process's local zone.
		c.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}))
	sqlDB.SetMaxOpenConns(20)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	// Serialize schema application across processes starting together.
	if _, err := sqlDB.Exec(`SELECT pg_advisory_lock(7461626) ; ` + schema.SQL + ` ; SELECT pg_advisory_unlock(7461626)`); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	d := &DB{sqlDB}
	if _, err := d.Exec(`INSERT INTO platform_instance(id, instance_id) VALUES (1, ?) ON CONFLICT DO NOTHING`, idgen.New("instance")); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("init instance id: %w", err)
	}
	return d, nil
}

// InstanceID returns the random identity stored in this database. Agents keep
// it in their state so a server backed by another database is reported as
// wrong_instance instead of a revoked credential.
func InstanceID(db *DB) (string, error) {
	var id string
	err := db.QueryRow(`SELECT instance_id FROM platform_instance WHERE id = 1`).Scan(&id)
	return id, err
}

// IsUniqueViolation reports a PostgreSQL unique_violation (23505).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Rebind turns each `?` outside a quoted literal into `$1`, `$2`, ...
func Rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	var quote byte
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (d *DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, Rebind(q), args...)
}

func (d *DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, Rebind(q), args...)
}

func (d *DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, Rebind(q), args...)
}

func (d *DB) Exec(q string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), q, args...)
}

func (d *DB) Query(q string, args ...any) (*sql.Rows, error) {
	return d.QueryContext(context.Background(), q, args...)
}

func (d *DB) QueryRow(q string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), q, args...)
}

func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{tx}, nil
}

func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, Rebind(q), args...)
}

func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, Rebind(q), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, Rebind(q), args...)
}

// ScratchSchema creates an empty schema in the database at baseURL and returns
// a URL whose search_path points at it, plus a func that drops it. Tests and
// evals use it to get an isolated database without creating one.
func ScratchSchema(baseURL, prefix string) (string, func(), error) {
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		return "", nil, err
	}
	defer admin.Close()
	name := prefix + "_" + strings.ReplaceAll(idgen.UUIDv4(), "-", "_")
	if _, err := admin.Exec(`CREATE SCHEMA ` + name); err != nil {
		return "", nil, fmt.Errorf("create scratch schema: %w", err)
	}
	drop := func() {
		if admin, err := sql.Open("pgx", baseURL); err == nil {
			_, _ = admin.Exec(`DROP SCHEMA ` + name + ` CASCADE`)
			admin.Close()
		}
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		drop()
		return "", nil, err
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String(), drop, nil
}
