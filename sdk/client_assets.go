// Package sdk embeds the portable data runtimes used by editor imports.
package sdk

import "embed"

//go:embed unity/Runtime/DataSchema.cs unity/Runtime/JsonSyntax.cs unity/Runtime/WireValidator.cs godot/data_schema.gd godot/wire_validator.gd typescript/data.ts typescript/schema.ts
var ClientAssets embed.FS
