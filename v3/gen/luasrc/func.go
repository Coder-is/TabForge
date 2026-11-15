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
	if globals.Types.IsStructField(valueType) {
		return wrapStruct(globals, cell, valueType)
	}
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
	UsefulFunc["LuaRows"] = luaRows
}

// luaRows writes every data row of tab as Lua table constructors: the text
// the per-cell template range produced, without two reflective template
// calls per cell. Fields and the nogenfield_lua filter are resolved once.
func luaRows(globals *model.Globals, tab *model.DataTable) string {
	headers := globals.Types.AllFieldByName(tab.OriginalHeaderType)
	wrap := make([]bool, len(headers))
	for col, header := range headers {
		wrap[col] = header != nil && !globals.CanDoAction(model.ActionNoGennFieldLua, header)
	}
	var sb strings.Builder
	for _, row := range tab.DataRowIndex() {
		sb.WriteString("\n\t\t\t{ ")
		for col, header := range headers {
			if !wrap[col] {
				continue
			}
			sb.WriteString(header.FieldName)
			sb.WriteString(" = ")
			sb.WriteString(WrapValue(globals, tab.GetCell(row, col), header))
			sb.WriteString(", ")
		}
		sb.WriteString("}, ")
	}
	return sb.String()
}

// wrapStruct writes a struct field as a Lua table (fields in definition
// order, nogenfield_lua members left out) and a struct array as a table of
// them. A missing struct is all default values; a missing array is empty.
func wrapStruct(globals *model.Globals, cell *model.Cell, field *model.TypeDefine) string {
	if !field.IsArray() {
		var v *model.StructValue
		if cell != nil {
			v = cell.Struct
		}
		return structTable(globals, field.FieldType, v)
	}
	var sb strings.Builder
	sb.WriteString("{")
	if cell != nil {
		for i, v := range cell.Structs {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(structTable(globals, field.FieldType, v))
		}
	}
	sb.WriteString("}")
	return sb.String()
}

func structTable(globals *model.Globals, typeName string, v *model.StructValue) string {
	var sb strings.Builder
	sb.WriteString("{ ")
	for i, member := range globals.Types.AllFieldByName(typeName) {
		if globals.CanDoAction(model.ActionNoGennFieldLua, member) {
			continue
		}
		var cell *model.Cell
		if v != nil && i < len(v.Cells) {
			cell = v.Cells[i]
		}
		sb.WriteString(member.FieldName)
		sb.WriteString(" = ")
		sb.WriteString(WrapValue(globals, cell, member))
		sb.WriteString(", ")
	}
	sb.WriteString("}")
	return sb.String()
}
