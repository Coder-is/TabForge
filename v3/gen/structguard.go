package gen

import (
	"fmt"

	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

// RejectStructFields fails exporters that do not write nested structs yet
// (see doc/nested-struct-design.md) instead of producing data without them.
func RejectStructFields(globals *model.Globals, format string) error {
	for _, field := range globals.Types.AllFields() {
		if !field.IsBuiltin && field.Kind == model.TypeUsage_HeaderStruct && globals.Types.IsStructField(field) {
			return fmt.Errorf("%s: StructFieldNotSupported %s: %s.%s", format, report.ErrorByID["StructFieldNotSupported"].CHS, field.ObjectType, field.FieldName)
		}
	}
	return nil
}
