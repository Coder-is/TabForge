package jsondata

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/Coder-is/TabForge/v3/model"
)

// The exporters write the document json.MarshalIndent(doc, "", "\t") would
// produce for the map-based model, byte for byte, without building a map
// per row and re-sorting its keys. Strings and floats still go through
// json.Marshal so escaping, number formatting and NaN/Inf errors match.

type jsonWriter struct {
	b   []byte
	err error
}

func (w *jsonWriter) newline(depth int) {
	w.b = append(w.b, '\n')
	for i := 0; i < depth; i++ {
		w.b = append(w.b, '\t')
	}
}

func (w *jsonWriter) marshal(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		if w.err == nil {
			w.err = err
		}
		return
	}
	w.b = append(w.b, data...)
}

func (w *jsonWriter) value(v interface{}, depth int) {
	switch v := v.(type) {
	case *jsonStruct:
		if len(v.keys) == 0 {
			w.b = append(w.b, "{}"...)
			return
		}
		w.b = append(w.b, '{')
		for i := range v.keys {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.newline(depth + 1)
			w.b = append(w.b, v.keys[i]...)
			w.value(v.values[i], depth+1)
		}
		w.newline(depth)
		w.b = append(w.b, '}')
	case []interface{}:
		if len(v) == 0 {
			w.b = append(w.b, "[]"...)
			return
		}
		w.b = append(w.b, '[')
		for i, e := range v {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.newline(depth + 1)
			w.value(e, depth+1)
		}
		w.newline(depth)
		w.b = append(w.b, ']')
	case bool:
		w.b = strconv.AppendBool(w.b, v)
	case int16:
		w.b = strconv.AppendInt(w.b, int64(v), 10)
	case int32:
		w.b = strconv.AppendInt(w.b, int64(v), 10)
	case int64:
		w.b = strconv.AppendInt(w.b, v, 10)
	case uint16:
		w.b = strconv.AppendUint(w.b, uint64(v), 10)
	case uint32:
		w.b = strconv.AppendUint(w.b, uint64(v), 10)
	case uint64:
		w.b = strconv.AppendUint(w.b, v, 10)
	default:
		w.marshal(v)
	}
}

type jsonColumn struct {
	col   int
	field *model.TypeDefine
	key   []byte // `"name": `
}

// rows writes tab's rows as an array of objects whose keys are sorted like
// a map's; a table without data rows is null, like the nil slice it was.
func (w *jsonWriter) rows(globals *model.Globals, tab *model.DataTable, action string, depth int) {
	if len(tab.Rows) <= 1 {
		w.b = append(w.b, "null"...)
		return
	}
	byName := map[string]jsonColumn{}
	for col, field := range globals.Types.AllFieldByName(tab.OriginalHeaderType) {
		if globals.CanDoAction(action, field) {
			continue
		}
		byName[field.FieldName] = jsonColumn{col: col, field: field} // A map kept the last one.
	}
	columns := make([]jsonColumn, 0, len(byName))
	for name, c := range byName {
		key, err := json.Marshal(name)
		if err != nil {
			w.err = err
			return
		}
		c.key = append(key, ": "...)
		columns = append(columns, c)
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].field.FieldName < columns[j].field.FieldName })
	w.b = append(w.b, '[')
	for row := 1; row < len(tab.Rows); row++ {
		if row > 1 {
			w.b = append(w.b, ',')
		}
		w.newline(depth + 1)
		if len(columns) == 0 {
			w.b = append(w.b, "{}"...)
			continue
		}
		w.b = append(w.b, '{')
		for i, c := range columns {
			if i > 0 {
				w.b = append(w.b, ',')
			}
			w.newline(depth + 2)
			w.b = append(w.b, c.key...)
			value, err := wrapField(globals, tab.GetCell(row, c.col), c.field, action)
			if err != nil {
				w.err = err
				return
			}
			w.value(value, depth+2)
		}
		w.newline(depth + 1)
		w.b = append(w.b, '}')
	}
	w.newline(depth)
	w.b = append(w.b, ']')
}

// document writes the metadata and the given tables (by header type) as
// one top-level object.
func document(globals *model.Globals, tables []*model.DataTable, action string) ([]byte, error) {
	byKey := map[string]*model.DataTable{"@Tool": nil, "@Version": nil}
	for _, tab := range tables {
		byKey[tab.HeaderType] = tab
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	w := &jsonWriter{}
	w.b = append(w.b, '{')
	for i, key := range keys {
		if i > 0 {
			w.b = append(w.b, ',')
		}
		w.newline(1)
		w.marshal(key)
		w.b = append(w.b, ": "...)
		switch tab := byKey[key]; {
		case key == "@Tool" && tab == nil:
			w.marshal("github.com/Coder-is/TabForge")
		case key == "@Version" && tab == nil:
			w.marshal(globals.Version)
		default:
			w.rows(globals, tab, action, 1)
		}
	}
	w.newline(0)
	w.b = append(w.b, '}')
	if w.err != nil {
		return nil, w.err
	}
	return w.b, nil
}
