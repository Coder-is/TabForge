package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

// CheckFieldDefinition runs before insertion: ambiguous aliases must not overwrite
// the lookup used to resolve data headers and subsequent definitions.
func CheckFieldDefinition(types *model.TypeTable, field *model.TypeDefine, tab *model.DataTable, row int, duplicateID string) {
	cell := tab.GetValueByName(row, "字段名")
	if cell == nil {
		report.ReportError("InvalidTypeTable", field.ObjectType, field.FieldName, tab.FileName)
	}
	location := cell
	previous := types.ObjectDefinition(field.ObjectType)
	if model.PrimitiveExists(field.ObjectType) || previous != nil && (previous.IsBuiltin || previous.Kind != field.Kind) {
		report.ReportError("ConflictingTypeName", location, field.ObjectType)
	}
	if previous := types.FieldByName(field.ObjectType, field.FieldName); previous != nil {
		if previous.FieldName != field.FieldName {
			report.ReportError("AmbiguousTypeFieldName", location, field.ObjectType, field.FieldName)
		}
		if duplicateID == "DuplicateKVField" {
			report.ReportError(duplicateID, location)
		}
		report.ReportError(duplicateID, location, field.ObjectType, field.FieldName)
	}
	if field.Name != "" && types.FieldByName(field.ObjectType, field.Name) != nil {
		report.ReportError("AmbiguousTypeFieldName", tab.GetValueByName(row, "标识名"), field.ObjectType, field.Name)
	}
}
