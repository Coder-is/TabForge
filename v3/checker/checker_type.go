package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"go/token"
	"strconv"
)

func CheckType(typeTab *model.TypeTable) {

	checkFieldNames(typeTab)

	checkEmptyEnumValues(typeTab)

	checkEnumValueFormat(typeTab)

	checkDuplicateEnumValues(typeTab)

}

func checkFieldNames(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {

		if !token.IsIdentifier(td.Define.FieldName) {
			cell := td.Tab.GetValueByName(td.Row, "字段名")
			report.ReportError("InvalidFieldName", cell)
		}
	}
}

func checkEmptyEnumValues(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		if td.Define.Kind == model.TypeUsage_Enum && td.Define.Value == "" {
			cell := td.Tab.GetValueByName(td.Row, "值")
			report.ReportError("EnumValueEmpty", cell)
		}
	}
}

// Generators emit enum values verbatim; leading zeros would be octal in Go/Java.
func checkEnumValueFormat(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		if td.Define.Kind != model.TypeUsage_Enum || td.Define.Value == "" {
			continue
		}
		if v, err := strconv.ParseInt(td.Define.Value, 10, 32); err != nil || strconv.FormatInt(v, 10) != td.Define.Value {
			cell := td.Tab.GetValueByName(td.Row, "值")
			report.ReportError("InvalidEnumValue", cell)
		}
	}
}

func checkDuplicateEnumValues(typeTab *model.TypeTable) {

	type NameValuePair struct {
		Name  string
		Value string
	}

	seen := make(map[NameValuePair]bool)

	for _, td := range typeTab.Raw() {

		if td.Define.IsBuiltin || td.Define.Kind != model.TypeUsage_Enum {
			continue
		}

		key := NameValuePair{td.Define.ObjectType, td.Define.Value}

		if seen[key] {

			cell := td.Tab.GetValueByName(td.Row, "值")

			report.ReportError("DuplicateEnumValue", cell)
		}

		seen[key] = true
	}
}
