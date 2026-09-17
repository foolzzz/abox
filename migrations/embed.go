package migrations

import "embed"

// Files contains the forward-only PostgreSQL schema migrations.
//
//go:embed *.sql
var Files embed.FS
