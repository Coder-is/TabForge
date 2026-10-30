package compiler

import (
	"github.com/Coder-is/TabForge/v3/checker"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func transposeKVtoData(symbols *model.TypeTable, kvtab *model.DataTable) (ret *model.DataTable) {

	ret = model.NewDataTable()
	ret.HeaderType = kvtab.HeaderType
	ret.OriginalHeaderType = kvtab.HeaderType
	ret.FileName = kvtab.FileName
	ret.SheetName = kvtab.SheetName

	// 添加表头
	ret.AddRow()

	// 添加数据行
	ret.AddRow()

	// 遍历KV表的每一行
	for row := 1; row < len(kvtab.Rows); row++ {

		// Merged rows only hold cells up to the last source column, so absent
		// required columns read as empty cells instead of nil.
		cellByName := func(name string) *model.Cell {
			return kvtab.MustGetCell(row, kvtab.HeaderByName(name).Cell.Col)
		}

		fieldName := cellByName("字段名")
		fieldType := cellByName("字段类型")
		name := kvtab.GetValueByName(row, "标识名")

		arraySplitter := kvtab.GetValueByName(row, "数组切割")

		tags := kvtab.GetValueByName(row, "标记")

		var tf model.TypeDefine
		tf.Kind = model.TypeUsage_HeaderStruct
		tf.ObjectType = kvtab.HeaderType

		if name != nil {
			tf.Name = name.Value
		}

		if !model.PrimitiveExists(fieldType.Value) && !symbols.ObjectExists(fieldType.Value) { // 对象检查
			report.ReportError("UnknownFieldType", fieldType.Value, fieldType)
		}

		tf.FieldName = fieldName.Value
		tf.FieldType = fieldType.Value
		if arraySplitter != nil {
			tf.ArraySplitter = arraySplitter.Value
		}

		// 将KV表的Tags转换过去
		// 标记 is an array column, so merging already split it into ValueList.
		if tags != nil && len(tags.ValueList) > 0 {
			tf.Tags = tags.ValueList
		}

		checker.CheckFieldDefinition(symbols, &tf, kvtab, row, "DuplicateKVField")

		symbols.AddField(&tf, kvtab, row)

		// 输出表的表头原始数据
		headerCell := ret.AddCell(0)
		headerCell.Value = fieldName.Value

		header := ret.MustGetHeader(headerCell.Col)
		header.Cell.Value = fieldName.Value
		header.TypeInfo = &tf

		inputValueCell := cellByName("值")

		outputValueCell := ret.AddCell(1)
		outputValueCell.CopyFrom(inputValueCell)

	}

	return
}
