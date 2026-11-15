package bindata

import (
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"path/filepath"
)

func writeHeader(writer *BinaryWriter) error {
	if err := writer.WriteString("TABTOY"); err != nil {
		return err
	}
	if err := writer.WriteUInt32(4); err != nil {
		return err
	}

	return nil
}

func exportTable(globals *model.Globals, writer *BinaryWriter, tab *model.DataTable) error {
	// 结构体的标记头, 方便跨过不同类型
	if err := writer.WriteUInt32(MakeTagStructArray()); err != nil {
		return err
	}

	if err := writer.WriteString(tab.HeaderType); err != nil {
		return err
	}
	if err := writer.WriteUInt32(uint32(len(tab.Rows) - 1)); err != nil {
		return err
	}

	// 表的每一个行
	for row := 1; row < len(tab.Rows); row++ {

		structWriter, err := writeStruct(globals, tab, row)
		if err != nil {
			return err
		}
		data := structWriter.Bytes()
		// 每行结构体包含长度边界，读取方可以跳过未知字段。
		if err := writer.WriteUInt32(uint32(len(data))); err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
	}

	return nil
}

func Generate(globals *model.Globals) (data []byte, err error) {

	totalWriter := NewBinaryWriter()

	if err := writeHeader(totalWriter); err != nil {
		return nil, err
	}

	for _, tab := range globals.Datas.AllTables() {

		err := exportTable(globals, totalWriter, tab)
		if err != nil {
			return nil, err
		}
	}

	data = totalWriter.Bytes()

	return
}

func Output(globals *model.Globals, param string) (err error) {

	for _, tab := range globals.Datas.AllTables() {

		writer := NewBinaryWriter()
		if err := writeHeader(writer); err != nil {
			return err
		}

		err := exportTable(globals, writer, tab)
		if err != nil {
			return err
		}

		err = helper.WriteFile(filepath.Join(param, tab.HeaderType+".bin"), writer.Bytes())

		if err != nil {
			return err
		}
	}

	return nil
}
