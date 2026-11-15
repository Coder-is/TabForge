package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

// checkStructFields rejects struct fields that cannot be exported: indices
// on a struct field, and structs that contain themselves.
func checkStructFields(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		field := td.Define
		if field.IsBuiltin || !typeTab.IsStructField(field) {
			continue
		}
		if field.MakeIndex {
			report.ReportError("StructFieldIndex", typeCell(td, "索引"))
		}
		if containsType(typeTab, field.FieldType, field.ObjectType, map[string]bool{}) {
			report.ReportError("RecursiveStructField", field.FieldType, typeCell(td, "字段类型"))
		}
	}
}

// containsType reports whether structType holds target at any depth.
func containsType(typeTab *model.TypeTable, structType, target string, seen map[string]bool) bool {
	if structType == target {
		return true
	}
	if seen[structType] {
		return false
	}
	seen[structType] = true
	for _, member := range typeTab.AllFieldByName(structType) {
		if typeTab.IsStructField(member) && containsType(typeTab, member.FieldType, target, seen) {
			return true
		}
	}
	return false
}
