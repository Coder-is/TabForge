package model

import (
	"fmt"
	"strings"
)

type HeaderField struct {
	Cell     *Cell       // 表头单元格内容
	TypeInfo *TypeDefine // 在类型表中找到对应的类型信息
	// 结构成员列(字段.成员)的完整路径; TypeInfo为路径末端的成员. 普通列为空
	Path []PathStep
}

func (self *HeaderField) String() string {

	var sb strings.Builder

	if self.Cell != nil {
		sb.WriteString("Cell: ")
		sb.WriteString(self.Cell.String())
	}

	if self.TypeInfo != nil {
		sb.WriteString("TypeInfo: ")
		sb.WriteString(fmt.Sprintf("%+v", self.TypeInfo))
	}

	return sb.String()
}
