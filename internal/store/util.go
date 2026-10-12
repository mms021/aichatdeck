package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"aichatdeck/internal/db"
)

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func fromJSON[T any](s string) T {
	var v T
	if s != "" {
		_ = json.Unmarshal([]byte(s), &v)
	}
	return v
}

// dbNow is the current time at the precision PostgreSQL stores, so a value
// returned to the caller equals the one read back later.
func dbNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func orEmptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// nullIfEmpty maps an empty string to SQL NULL so nullable/unique-partial-index
// columns behave correctly (e.g. idempotency_key).
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool { return db.IsUniqueViolation(err) }

// encodeSeqCursor/decodeSeqCursor carry a monotonic insertion sequence — the
// message inbox pages on that rather than on a timestamp, which is not unique
// enough to order messages that arrive in the same second.
func encodeSeqCursor(seq int64) string {
	return base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(seq, 10)))
}

func decodeSeqCursor(cursor string) (int64, bool) {
	b, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}
	seq, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

func encodeCursor(ts time.Time, id string) string {
	return base64.URLEncoding.EncodeToString([]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(cursor string) (ts time.Time, id string, ok bool) {
	b, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", false
	}
	ts, err = time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, parts[1], true
}
