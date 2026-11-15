package pbdata

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"path/filepath"
)

func exportTable(globals *model.Globals, pbFile protoreflect.FileDescriptor, tab *model.DataTable, combineRoot *dynamicpb.Message) {
	md := pbFile.Messages().ByName(protoreflect.Name(tab.OriginalHeaderType))

	combineField := combineRoot.Descriptor().Fields().ByName(protoreflect.Name(tab.OriginalHeaderType))
	list := combineRoot.NewField(combineField).List()

	// 每个表的所有列
	headers := globals.Types.AllFieldByName(tab.OriginalHeaderType)

	// 遍历每一行
	for row := 1; row < len(tab.Rows); row++ {

		rowMsg := dynamicpb.NewMessage(md)

		for col, field := range headers {

			if globals.CanDoAction(model.ActionNoGenFieldPbBinary, field) {
				continue
			}

			// 在单元格找到值
			valueCell := tab.GetCell(row, col)
			if valueCell == nil {
				continue
			}

			setField(globals, pbFile, rowMsg, field, valueCell)
		}

		list.Append(protoreflect.ValueOf(rowMsg))
	}

	combineRoot.Set(combineField, protoreflect.ValueOfList(list))
}

func Generate(globals *model.Globals) (data []byte, err error) {
	if globals.ProtoDescriptorFile != "" || globals.ProtoMappingFile != "" {
		if err := gen.RejectStructFields(globals, "protobuf (Proto mapping)"); err != nil {
			return nil, err
		}
		schema, err := loadExternalSchema(globals)
		if err != nil {
			return nil, err
		}
		message, err := schema.export(globals, "")
		if err != nil {
			return nil, err
		}
		return proto.MarshalOptions{Deterministic: true}.Marshal(message)
	}

	if err := gen.ValidateNames(globals, "protobuf"); err != nil {
		return nil, err
	}
	pbFile, err := buildDynamicType(globals)
	if err != nil {
		return nil, err
	}

	combineD := pbFile.Messages().ByName(protoreflect.Name(globals.CombineStructName))

	combineRoot := dynamicpb.NewMessage(combineD)

	// 所有的表
	for _, tab := range globals.Datas.AllTables() {
		exportTable(globals, pbFile, tab, combineRoot)
	}

	return proto.MarshalOptions{Deterministic: true}.Marshal(combineRoot)
}

func Output(globals *model.Globals, param string) (err error) {
	if globals.ProtoDescriptorFile != "" || globals.ProtoMappingFile != "" {
		if err := gen.RejectStructFields(globals, "protobuf_dir (Proto mapping)"); err != nil {
			return err
		}
		return outputExternal(globals, param)
	}

	if err := gen.ValidateNames(globals, "protobuf"); err != nil {
		return err
	}
	pbFile, err := buildDynamicType(globals)
	if err != nil {
		return err
	}

	for _, tab := range globals.Datas.AllTables() {

		combineD := pbFile.Messages().ByName(protoreflect.Name(globals.CombineStructName))

		combineRoot := dynamicpb.NewMessage(combineD)

		exportTable(globals, pbFile, tab, combineRoot)

		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(combineRoot)
		if err != nil {
			return err
		}

		err = helper.WriteFile(filepath.Join(param, tab.HeaderType+".pbb"), data)

		if err != nil {
			return err
		}
	}

	return nil
}

// setField stores one cell in msg: scalars and arrays as before, nested
// structs as messages (left unset when all their members are empty, like
// an absent message), struct arrays as repeated messages.
func setField(globals *model.Globals, pbFile protoreflect.FileDescriptor, msg *dynamicpb.Message, field *model.TypeDefine, cell *model.Cell) {
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(field.FieldName))
	switch {
	case globals.Types.IsStructField(field):
		md := pbFile.Messages().ByName(protoreflect.Name(field.FieldType))
		if field.IsArray() {
			if len(cell.Structs) == 0 {
				return
			}
			list := msg.NewField(fd).List()
			for _, v := range cell.Structs {
				list.Append(protoreflect.ValueOfMessage(structMessage(globals, pbFile, md, v)))
			}
			msg.Set(fd, protoreflect.ValueOfList(list))
		} else if cell.Struct != nil && !cell.Struct.Empty() {
			msg.Set(fd, protoreflect.ValueOfMessage(structMessage(globals, pbFile, md, cell.Struct)))
		}
	case field.IsArray():
		list := msg.NewField(fd).List()
		tableValue2PbValueList(globals, cell, field, list)
		msg.Set(fd, protoreflect.ValueOfList(list))
	default:
		msg.Set(fd, tableValue2PbValue(globals, cell.Value, field))
	}
}

func structMessage(globals *model.Globals, pbFile protoreflect.FileDescriptor, md protoreflect.MessageDescriptor, v *model.StructValue) *dynamicpb.Message {
	m := dynamicpb.NewMessage(md)
	for i, member := range globals.Types.AllFieldByName(v.Type) {
		if globals.CanDoAction(model.ActionNoGenFieldPbBinary, member) || i >= len(v.Cells) {
			continue
		}
		setField(globals, pbFile, m, member, v.Cells[i])
	}
	return m
}
