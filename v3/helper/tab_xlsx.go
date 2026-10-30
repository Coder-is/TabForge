package helper

import (
	"github.com/Coder-is/TabForge/util"
	"github.com/Coder-is/TabForge/v3/report"
	"github.com/tealeg/xlsx"
	"strings"
)

type XlsxFile struct {
	file *xlsx.File

	sheets   []TableSheet
	cacheDir string
}

func (self *XlsxFile) Sheets() (ret []TableSheet) {

	return self.sheets
}

func (self *XlsxFile) Save(filename string) error {
	return self.file.Save(filename)
}

func (self *XlsxFile) Load(filename string) (err error) {

	var file *xlsx.File

	if self.cacheDir == "" {
		file, err = xlsx.OpenFile(filename)
		if err != nil {
			return err
		}
	} else {
		cache := util.NewTableCache(filename, self.cacheDir)

		if err = cache.Open(); err != nil {
			return err
		}
		defer func() {
			if closeErr := cache.Close(); err == nil {
				err = closeErr
			}
		}()

		file, err = cache.Load()
		if err != nil {
			return err
		}
		if !cache.UseCache() {
			if err := cache.Save(); err != nil {
				report.Log.Warnf("Cannot save cache for %s: %v", filename, err)
			}
		}
	}

	self.FromXFile(file)

	return nil
}

func (self *XlsxFile) FromXFile(file *xlsx.File) {
	self.file = file
	self.sheets = make([]TableSheet, 0, len(file.Sheets))

	for _, sheet := range file.Sheets {
		self.sheets = append(self.sheets, newXlsxSheet(sheet))
	}
}

func NewXlsxFile(cacheDir string) TableFile {
	return &XlsxFile{
		cacheDir: cacheDir,
	}
}

type XlsxSheet struct {
	*xlsx.Sheet
}

func (self *XlsxSheet) Name() string {
	return self.Sheet.Name
}

func (self *XlsxSheet) MaxColumn() int {
	ret := self.Sheet.MaxCol
	for _, row := range self.Sheet.Rows {
		if row != nil && len(row.Cells) > ret {
			ret = len(row.Cells)
		}
	}
	return ret
}

func (self *XlsxSheet) MaxRow() int {
	return len(self.Sheet.Rows)
}

// 读取已有单元格; 不能用Sheet.Cell, 它会为越界的行列创建单元格
func (self *XlsxSheet) cell(row, col int) *xlsx.Cell {
	if row < 0 || row >= len(self.Sheet.Rows) || col < 0 {
		return nil
	}

	r := self.Sheet.Rows[row]
	if r == nil || col >= len(r.Cells) {
		return nil
	}

	return r.Cells[col]
}

func (self *XlsxSheet) IsRowEmpty(row, maxCol int) bool {

	if row < 0 || row >= len(self.Sheet.Rows) || self.Sheet.Rows[row] == nil {
		return true
	}

	// 只检查实际存在的单元格, 带格式的空白行列不会带来额外开销
	cells := self.Sheet.Rows[row].Cells
	if maxCol == -1 || maxCol > len(cells) {
		maxCol = len(cells)
	}

	for col := 0; col < maxCol; col++ {

		if cells[col] != nil && strings.TrimSpace(cells[col].Value) != "" {
			return false
		}
	}

	return true
}

func (self *XlsxSheet) GetValue(row, col int, opt *ValueOption) (ret string) {
	c := self.cell(row, col)
	if c == nil {
		return ""
	}

	// 浮点数单元格按原样输出
	if opt != nil && opt.ValueAsFloat {
		ret, _ = c.GeneralNumeric()
		ret = strings.TrimSpace(ret)
	} else {
		// 取列头所在列和当前行交叉的单元格
		ret = strings.TrimSpace(c.Value)
	}

	return
}

func (self *XlsxSheet) WriteRow(valueList ...string) {
	row := self.Sheet.AddRow()

	for _, value := range valueList {

		cell := row.AddCell()
		cell.SetValue(value)
	}
}

func newXlsxSheet(sheet *xlsx.Sheet) TableSheet {
	return &XlsxSheet{
		Sheet: sheet,
	}
}
