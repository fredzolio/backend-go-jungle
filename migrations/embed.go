// Package migrations embeds the versioned SQL migrations (goose format) so the
// binary can apply and revert them without files on disk.
package migrations

import "embed"

// FS holds every migration file.
//
//go:embed *.sql
var FS embed.FS
