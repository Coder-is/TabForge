// Package typescript embeds the portable data SDK for generated schema bundles.
package typescript

import "embed"

//go:embed data.ts schema.ts json.ts
var DataFiles embed.FS
