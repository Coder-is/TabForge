package pbsrc

import (
	"fmt"

	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
)

func Generate(globals *model.Globals) ([]byte, error) {
	if err := gen.ValidateNames(globals, "protobuf"); err != nil {
		return nil, err
	}
	// protoc rejects proto3 enums whose first value isn't zero.
	for _, name := range globals.Types.EnumNames() {
		if fields := globals.Types.AllFieldByName(name); len(fields) > 0 && fields[0].Value != "0" {
			return nil, fmt.Errorf("protobuf enum %s: first value %s must be 0 in proto3", name, fields[0].FieldName)
		}
	}
	return gen.Render("pbsrc", templateText, globals, UsefulFunc)
}
