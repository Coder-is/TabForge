package compiler

import (
	"fmt"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strings"
)

func readOneRow(sheet helper.TableSheet, tab *model.DataTable, row int) bool {

	for _, header := range tab.Headers {

		if header.TypeInfo == nil {
			continue
		}

		// 浮点数用库取时，需要特殊处理
		isFloat := model.LanguagePrimitive(header.TypeInfo.FieldType, "go") == "float32"

		value := sheet.GetValue(row, header.Cell.Col, &helper.ValueOption{ValueAsFloat: isFloat})

		// 首列带#时，本行忽略
		if header.Cell.Col == 0 && strings.HasPrefix(value, "#") {
			return false
		}

		cell := tab.MustGetCell(row, header.Cell.Col)
		cell.Value = value
	}

	return true
}

func LoadDataTable(filegetter helper.FileGetter, fileName, headerType, resolveHeaderType string, typeTab *model.TypeTable) (ret []*model.DataTable, err error) {
	file, err := filegetter.GetFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fileName, err)
	}

	for _, sheet := range file.Sheets() {

		tab := model.NewDataTable()
		tab.HeaderType = headerType
		tab.FileName = fileName
		tab.SheetName = sheet.Name()

		ret = append(ret, tab)

		maxCol, emptyCols := LoadHeader(sheet, tab, resolveHeaderType, typeTab)

		// 遍历所有数据行
		for row := 0; row < sheet.MaxRow(); row++ {

			// 完整空行跳过, 继续读取后续行
			if sheet.IsRowEmpty(row, maxCol) {
				continue
			}

			// 读取每一行, 注释行不检查
			if !readOneRow(sheet, tab, row) {
				continue
			}

			// 列头为空的列不能有数据, 避免数据被静默丢弃
			for _, col := range emptyCols {
				if value := sheet.GetValue(row, col, nil); value != "" {
					report.ReportError("DataUnderEmptyHeader", &model.Cell{Value: value, Row: row, Col: col, Table: tab})
				}
			}
		}

	}

	return
}
