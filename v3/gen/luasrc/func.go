package luasrc

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
	"math"
	"strconv"
	"strings"
	"text/template"
)

var UsefulFunc = template.FuncMap{}

func WrapValue(globals *model.Globals, cell *model.Cell, valueType *model.TypeDefine) string {
	if valueType.IsArray() {

		var sb strings.Builder
		sb.WriteString("{")

		if cell != nil {
			for index, elementValue := range cell.ValueList {
				if index > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(wrapSingleValue(globals, valueType, elementValue))
			}
		}

		sb.WriteString("}")

		return sb.String()

	} else {

		var value string
		if cell != nil {
			value = cell.Value
		}

		return wrapSingleValue(globals, valueType, value)
	}
}

func wrapSingleValue(globals *model.Globals, valueType *model.TypeDefine, value string) string {
	text := gen.WrapSingleValue(globals, valueType, value)
	switch model.LanguagePrimitive(valueType.FieldType, "go") {
	case "int16", "int32", "int64", "uint16", "uint32", "uint64", "float32", "float64":
		// The checker accepts "+1", "NaN" and "Inf", but Lua has no unary plus or
		// NaN/Inf literals (they would be syntax errors or nil globals).
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			switch {
			case math.IsNaN(f):
				return "(0/0)"
			case math.IsInf(f, 1):
				return "math.huge"
			case math.IsInf(f, -1):
				return "-math.huge"
			}
		}
		return strings.TrimPrefix(text, "+")
	}
	return text
}

func init() {
	UsefulFunc["WrapTabValue"] = func(globals *model.Globals, dataTable *model.DataTable, allHeaders []*model.TypeDefine, row, col int) (ret string) {
		// 找到完整的表头（按完整表头遍历）
		header := allHeaders[col]

		
		if header == nil {
			return ""
		}

		// 在单元格找到值
		valueCell := dataTable.GetCell(row, col)

		if valueCell != nil {

			return WrapValue(globals, valueCell, header)
		} else {
			// 这个表中没有这列数据
			return WrapValue(globals, nil, header) 
		}
	}

	UsefulFunc["IsWrapFieldName"] = func(globals *model.Globals, dataTable *model.DataTable, allHeaders []*model.TypeDefine, row, col int) (ret bool) {
		// 找到完整的表头（按完整表头遍历）
		header := allHeaders[col]

		
		if header == nil {
			return false
		}

		if globals.CanDoAction(model.ActionNoGennFieldLua, header) {
			return false
		}

		return true
	}


}
