// Package migrations embeds the SQL files applied by internal/db.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
