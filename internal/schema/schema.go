// Package schema embeds the PostgreSQL DDL. It is idempotent (IF NOT EXISTS /
// OR REPLACE) and re-applied on every start; there is no migration framework
// yet because no deployed database predates this schema.
package schema

import _ "embed"

//go:embed schema.sql
var SQL string
