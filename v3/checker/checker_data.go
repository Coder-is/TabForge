package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strconv"
)

// 检查数据与定义类型是否匹配; 结构字段逐个检查成员单元格
func checkDataType(globals *model.Globals) {

	for _, tab := range globals.Datas.AllTables() {

		// 遍历输入数据的每一列
		for _, header := range tab.Headers {

			// 输入的列头，为空表示改列被注释
			if header.TypeInfo == nil {
				continue
			}

			for row := 1; row < len(tab.Rows); row++ {

				// 这行被注释，无效行(GetCell为nil)时VisitLeaves不调用
				model.VisitLeaves(globals.Types, header.TypeInfo, tab.GetCell(row, header.Cell.Col), checkCellType)
			}
		}
	}
}

func checkCellType(field *model.TypeDefine, cell *model.Cell) {
	if field.IsArray() {
		for _, value := range cell.ValueList {
			if checkSingleValue(field, value) != nil {
				report.ReportError("DataMissMatchTypeDefine", field.FieldType, cell)
			}
		}
	} else if cell.Value != "" {
		if checkSingleValue(field, cell.Value) != nil {
			report.ReportError("DataMissMatchTypeDefine", field.FieldType, cell)
		}
	}
}


func checkSingleValue(field *model.TypeDefine, value string) error {
	kind := model.LanguagePrimitive(field.FieldType, "go")
	// Empty numeric cells use defaults, except empty float32 array elements,
	// which the existing table format rejects.
	if value == "" && kind != "float32" {
		return nil
	}
	switch kind {
	case "int16", "int32", "int64":
		bits, _ := strconv.Atoi(kind[3:])
		_, err := strconv.ParseInt(value, 10, bits)
		return err
	case "uint16", "uint32", "uint64":
		bits, _ := strconv.Atoi(kind[4:])
		_, err := strconv.ParseUint(value, 10, bits)
		return err
	case "float32", "float64":
		bits, _ := strconv.Atoi(kind[5:])
		_, err := strconv.ParseFloat(value, bits)
		return err
	case "bool":
		_, err := model.ParseBool(value)
		return err
	default:
		return nil
	}
}
