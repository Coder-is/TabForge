package bindata

import (
	"github.com/Coder-is/TabForge/v3/model"
)

// 写入表的一行
func writeStruct(globals *model.Globals, tab *model.DataTable, row int) (*BinaryWriter, error) {

	structWriter := NewBinaryWriter()

	// 一个结构体
	for _, header := range tab.Headers {

		if header == nil {
			continue
		}

		if globals.CanDoAction(model.ActionNoGenFieldBinary, header) {
			continue
		}

		cell := tab.GetCell(row, header.Cell.Col)

		if cell == nil {
			continue
		}

		if err := writeField(globals, structWriter, header.TypeInfo, cell, header.Cell.Col); err != nil {
			return nil, err
		}
	}

	return structWriter, nil
}

// writeField writes one field: a pair per value (one per array element),
// or for nested structs the tags reserved in the format: 11 with one
// length-prefixed struct, 111 with a count and length-prefixed elements,
// as TableReader.ReadStruct reads them. Empty values, structs whose
// members are all empty and empty arrays are left out.
func writeField(globals *model.Globals, w *BinaryWriter, field *model.TypeDefine, cell *model.Cell, fieldIndex int) error {
	if globals.Types.IsStructField(field) {
		structs := cell.Structs
		if !field.IsArray() {
			if cell.Struct == nil || cell.Struct.Empty() {
				return nil
			}
			structs = []*model.StructValue{cell.Struct}
		} else if len(structs) == 0 {
			return nil
		}
		if err := w.WriteUInt32(MakeTag(globals, field, fieldIndex)); err != nil {
			return err
		}
		if field.IsArray() {
			if err := w.WriteUInt32(uint32(len(structs))); err != nil {
				return err
			}
		}
		for _, v := range structs {
			body, err := encodeStruct(globals, field.FieldType, v)
			if err != nil {
				return err
			}
			if err := w.WriteUInt32(uint32(len(body))); err != nil {
				return err
			}
			if _, err := w.Write(body); err != nil {
				return err
			}
		}
		return nil
	}

	goType := model.LanguagePrimitive(field.FieldType, "go")

	// 写入字段
	if field.IsArray() {
		for _, elementValue := range cell.ValueList {
			if err := writePair(globals, w, field, goType, elementValue, fieldIndex); err != nil {
				return err
			}
		}
		return nil
	}

	// 空格不输出
	if cell.Value != "" {
		return writePair(globals, w, field, goType, cell.Value, fieldIndex)
	}
	return nil
}

// encodeStruct writes a nested struct's members, numbered by their place
// in the struct type like the generated readers' tags.
func encodeStruct(globals *model.Globals, typeName string, v *model.StructValue) ([]byte, error) {
	w := NewBinaryWriter()
	for i, member := range globals.Types.AllFieldByName(typeName) {
		if globals.CanDoAction(model.ActionNoGenFieldBinary, member) || i >= len(v.Cells) {
			continue
		}
		if err := writeField(globals, w, member, v.Cells[i], i); err != nil {
			return nil, err
		}
	}
	return w.Bytes(), nil
}
