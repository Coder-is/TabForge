// Package typescript embeds the portable data SDK for generated schema bundles.
package typescript

import "embed"

//go:embed data.ts schema.ts
var DataFiles embed.FS
