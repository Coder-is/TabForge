package compiler

import (
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strings"
)

// 读取列头, 返回需要扫描的列数和列头为空的列
func LoadHeader(sheet helper.TableSheet, tab *model.DataTable, resolveTableType string, typeTab *model.TypeTable) (maxCol int, emptyCols []int) {

	maxCol = sheet.MaxColumn()

	for col := 0; col < maxCol; col++ {

		headerValue := sheet.GetValue(0, col, nil)

		// 空列头不终止读取, 但该列不能有数据, 由调用方检查
		if headerValue == "" {
			emptyCols = append(emptyCols, col)
			continue
		}

		// 列头带#时，本列忽略
		if strings.HasPrefix(headerValue, "#") {
			continue
		}

		header := tab.MustGetHeader(col)
		header.Cell.CopyFrom(&model.Cell{
			Value: headerValue,
			Col:   col,
			Row:   0,
			Table: tab,
		})

	}

	resolveHeaderFields(tab, resolveTableType, typeTab)

	checkHeaderTypes(tab, typeTab)

	return
}

func checkHeaderTypes(tab *model.DataTable, symbols *model.TypeTable) {

	for _, header := range tab.Headers {

		if header.TypeInfo == nil {
			continue
		}

		// 原始类型检查
		if !model.PrimitiveExists(header.TypeInfo.FieldType) &&
			!symbols.ObjectExists(header.TypeInfo.FieldType) { // 对象检查

			report.ReportError("UnknownFieldType", header.TypeInfo.FieldType, header.Cell)
		}
	}

}

// 列头可使用标识名或字段名, 两种写法指向同一字段时也视为重复
func headerFieldExists(offset int, tf *model.TypeDefine, headers []*model.HeaderField) bool {

	for i := offset; i < len(headers); i++ {
		if headers[i].TypeInfo == tf {
			return true
		}
	}

	return false
}

func resolveHeaderFields(tab *model.DataTable, tableObjectType string, typeTab *model.TypeTable) {

	tab.OriginalHeaderType = tableObjectType
	for _, header := range tab.Headers {

		if header.Cell.Value == "" {
			continue
		}

		tf := typeTab.FieldByName(tableObjectType, header.Cell.Value)
		if tf == nil {
			report.ReportError("HeaderFieldNotDefined", header.Cell, tableObjectType)
		}

		// 解析好的类型
		header.TypeInfo = tf
	}

	for index, header := range tab.Headers {
		if header.TypeInfo != nil && !header.TypeInfo.IsArray() && headerFieldExists(index+1, header.TypeInfo, tab.Headers) {
			report.ReportError("DuplicateHeaderField", header.Cell)
		}
	}

}
