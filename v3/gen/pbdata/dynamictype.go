package pbdata

import (
	"github.com/Coder-is/TabForge/v3/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"strconv"
)

// https://farer.org/2020/04/17/go-protobuf-apiv2-reflect-dynamicpb/
func buildDynamicType(globals *model.Globals) (protoreflect.FileDescriptor, error) {
	var file descriptorpb.FileDescriptorProto
	file.Syntax = proto.String("proto3")
	file.Name = proto.String(globals.CombineStructName)
	file.Package = proto.String(globals.PackageName)

	message := func(typeName string) *descriptorpb.DescriptorProto {
		var desc descriptorpb.DescriptorProto
		desc.Name = proto.String(typeName)
		for index, field := range globals.Types.AllFieldByName(typeName) {
			var fd descriptorpb.FieldDescriptorProto
			fd.Name = proto.String(field.FieldName)
			fd.Number = proto.Int32(int32(index + 1))
			fd.JsonName = proto.String(field.FieldName)
			tableType2PbType(globals, field, &fd)
			if field.IsArray() {
				fd.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			} else {
				fd.Label = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
			}

			desc.Field = append(desc.Field, &fd)
		}

		return &desc
	}

	// Table messages first: the combine message has one field per table.
	tables := map[string]bool{}
	var structTypes []string
	for _, tab := range globals.Datas.AllTables() {
		file.MessageType = append(file.MessageType, message(tab.OriginalHeaderType))
		tables[tab.OriginalHeaderType] = true
		structTypes = collectStructTypes(globals, tab.OriginalHeaderType, structTypes)
	}
	var structMessages []*descriptorpb.DescriptorProto
	for _, name := range structTypes {
		if !tables[name] {
			structMessages = append(structMessages, message(name))
		}
	}

	for _, enumName := range globals.Types.EnumNames() {

		var ed descriptorpb.EnumDescriptorProto
		ed.Name = proto.String(enumName)

		for _, field := range globals.Types.AllFieldByName(enumName) {
			var vd descriptorpb.EnumValueDescriptorProto
			vd.Name = proto.String(field.FieldName)
			v, _ := strconv.Atoi(field.Value)
			vd.Number = proto.Int32(int32(v))
			ed.Value = append(ed.Value, &vd)
		}
		file.EnumType = append(file.EnumType, &ed)
	}

	var combine descriptorpb.DescriptorProto
	combine.Name = proto.String(globals.CombineStructName)
	for index, md := range file.MessageType {
		var fd descriptorpb.FieldDescriptorProto
		fd.Name = proto.String(md.GetName())
		fd.Number = proto.Int32(int32(index + 1))
		fd.JsonName = proto.String(md.GetName())
		fd.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
		fd.TypeName = proto.String(globals.PackageName + "." + md.GetName())
		fd.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		combine.Field = append(combine.Field, &fd)
	}

	file.MessageType = append(file.MessageType, &combine)
	file.MessageType = append(file.MessageType, structMessages...)

	return protodesc.NewFile(&file, nil)
}

// collectStructTypes appends the struct types typeName's fields use, at
// any depth, that are not in list yet.
func collectStructTypes(globals *model.Globals, typeName string, list []string) []string {
	for _, field := range globals.Types.AllFieldByName(typeName) {
		if !globals.Types.IsStructField(field) {
			continue
		}
		known := false
		for _, name := range list {
			known = known || name == field.FieldType
		}
		if !known {
			list = collectStructTypes(globals, field.FieldType, append(list, field.FieldType))
		}
	}
	return list
}
