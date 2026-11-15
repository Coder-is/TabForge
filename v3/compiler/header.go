package compiler

import (
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"strconv"
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

		// 结构字段要展开成成员列; 列中只能是原始类型和枚举
		if symbols.IsStructField(header.TypeInfo) {
			report.ReportError("StructFieldNeedsMembers", header.Cell)
		}
		if !model.PrimitiveExists(header.TypeInfo.FieldType) && !symbols.IsEnumKind(header.TypeInfo.FieldType) {

			report.ReportError("UnknownFieldType", header.TypeInfo.FieldType, header.Cell)
		}
	}

}

// 列头可使用标识名或字段名, 两种写法指向同一字段时也视为重复; 结构成员按完整路径比较
func headerFieldExists(offset int, key string, headers []*model.HeaderField) bool {

	for i := offset; i < len(headers); i++ {
		if headers[i].TypeInfo != nil && headers[i].Key() == key {
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

		// 先按完整列头查找, 已有的标识名中可能含有'.'或'['
		tf := typeTab.FieldByName(tableObjectType, header.Cell.Value)
		if tf == nil && strings.ContainsAny(header.Cell.Value, ".[") {
			header.Path = resolveHeaderPath(header, tableObjectType, typeTab)
			tf = header.Path[len(header.Path)-1].Field
		}
		if tf == nil {
			report.ReportError("HeaderFieldNotDefined", header.Cell, tableObjectType)
		}

		// 解析好的类型
		header.TypeInfo = tf
	}

	for index, header := range tab.Headers {
		if header.TypeInfo != nil && !header.TypeInfo.IsArray() && headerFieldExists(index+1, header.Key(), tab.Headers) {
			report.ReportError("DuplicateHeaderField", header.Cell)
		}
	}

}

// resolveHeaderPath resolves a member column such as 奖励.物品 or
// 消耗[2].数量: every step but the last must be a struct field, and array
// struct fields need an element number.
func resolveHeaderPath(header *model.HeaderField, tableObjectType string, typeTab *model.TypeTable) []model.PathStep {
	segments := strings.Split(header.Cell.Value, ".")
	objectType := tableObjectType
	path := make([]model.PathStep, 0, len(segments))
	for i, segment := range segments {
		name, index, hasIndex, ok := splitElementNumber(segment)
		if !ok {
			report.ReportError("HeaderPathInvalid", header.Cell)
		}
		field := typeTab.FieldByName(objectType, name)
		if field == nil {
			report.ReportError("HeaderFieldNotDefined", header.Cell, objectType)
		}
		last := i == len(segments)-1
		isStruct := typeTab.IsStructField(field)
		switch {
		case hasIndex && !(isStruct && field.IsArray()):
			report.ReportError("HeaderPathInvalid", header.Cell) // [n] only selects an element of a struct array
		case !last && !isStruct:
			report.ReportError("HeaderPathInvalid", header.Cell) // . only opens a struct
		case !last && field.IsArray() && !hasIndex:
			report.ReportError("HeaderPathInvalid", header.Cell) // struct array members need [n]
		}
		path = append(path, model.PathStep{Field: field, Index: index, HasIndex: hasIndex})
		objectType = field.FieldType
	}
	return path
}

// splitElementNumber splits name[n] (n a non-negative integer); a segment
// without brackets has no element number.
func splitElementNumber(segment string) (name string, index int, hasIndex, ok bool) {
	open := strings.IndexByte(segment, '[')
	if open < 0 {
		return segment, 0, false, segment != "" && !strings.Contains(segment, "]")
	}
	if !strings.HasSuffix(segment, "]") || open == 0 {
		return "", 0, false, false
	}
	n, err := strconv.Atoi(segment[open+1 : len(segment)-1])
	if err != nil || n < 0 || strconv.Itoa(n) != segment[open+1:len(segment)-1] {
		return "", 0, false, false
	}
	return segment[:open], n, true, true
}
