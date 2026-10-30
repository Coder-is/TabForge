// Package sdk embeds the portable data runtimes used by editor imports.
package sdk

import (
	"bytes"
	"embed"
)

//go:embed unity/Runtime/DataSchema.cs unity/Runtime/JsonSyntax.cs unity/Runtime/WireValidator.cs godot/data_schema.gd godot/wire_validator.gd typescript/data.ts typescript/schema.ts typescript/json.ts
var ClientAssets embed.FS

// ReadClientAsset returns an embedded runtime file with LF line endings, so
// imported engine assets do not depend on the checkout's core.autocrlf setting.
func ReadClientAsset(name string) ([]byte, error) {
	data, err := ClientAssets.ReadFile(name)
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), err
}
