package checker

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

// 枚举值的解析是放在输出端处理的, 例如json中, 所以在这里提前检查; 结构字段逐个检查成员
func checkEnumValue(globals *model.Globals) {

	for _, tab := range globals.Datas.AllTables() {

		// 遍历输入数据的每一列
		for _, header := range tab.Headers {

			// 输入的列头，为空表示改列被注释
			if header.TypeInfo == nil {
				continue
			}

			for row := 1; row < len(tab.Rows); row++ {

				model.VisitLeaves(globals.Types, header.TypeInfo, tab.GetCell(row, header.Cell.Col), func(field *model.TypeDefine, cell *model.Cell) {
					if !globals.Types.IsEnumKind(field.FieldType) {
						return
					}
					if field.IsArray() {
						for _, v := range cell.ValueList {
							checkEnumFieldValue(globals, field, v, cell)
						}
					} else {
						checkEnumFieldValue(globals, field, cell.Value, cell)
					}
				})
			}
		}
	}
}

// 检查枚举值是否存在有效
func checkEnumFieldValue(globals *model.Globals, field *model.TypeDefine, value string, inputCell *model.Cell) {

	if value == "" {
		return
	}

	enumValue := globals.Types.GetEnumValue(field.FieldType, value)
	if enumValue == nil {
		report.ReportError("UnknownEnumValue", field.FieldType, inputCell)
	}

}
