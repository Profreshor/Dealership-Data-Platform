package migrations

import "embed"

// FS contains the platform and client SQL migration chains.
//
//go:embed ddp/*.sql app/*.sql
var FS embed.FS
