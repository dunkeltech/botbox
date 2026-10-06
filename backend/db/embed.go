// Package migrations embeds SQL database migration files for goose.
package migrations

import "embed"

// Files holds all embedded SQL migration scripts under migrations/.
//
//go:embed migrations/*.sql
var Files embed.FS
