package checker

import (
	"fmt"
	"math"
	"strconv"

	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func checkRepeat(globals *model.Globals) {
	for _, tab := range globals.Datas.AllTables() {
		for _, header := range tab.Headers {
			field := header.TypeInfo
			if field == nil || !field.MakeIndex {
				continue
			}
			if field.IsArray() {
				report.ReportError("InvalidIndexType", field.FieldName, field.FieldType, "array")
			}
			seen := make(map[string]bool)
			for row := 1; row < len(tab.Rows); row++ {
				cell := tab.GetCell(row, header.Cell.Col)
				// Empty indices intentionally remain optional, as in the V3 split-table examples.
				if cell == nil || cell.Value == "" {
					continue
				}
				text := cell.Value
				key, err := indexValue(globals.Types, field, text)
				if err != nil {
					report.ReportError("InvalidIndexType", cell, field.FieldType, err)
				}
				if seen[key] {
					report.ReportError("DuplicateValueInMakingIndex", cell)
				}
				seen[key] = true
			}
		}
	}
}

// Compare typed values rather than spelling; callers skip optional empty indices.
func indexValue(types *model.TypeTable, field *model.TypeDefine, text string) (string, error) {
	if types.IsEnumKind(field.FieldType) {
		text = types.ResolveEnumValue(field.FieldType, text)
		value, err := strconv.ParseInt(text, 10, 32)
		return strconv.FormatInt(value, 10), err
	}
	kind := model.LanguagePrimitive(field.FieldType, "go")
	if kind == "string" {
		return text, nil
	}
	if kind == "bool" {
		value, err := model.ParseBool(text)
		return strconv.FormatBool(value), err
	}
	if text == "" {
		text = "0"
	}
	switch kind {
	case "int16", "int32", "int64":
		bits, _ := strconv.Atoi(kind[3:])
		value, err := strconv.ParseInt(text, 10, bits)
		return strconv.FormatInt(value, 10), err
	case "uint16", "uint32", "uint64":
		bits, _ := strconv.Atoi(kind[4:])
		value, err := strconv.ParseUint(text, 10, bits)
		return strconv.FormatUint(value, 10), err
	case "float32", "float64":
		bits, _ := strconv.Atoi(kind[5:])
		value, err := strconv.ParseFloat(text, bits)
		if err != nil {
			return "", err
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", fmt.Errorf("index must be finite")
		}
		if value == 0 {
			value = 0 // Canonicalize negative zero.
		}
		return strconv.FormatFloat(value, 'g', -1, bits), nil
	default:
		return "", fmt.Errorf("index requires a scalar primitive or enum")
	}
}
