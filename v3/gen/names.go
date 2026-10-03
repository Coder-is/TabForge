package gen

import (
	"fmt"
	"go/token"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Coder-is/TabForge/v3/model"
	"golang.org/x/text/unicode/norm"
)

// ValidateNames checks the unescaped identifiers emitted by the selected exporter.
// Source aliases remain unrestricted; existing-Proto mappings do not emit source names.
func ValidateNames(globals *model.Globals, language string) error {
	v := nameValidator{language: language, symbols: make(map[string]string)}
	root := globals.CombineStructName
	if err := v.identifier(root, "type", "-combinename"); err != nil {
		return err
	}
	if language != "lua" {
		if globals.PackageName == "" && language != "protobuf" {
			return fmt.Errorf("%s export requires a non-empty -package", language)
		}
		if globals.PackageName != "" {
			for _, part := range strings.Split(globals.PackageName, ".") {
				if err := v.identifier(part, "package", "-package"); err != nil {
					return err
				}
			}
		}
		if language == "go" && strings.Contains(globals.PackageName, ".") {
			return fmt.Errorf("go package must be a single identifier: %q", globals.PackageName)
		}
	}
	if language != "lua" {
		if err := v.add("types", root, "-combinename"); err != nil {
			return err
		}
	}
	var helpers string
	switch language {
	case "go":
		helpers = "errors error init " + root + "EnumValue New" + root
	case "csharp":
		helpers = "tabtoy List Dictionary Int16 Int32 Int64 UInt16 UInt32 UInt64"
	case "java":
		helpers = "List Map ArrayList HashMap String Integer Long Float Double Boolean TableEvent"
	}
	for _, name := range strings.Fields(helpers) {
		if err := v.add("types", name, "generated helper"); err != nil {
			return err
		}
	}
	for _, td := range globals.Types.Raw() {
		field := td.Define
		if field.IsBuiltin {
			continue
		}
		where := definitionLocation(td)
		emitted := language != "lua" || field.Kind == model.TypeUsage_Enum || globals.Datas.GetDataTable(field.ObjectType) != nil
		if !emitted {
			continue
		}
		if err := v.identifier(field.ObjectType, "type", where); err != nil {
			return err
		}
		typeScope := "types"
		if language == "lua" {
			typeScope = "lua types" // Structs aren't declared on the Lua root.
			if field.Kind == model.TypeUsage_Enum {
				typeScope = "root"
			}
		}
		// Multiple fields belong to the same declared type.
		if _, ok := v.symbols[typeScope+"\x00"+field.ObjectType]; !ok {
			if err := v.add(typeScope, field.ObjectType, "type "+field.ObjectType); err != nil {
				return err
			}
		} else if v.symbols[typeScope+"\x00"+field.ObjectType] != "type "+field.ObjectType {
			return fmt.Errorf("%s type %q at %s conflicts with %s", language, field.ObjectType, where, v.symbols[typeScope+"\x00"+field.ObjectType])
		}
		if field.Kind == model.TypeUsage_HeaderStruct && (language == "csharp" && globals.CanDoAction(model.ActionNoGennFieldCsharp, field) || language == "lua" && globals.CanDoAction(model.ActionNoGennFieldLua, field)) {
			continue
		}
		role := "field"
		if field.Kind == model.TypeUsage_Enum {
			role = "enum value"
		}
		if err := v.identifier(field.FieldName, role, where); err != nil {
			return err
		}
		if language == "go" && field.Kind == model.TypeUsage_HeaderStruct {
			r, _ := utf8.DecodeRuneInString(field.FieldName)
			if !unicode.IsUpper(r) {
				return fmt.Errorf("go JSON field %q at %s must be exported", field.FieldName, where)
			}
		}
		if language == "csharp" && (field.FieldName == field.ObjectType || globals.GenBinary && field.Kind == model.TypeUsage_HeaderStruct && field.FieldName == "Deserialize") {
			return fmt.Errorf("csharp field %q at %s conflicts with its enclosing type or Deserialize method", field.FieldName, where)
		}
		if language == "java" && field.Kind == model.TypeUsage_Enum && field.FieldName == field.ObjectType {
			return fmt.Errorf("java enum value %q at %s conflicts with its generated value field", field.FieldName, where)
		}
		if language == "protobuf" {
			scope, name := "fields:"+field.ObjectType, protoFieldKey(field.FieldName)
			if field.Kind == model.TypeUsage_Enum {
				if err := v.add("enumNames:"+field.ObjectType, protoEnumKey(field.ObjectType, field.FieldName), where); err != nil {
					return err
				}
				scope, name = "types", field.FieldName // Proto enum values share their enclosing package scope.
			}
			if err := v.add(scope, name, where); err != nil {
				return err
			}
		}
	}
	if language == "go" {
		for _, name := range globals.Types.EnumNames() {
			for _, suffix := range []string{"EnumValues", "MapperValueByName", "MapperNameByValue"} {
				if err := v.add("types", name+suffix, "generated enum helper"); err != nil {
					return err
				}
			}
			for _, field := range globals.Types.AllFieldByName(name) {
				if err := v.add("types", name+"_"+field.FieldName, "generated enum constant"); err != nil {
					return err
				}
			}
		}
	}
	switch language {
	case "go":
		helpers = "postHandlers preHandlers indexHandler resetHandler RegisterPostEntry RegisterPreEntry ResetData BuildData InvokePreHandler InvokePostHandler IndexTable ResetTable"
	case "csharp":
		helpers = "ResetData"
		if globals.GenBinary {
			helpers += " Deserialize IndexData"
		}
	case "java":
		helpers = "eventHandlers"
	default:
		helpers = ""
	}
	for _, name := range strings.Fields(helpers) {
		if err := v.add("root", name, "generated member"); err != nil {
			return err
		}
	}
	for _, tab := range globals.Datas.AllTables() {
		if err := v.identifier(tab.HeaderType, "field", "table "+tab.HeaderType); err != nil {
			return err
		}
		if language == "go" {
			r, _ := utf8.DecodeRuneInString(tab.HeaderType)
			if !unicode.IsUpper(r) {
				return fmt.Errorf("go JSON table %q must be exported", tab.HeaderType)
			}
		}
		name := tab.HeaderType
		if language == "protobuf" {
			name = protoFieldKey(name)
		}
		if err := v.add("root", name, "table "+tab.HeaderType); err != nil {
			return err
		}
		if language == "protobuf" {
			continue // Protobuf doesn't emit runtime indices.
		}
		for _, index := range GetIndicesByTable(tab) {
			if language == "csharp" && globals.CanDoAction(model.ActionNoGennFieldCsharp, index.FieldInfo) {
				continue
			}
			if err := v.identifier(index.FieldInfo.FieldName, "field", "index "+tab.HeaderType); err != nil {
				return err
			}
			if err := v.add("root", tab.HeaderType+"By"+index.FieldInfo.FieldName, "generated index"); err != nil {
				return err
			}
		}
	}
	if language == "go" || language == "csharp" {
		for _, name := range KeyValueTypeNames(globals) {
			if globals.Datas.GetDataTable(name) != nil {
				if err := v.add("root", "GetKeyValue_"+name, "generated KV accessor"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type nameValidator struct {
	language string
	symbols  map[string]string
}

func (v *nameValidator) add(scope, name, origin string) error {
	key := scope + "\x00" + name
	if previous, ok := v.symbols[key]; ok {
		return fmt.Errorf("%s name %q in %s conflicts: %s and %s", v.language, name, scope, previous, origin)
	}
	v.symbols[key] = origin
	return nil
}

func definitionLocation(td *model.TypeData) string {
	if td.Tab != nil {
		if cell := td.Tab.GetValueByName(td.Row, "字段名"); cell != nil {
			return cell.String()
		}
	}
	return td.Define.ObjectType + "." + td.Define.FieldName
}

func (v *nameValidator) identifier(name, role, where string) error {
	valid := name != ""
	keywords := ""
	switch v.language {
	case "go":
		valid = valid && name != "_" && token.IsIdentifier(name) && !(role == "type" && model.PrimitiveExists(name))
	case "csharp":
		valid = valid && unicodeIdentifier(name, false) && norm.NFC.IsNormalString(name)
		keywords = "abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void volatile while"
	case "java":
		valid = valid && unicodeIdentifier(name, true)
		keywords = "abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while true false null _"
		if role == "type" {
			keywords += " permits record sealed var yield"
		}
	case "lua":
		valid = valid && asciiIdentifier(name)
		keywords = "and break do else elseif end false for function goto if in local nil not or repeat return then true until while"
	case "protobuf":
		valid = valid && asciiIdentifier(name)
		if role == "enum value" {
			keywords = "option reserved"
		}
		if role == "type" {
			keywords = "double float int32 int64 uint32 uint64 sint32 sint64 fixed32 fixed64 sfixed32 sfixed64 bool string bytes map group repeated optional required message enum extend extensions option reserved oneof service rpc returns syntax package import weak public stream"
		}
	default:
		return fmt.Errorf("unknown output language %q", v.language)
	}
	if strings.Contains(" "+keywords+" ", " "+name+" ") {
		valid = false
	}
	if !valid {
		return fmt.Errorf("invalid %s %s name %q at %s", v.language, role, name, where)
	}
	return nil
}

func asciiIdentifier(name string) bool {
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

// Raw identifiers only: templates do not emit C# @ escapes or Java Unicode escapes.
func unicodeIdentifier(name string, java bool) bool {
	for i, r := range name {
		letter := unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
		connector := unicode.Is(unicode.Pc, r)
		if letter || r == '_' || java && (connector || unicode.Is(unicode.Sc, r)) {
			continue
		}
		if i > 0 && (unicode.IsDigit(r) || connector || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)) {
			continue
		}
		return false
	}
	return name != ""
}

// Match the Proto3 name collision rules in the bundled protobuf descriptor validator.
func protoFieldKey(name string) string {
	return strings.Replace(strings.ToLower(name), "_", "", -1)
}

func protoEnumKey(enum, name string) string {
	prefix := protoFieldKey(enum)
	var scanned strings.Builder
	remaining := name
	for i, r := range name {
		if r != '_' {
			scanned.WriteRune(unicode.ToLower(r))
		}
		if scanned.Len() >= len(prefix) {
			if scanned.String() == prefix {
				if suffix := strings.TrimLeft(name[i+1:], "_"); suffix != "" {
					remaining = suffix
				}
			}
			break
		}
	}
	var key strings.Builder
	for _, word := range strings.Split(remaining, "_") {
		if word != "" {
			key.WriteString(strings.ToUpper(word[:1]))
			key.WriteString(strings.ToLower(word[1:]))
		}
	}
	return key.String()
}
