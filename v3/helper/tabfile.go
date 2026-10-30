package helper

type TableFile interface {
	Load(filename string) error

	// 保存到文件
	Save(filename string) error

	// 获取所有表单
	Sheets() []TableSheet
}

type ValueOption struct {
	ValueAsFloat bool
}

type TableSheet interface {

	// 表单名称
	Name() string

	// 从表单指定单元格获取值
	GetValue(row, col int, opt *ValueOption) string

	// 最大列
	MaxColumn() int

	// 最大行(含表头), 之后的行均为空
	MaxRow() int

	// 写入一行数据
	WriteRow(valueList ...string)

	// 检测本行前maxCol列是否全空, maxCol为-1时检查所有列
	IsRowEmpty(row, maxCol int) bool
}

func ReadSheetRow(sheet TableSheet, row int) (ret []string) {

	maxCol := sheet.MaxColumn()
	ret = make([]string, maxCol)
	for col := 0; col < maxCol; col++ {

		value := sheet.GetValue(row, col, nil)

		ret[col] = value
	}

	return
}
