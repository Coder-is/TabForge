package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strconv"
)

// 检查数据与定义类型是否匹配
func checkDataType(globals *model.Globals) {

	for _, tab := range globals.Datas.AllTables() {

		// 遍历输入数据的每一列
		for _, header := range tab.Headers {

			// 输入的列头，为空表示改列被注释
			if header.TypeInfo == nil {
				continue
			}

			for row := 1; row < len(tab.Rows); row++ {

				inputCell := tab.GetCell(row, header.Cell.Col)

				// 这行被注释，无效行
				if inputCell == nil {
					continue
				}

				if header.TypeInfo.IsArray() {
					for _, value := range inputCell.ValueList {

						err := checkSingleValue(header, value)
						if err != nil {
							report.ReportError("DataMissMatchTypeDefine", header.TypeInfo.FieldType, inputCell)
						}
					}
				} else if inputCell.Value != "" {
					err := checkSingleValue(header, inputCell.Value)
					if err != nil {
						report.ReportError("DataMissMatchTypeDefine", header.TypeInfo.FieldType, inputCell)
					}
				}

			}
		}
	}
}

func checkSingleValue(header *model.HeaderField, value string) error {
	kind := model.LanguagePrimitive(header.TypeInfo.FieldType, "go")
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
