package javasrc

import (
	"fmt"
	"github.com/Coder-is/TabForge/util"
	"github.com/Coder-is/TabForge/v3/model"
	"text/template"
)

var UsefulFunc = template.FuncMap{}

// 将定义用的类型，转换为不同语言对应的复合类型

func wrapSingleValue(globals *model.Globals, valueType *model.TypeDefine, value string) string {
	switch {
	case valueType.FieldType == "string": // 字符串
		return util.StringWrap(util.StringEscape(value))
	case globals.Types.IsEnumKind(valueType.FieldType): // 枚举
		t := globals.Types.ResolveEnum(valueType.FieldType, value)
		if t != nil {
			return t.Define.ObjectType + "." + t.Define.FieldName
		}

		return ""
	case valueType.FieldType == "bool":

		v, _ := model.ParseBool(value)
		if v {
			return "true"
		}

		return "false"
	}

	if value == "" {
		value = model.FetchDefaultValue(valueType.FieldType)
	}

	// uint64 exceeds long; BigInteger keeps the full unsigned range in JSON loading and index keys.
	// A constructor keeps the qualified name in a type context, so a field named "java" cannot obscure it.
	if model.LanguagePrimitive(valueType.FieldType, "java") == bigIntegerType {
		return "new " + bigIntegerType + "(" + util.StringWrap(value) + ")"
	}

	return value
}

const bigIntegerType = "java.math.BigInteger"

func init() {
	UsefulFunc["JavaType"] = func(tf *model.TypeDefine, requireRef bool) string {

		convertedType := model.LanguagePrimitive(tf.FieldType, "java")

		if requireRef {
			// https://www.geeksforgeeks.org/difference-between-an-integer-and-int-in-java/
			switch convertedType {
			case "int":
				convertedType = "Integer"
			case "short":
				convertedType = "Short"
			case "long":
				convertedType = "Long"
			case "float":
				convertedType = "Float"
			case "double":
				convertedType = "Double"
			case "boolean":
				convertedType = "Boolean"
			}
		}

		if tf.IsArray() {
			return convertedType + "[]"
		}

		return convertedType
	}

	UsefulFunc["JavaDefaultValue"] = func(globals *model.Globals, tf *model.TypeDefine) string {

		convertedType := model.LanguagePrimitive(tf.FieldType, "java")

		if tf.IsArray() {
			return fmt.Sprintf("new %s[]{}", convertedType)
		} else {
			return wrapSingleValue(globals, tf, "")
		}
	}

}
