// Package db embeds the SQL migrations so the API can apply them on startup.
package db

import "embed"

// Migrations holds every file in db/migrations, applied in lexical order.
//
//go:embed migrations/*.sql
var Migrations embed.FS
