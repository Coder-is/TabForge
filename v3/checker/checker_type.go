package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"go/token"
	"strconv"
	"unicode"
)

func CheckType(typeTab *model.TypeTable) {

	checkObjectTypes(typeTab)

	checkFieldNames(typeTab)

	checkFieldAliases(typeTab)

	checkFieldTypes(typeTab)

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

// 只支持原始类型和枚举, 类型表中未被数据表引用的字段也需检查
func checkFieldTypes(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		field := td.Define
		if field.IsBuiltin || field.Kind != model.TypeUsage_HeaderStruct {
			continue
		}
		if !model.PrimitiveExists(field.FieldType) && !typeTab.IsEnumKind(field.FieldType) {
			report.ReportError("UnknownFieldType", field.FieldType, typeCell(td, "字段类型"))
		}
	}
}

// 类型名会用于生成代码和输出文件路径, 只允许标识符
func checkObjectTypes(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		if !td.Define.IsBuiltin && !model.IsPlainIdentifier(td.Define.ObjectType) {
			report.ReportError("InvalidTypeName", td.Define.ObjectType, typeCell(td, "对象类型"))
		}
	}
}

// 标识名会写入生成代码的注释和Go标签, 换行会把后续内容变成代码
func checkFieldAliases(typeTab *model.TypeTable) {
	for _, td := range typeTab.Raw() {
		if !td.Define.IsBuiltin && !validFieldAlias(td.Define.Name) {
			report.ReportError("InvalidFieldAlias", typeCell(td, "标识名"))
		}
	}
}

// validFieldAlias 拒绝控制字符(含CR/LF)、Unicode行分隔符、双引号和反引号
func validFieldAlias(name string) bool {
	for _, r := range name {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == '"' || r == '`' {
			return false
		}
	}
	return true
}

// KV表没有对象类型列, 找不到单元格时返回定义描述
func typeCell(td *model.TypeData, column string) interface{} {
	if td.Tab != nil {
		if cell := td.Tab.GetValueByName(td.Row, column); cell != nil {
			return cell
		}
	}
	return td.Define.ObjectType + "." + td.Define.FieldName
}
