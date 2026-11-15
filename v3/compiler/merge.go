package compiler

import (
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strings"
)

func createOutputTable(symbols *model.TypeTable, inputTab *model.DataTable) *model.DataTable {
	outputTab := model.NewDataTable()
	outputTab.HeaderType = inputTab.HeaderType
	outputTab.OriginalHeaderType = inputTab.OriginalHeaderType

	// Merge后的表报错没显示来源文件名, 添加这里
	outputTab.FileName = inputTab.FileName
	outputTab.SheetName = inputTab.SheetName

	// 原始表头类型为解析
	headerFields := symbols.AllFieldByName(inputTab.OriginalHeaderType)

	if headerFields == nil {
		report.ReportError("HeaderTypeNotFound", inputTab.OriginalHeaderType)
	}

	// 将完整的表头添加到输出表的表头中
	for col, tf := range headerFields {

		outputHeader := outputTab.MustGetHeader(col)
		outputHeader.Cell.Value = tf.Name
		outputHeader.Cell.Col = col
		outputHeader.Cell.Row = 0
		outputHeader.TypeInfo = tf

		headerCell := outputTab.MustGetCell(0, col)
		headerCell.Value = tf.Name
	}

	return outputTab
}

// 将不同文件/Sheet/KV转换的表，按照表头类型合并数据输出
func MergeData(inputList, outputList *model.DataTableList, symbols *model.TypeTable) {

	for _, inputTab := range inputList.AllTables() {

		// 表头类型为分类
		outputTab := outputList.GetDataTable(inputTab.HeaderType)

		// 为输入表头创建唯一的表数据
		if outputTab == nil {
			outputTab = createOutputTable(symbols, inputTab)

			outputList.AddDataTable(outputTab)
		}

		// 每张输入表只解析一次: 每列对应的输出列, 以及同名重复列的个数
		outputHeaders := make([]*model.HeaderField, len(inputTab.Headers))
		arrayCounts := make([]int, len(inputTab.Headers))
		widths := map[string]int{}
		for _, header := range inputTab.Headers {
			if header.TypeInfo != nil {
				widths[header.Key()]++
			}
		}
		for i, header := range inputTab.Headers {
			if header.TypeInfo != nil {
				outputHeaders[i] = outputTab.HeaderByName(header.Top().FieldName)
				arrayCounts[i] = widths[header.Key()]
			}
		}

		// 遍历输入表的每一行
		for row := 1; row < len(inputTab.Rows); row++ {

			// Commented rows have no cells and must not reach the output.
			if inputTab.GetCell(row, 0) == nil {
				continue
			}
			outputRow := outputTab.AddRow()

			// 遍历输入数据的每一列
			for i, inputHeader := range inputTab.Headers {

				// 输入的列头

				if inputHeader.TypeInfo == nil {
					continue
				}

				inputCell := inputTab.GetCell(row, inputHeader.Cell.Col)

				// 这行被注释，无效行
				if inputCell == nil {
					break
				}

				// 用输入的表头名在输出的表头中找
				outputHeader := outputHeaders[i]

				if outputHeader == nil {
					panic("输入的列头名在输出表头中找不到:" + inputHeader.TypeInfo.FieldName)
				}

				// 取输出表的最后的一行和对应表头的列单元格
				outputCell := outputTab.MustGetCell(outputRow, outputHeader.Cell.Col)
				if len(inputHeader.Path) > 0 {
					// 结构成员列: 写入结构(或结构数组元素)里对应的成员单元格
					outputCell = model.LeafCell(symbols, outputCell, inputHeader.Path)
				}

				// 切记, V3不支持, 也不能支持默认值
				// 默认值会导致多表中, 有默认值列才有默认值, 导出数据在不同功能表间会出现歧义
				// 建议在逻辑层自行处理

				if inputHeader.TypeInfo.IsArray() {

					combineRepeatedCell(outputCell, inputCell, inputHeader, arrayCounts[i])

				} else {
					outputCell.CopyFrom(inputCell)
				}
			}
			// 所有列都为空的结构数组元素不输出
			for _, header := range outputTab.Headers {
				if symbols.IsStructField(header.TypeInfo) {
					if cell := outputTab.GetCell(outputRow, header.Cell.Col); cell != nil {
						cell.DropEmptyElements()
					}
				}
			}
		}

	}
}

func combineRepeatedCell(outputCell, inputCell *model.Cell, header *model.HeaderField, columns int) {
	// Repeated physical columns each contribute one element, including empty ones.
	if columns != 1 {
		outputCell.ValueList = append(outputCell.ValueList, inputCell.Value)
		return
	}
	if inputCell.Value != "" {
		outputCell.ValueList = append(outputCell.ValueList, strings.Split(inputCell.Value, header.TypeInfo.ArraySplitter)...)
	}
}
