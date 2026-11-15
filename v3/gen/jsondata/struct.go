package jsondata

import (
	"encoding/json"
	"sort"

	"github.com/Coder-is/TabForge/v3/model"
)

// jsonStruct is a nested struct: members sorted by name, like a row.
type jsonStruct struct {
	keys   [][]byte // `"name": `
	values []interface{}
}

// wrapField converts a cell like wrapValue, and nested structs (filtered
// by action like row fields) into objects or arrays of objects. A missing
// struct is all default values; a missing struct array is empty.
func wrapField(globals *model.Globals, cell *model.Cell, field *model.TypeDefine, action string) (interface{}, error) {
	if !globals.Types.IsStructField(field) {
		return wrapValue(globals, cell, field), nil
	}
	if field.IsArray() {
		list := make([]interface{}, 0)
		if cell != nil {
			for _, v := range cell.Structs {
				obj, err := structObject(globals, field.FieldType, v, action)
				if err != nil {
					return nil, err
				}
				list = append(list, obj)
			}
		}
		return list, nil
	}
	var v *model.StructValue
	if cell != nil {
		v = cell.Struct
	}
	return structObject(globals, field.FieldType, v, action)
}

func structObject(globals *model.Globals, typeName string, v *model.StructValue, action string) (*jsonStruct, error) {
	type member struct {
		name  string
		value interface{}
	}
	var members []member
	for i, field := range globals.Types.AllFieldByName(typeName) {
		if globals.CanDoAction(action, field) {
			continue
		}
		var cell *model.Cell
		if v != nil && i < len(v.Cells) {
			cell = v.Cells[i]
		}
		value, err := wrapField(globals, cell, field, action)
		if err != nil {
			return nil, err
		}
		members = append(members, member{field.FieldName, value})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].name < members[j].name })
	obj := &jsonStruct{}
	for _, m := range members {
		key, err := json.Marshal(m.name)
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, append(key, ": "...))
		obj.values = append(obj.values, m.value)
	}
	return obj, nil
}
