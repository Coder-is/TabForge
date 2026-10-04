package model

import (
	"fmt"
	"github.com/Coder-is/TabForge/util"
	"github.com/Coder-is/TabForge/v3/report"
)

type Cell struct {
	Value     string
	ValueList []string // merge之后, 数组值保存在这里
	Row       int      // base 0
	Col       int      // base 0
	Table     *DataTable
}

func (self *Cell) SourceLocation() report.Location {
	location := report.Location{Line: self.Row + 1, Column: self.Col + 1, Cell: util.R1C1ToA1(self.Row+1, self.Col+1)}
	if self.Table != nil {
		location.File, location.Sheet = self.Table.FileName, self.Table.SheetName
	}
	return location
}

// 全拷贝
func (self *Cell) CopyFrom(c *Cell) {
	self.Value = c.Value
	self.Row = c.Row
	self.Col = c.Col
	self.Table = c.Table
}

func (self *Cell) String() string {

	var file, sheet string
	if self.Table != nil {
		file = self.Table.FileName
		sheet = self.Table.SheetName
	}

	var value string
	if len(self.ValueList) > 0 {
		value = fmt.Sprintf("%+v", self.ValueList)
	} else {
		value = self.Value
	}

	return fmt.Sprintf("'%s' @%s|%s(%s)", value, file, sheet, util.R1C1ToA1(self.Row+1, self.Col+1))
}
