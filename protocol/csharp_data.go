package protocol

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// CSharpDataTypes emits ProtoJSON DTOs, not Protobuf binary message classes.
// Nullable scalar fields preserve absence and 64-bit values remain strings.
func (s *Schema) CSharpDataTypes() string {
	var messages []protoreflect.MessageDescriptor
	var enums []protoreflect.EnumDescriptor
	var collect func(protoreflect.MessageDescriptors)
	collect = func(list protoreflect.MessageDescriptors) {
		for i := 0; i < list.Len(); i++ {
			m := list.Get(i)
			if !m.IsMapEntry() {
				messages = append(messages, m)
			}
			for j := 0; j < m.Enums().Len(); j++ {
				enums = append(enums, m.Enums().Get(j))
			}
			collect(m.Messages())
		}
	}
	s.contract.Files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(f.Path(), "google/protobuf/") {
			collect(f.Messages())
			for i := 0; i < f.Enums().Len(); i++ {
				enums = append(enums, f.Enums().Get(i))
			}
		}
		return true
	})
	sort.Slice(messages, func(i, j int) bool { return messages[i].FullName() < messages[j].FullName() })
	sort.Slice(enums, func(i, j int) bool { return enums[i].FullName() < enums[j].FullName() })
	var b strings.Builder
	b.WriteString("// Generated ProtoJSON types. Regenerate with TabForge; do not edit.\nusing System.Collections.Generic;\nusing Newtonsoft.Json;\nusing Newtonsoft.Json.Linq;\nusing Newtonsoft.Json.Converters;\nusing TabForge.Data;\n\nnamespace TabForge.Data.Generated\n{\n")
	for _, e := range enums {
		fmt.Fprintf(&b, "    [JsonConverter(typeof(StringEnumConverter))]\n    public enum %s\n    {\n", csName("E", e.FullName()))
		for i := 0; i < e.Values().Len(); i++ {
			v := e.Values().Get(i)
			fmt.Fprintf(&b, "        @%s = %d,\n", v.Name(), v.Number())
		}
		b.WriteString("    }\n")
	}
	for _, m := range messages {
		fmt.Fprintf(&b, "    [ProtoMessage(%s)]\n    public sealed class %s\n    {\n", strconv.Quote(string(m.FullName())), csName("M", m.FullName()))
		for i := 0; i < m.Fields().Len(); i++ {
			f := m.Fields().Get(i)
			value := f
			if f.IsMap() {
				value = f.MapValue()
			}
			typeName := csFieldType(value, !f.IsList() && !f.IsMap())
			if f.IsMap() {
				typeName = "Dictionary<string, " + typeName + ">"
			} else if f.IsList() {
				typeName = "List<" + typeName + ">"
			}
			fmt.Fprintf(&b, "        [JsonProperty(%s)]\n        public %s @%s { get; set; }\n", strconv.Quote(f.JSONName()), typeName, f.Name())
		}
		b.WriteString("    }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func csName(prefix string, name protoreflect.FullName) string {
	return prefix + "_" + strings.ReplaceAll(strings.ReplaceAll(string(name), "_", "_0"), ".", "__")
}

func csFieldType(f protoreflect.FieldDescriptor, nullable bool) string {
	var t string
	switch f.Kind() {
	case protoreflect.BoolKind:
		t = "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		t = "int"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		t = "uint"
	case protoreflect.FloatKind:
		t = "float"
	case protoreflect.DoubleKind:
		t = "double"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind, protoreflect.StringKind, protoreflect.BytesKind:
		return "string"
	case protoreflect.EnumKind:
		if f.Enum().FullName() == "google.protobuf.NullValue" {
			return "JToken"
		}
		t = csName("E", f.Enum().FullName())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		switch f.Message().FullName() {
		case "google.protobuf.Timestamp", "google.protobuf.Duration", "google.protobuf.FieldMask", "google.protobuf.Int64Value", "google.protobuf.UInt64Value", "google.protobuf.StringValue", "google.protobuf.BytesValue":
			return "string"
		case "google.protobuf.Int32Value":
			return "int?"
		case "google.protobuf.UInt32Value":
			return "uint?"
		case "google.protobuf.BoolValue":
			return "bool?"
		case "google.protobuf.FloatValue":
			return "float?"
		case "google.protobuf.DoubleValue":
			return "double?"
		case "google.protobuf.Struct", "google.protobuf.Any", "google.protobuf.Empty":
			return "JObject"
		case "google.protobuf.ListValue":
			return "JArray"
		case "google.protobuf.Value":
			return "JToken"
		default:
			return csName("M", f.Message().FullName())
		}
	default:
		return "JToken"
	}
	if nullable {
		t += "?"
	}
	return t
}
