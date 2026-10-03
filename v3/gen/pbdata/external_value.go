package pbdata

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Coder-is/TabForge/v3/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func (schema *externalSchema) exportRow(globals *model.Globals, table *externalTable, row int) (protoreflect.Message, error) {
	values := make(map[string]interface{})
	var locations []string
	for _, column := range table.columns {
		cell := table.table.GetCell(row, column.col)
		if cell == nil {
			continue
		}
		fd := column.path[len(column.path)-1]
		value, present, err := externalCellValue(globals, cell, column.source, fd)
		if err != nil {
			return nil, fmt.Errorf("table %s column %s -> %s, %s: %v", table.table.HeaderType, column.source.FieldName, fd.FullName(), cell.String(), err)
		}
		if !present {
			continue
		}
		locations = append(locations, cell.String())
		target := values
		for _, part := range column.path[:len(column.path)-1] {
			name := string(part.Name())
			next, ok := target[name].(map[string]interface{})
			if !ok {
				next = make(map[string]interface{})
				target[name] = next
			}
			target = next
		}
		target[string(fd.Name())] = value
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("table %s row %d: %v", table.table.HeaderType, row+1, err)
	}
	message := dynamicpb.NewMessage(table.message)
	if err := (protojson.UnmarshalOptions{Resolver: schema.types}).Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("table %s row %d (%s): %v", table.table.HeaderType, row+1, strings.Join(locations, "; "), err)
	}
	return message.ProtoReflect(), nil
}

func externalCellValue(globals *model.Globals, cell *model.Cell, source *model.TypeDefine, field protoreflect.FieldDescriptor) (interface{}, bool, error) {
	if source.IsArray() {
		if len(cell.ValueList) == 0 {
			return nil, false, nil
		}
		values := make([]interface{}, 0, len(cell.ValueList))
		for _, text := range cell.ValueList {
			value, err := externalScalarValue(globals, source, text, field)
			if err != nil {
				return nil, false, err
			}
			values = append(values, value)
		}
		return values, true, nil
	}
	if cell.Value == "" {
		return nil, false, nil
	}
	if field.IsList() || field.IsMap() || field.Message() != nil {
		value, err := externalJSONValue(cell.Value)
		return value, err == nil, err
	}
	value, err := externalScalarValue(globals, source, cell.Value, field)
	return value, err == nil, err
}

func externalJSONValue(text string) (json.RawMessage, error) {
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("expected valid ProtoJSON for a message, map or repeated field")
	}
	return json.RawMessage(text), nil
}

func externalScalarValue(globals *model.Globals, source *model.TypeDefine, text string, field protoreflect.FieldDescriptor) (interface{}, error) {
	switch field.Kind() {
	case protoreflect.StringKind, protoreflect.BytesKind:
		return text, nil // ProtoJSON validates base64 for bytes.
	case protoreflect.BoolKind:
		return model.ParseBool(text)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return externalSignedValue(text, 32)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return externalSignedValue(text, 64)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return externalUnsignedValue(text, 32)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return externalUnsignedValue(text, 64)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		if text == "" {
			text = "0"
		}
		bits := 64
		if field.Kind() == protoreflect.FloatKind {
			bits = 32
		}
		if text == "Infinity" {
			text = "+Inf"
		} else if text == "-Infinity" {
			text = "-Inf"
		}
		value, err := strconv.ParseFloat(text, bits)
		if err != nil {
			return nil, err
		}
		switch {
		case math.IsNaN(value):
			return "NaN", nil
		case math.IsInf(value, 1):
			return "Infinity", nil
		case math.IsInf(value, -1):
			return "-Infinity", nil
		default:
			return json.Number(strconv.FormatFloat(value, 'g', -1, bits)), nil
		}
	case protoreflect.EnumKind:
		if text == "" {
			return int32(field.Default().Enum()), nil
		}
		if value := field.Enum().Values().ByName(protoreflect.Name(text)); value != nil {
			return string(value.Name()), nil
		}
		if source != nil && globals.Types.IsEnumKind(source.FieldType) {
			if value := globals.Types.GetEnumValue(source.FieldType, text); value != nil {
				text = value.Define.Value
			}
		}
		value, err := strconv.ParseInt(text, 10, 32)
		if err != nil || field.Enum().Values().ByNumber(protoreflect.EnumNumber(value)) == nil {
			return nil, fmt.Errorf("unknown enum value %q for %s", text, field.Enum().FullName())
		}
		return int32(value), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if text == "" {
			text = "{}"
		}
		return externalJSONValue(text)
	default:
		return nil, fmt.Errorf("unsupported Proto kind %s", field.Kind())
	}
}

func externalSignedValue(text string, bits int) (interface{}, error) {
	if text == "" {
		text = "0"
	}
	value, err := strconv.ParseInt(text, 10, bits)
	if err != nil {
		return nil, err
	}
	if bits == 64 {
		return strconv.FormatInt(value, 10), nil // Preserve all 64 bits in ProtoJSON.
	}
	return int32(value), nil
}

func externalUnsignedValue(text string, bits int) (interface{}, error) {
	if text == "" {
		text = "0"
	}
	value, err := strconv.ParseUint(text, 10, bits)
	if err != nil {
		return nil, err
	}
	if bits == 64 {
		return strconv.FormatUint(value, 10), nil
	}
	return uint32(value), nil
}

func externalMapKey(text string, field protoreflect.FieldDescriptor) (protoreflect.MapKey, error) {
	switch field.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(text).MapKey(), nil
	case protoreflect.BoolKind:
		value, err := model.ParseBool(text)
		return protoreflect.ValueOfBool(value).MapKey(), err
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		value, err := strconv.ParseInt(text, 10, 32)
		return protoreflect.ValueOfInt32(int32(value)).MapKey(), err
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		value, err := strconv.ParseInt(text, 10, 64)
		return protoreflect.ValueOfInt64(value).MapKey(), err
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		value, err := strconv.ParseUint(text, 10, 32)
		return protoreflect.ValueOfUint32(uint32(value)).MapKey(), err
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		value, err := strconv.ParseUint(text, 10, 64)
		return protoreflect.ValueOfUint64(value).MapKey(), err
	default:
		return protoreflect.MapKey{}, fmt.Errorf("unsupported map key kind %s", field.Kind())
	}
}
