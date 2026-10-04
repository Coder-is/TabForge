package bindata

import (
	"strconv"

	"github.com/Coder-is/TabForge/v3/model"
)

func writeValue(globals *model.Globals, writer *BinaryWriter, field *model.TypeDefine, kind, value string) error {
	if globals.Types.IsEnumKind(field.FieldType) {
		kind = "int32"
		if value != "" {
			value = globals.Types.ResolveEnumValue(field.FieldType, value)
		}
	}
	if value == "" && kind != "string" && kind != "bool" {
		value = "0"
	}
	switch kind {
	case "int16":
		v, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return err
		}
		return writer.WriteInt16(int16(v))
	case "int32":
		v, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return err
		}
		return writer.WriteInt32(int32(v))
	case "int64":
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
		return writer.WriteInt64(v)
	case "uint16":
		v, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return err
		}
		return writer.WriteUInt16(uint16(v))
	case "uint32":
		v, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return err
		}
		return writer.WriteUInt32(uint32(v))
	case "uint64":
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return err
		}
		return writer.WriteUInt64(v)
	case "float32":
		v, err := strconv.ParseFloat(value, 32)
		if err != nil {
			return err
		}
		return writer.WriteFloat32(float32(v))
	case "float64":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		return writer.WriteFloat64(v)
	case "bool":
		v, err := model.ParseBool(value)
		if err != nil {
			return err
		}
		return writer.WriteBool(v)
	case "string":
		return writer.WriteString(value)
	default:
		panic("unknown binary type: " + field.FieldType)
	}
}
