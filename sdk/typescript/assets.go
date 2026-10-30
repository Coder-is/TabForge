// Package typescript embeds the portable data SDK for generated schema bundles.
package typescript

import (
	"bytes"
	"embed"
)

//go:embed data.ts schema.ts json.ts
var DataFiles embed.FS

// ReadDataFile returns an embedded runtime file with LF line endings, so
// generated bundles do not depend on the checkout's core.autocrlf setting.
func ReadDataFile(name string) ([]byte, error) {
	data, err := DataFiles.ReadFile(name)
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), err
}
