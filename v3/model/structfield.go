package model

import (
	"sort"
	"strconv"
	"strings"
)

// Nested struct fields (doc/nested-struct-design.md): a field whose type is
// another header type. Data tables spread its members over columns named
// field.member, and arrays of structs over field[n].member groups.

// IsStructKind reports whether name is a user-defined header type, so a
// field of that type holds a nested struct.
func (self *TypeTable) IsStructKind(name string) bool {
	tds := self.fieldsByObject[name]
	if len(tds) == 0 || self.enumKinds[name] {
		return false
	}
	for _, td := range tds {
		if td.Define.IsBuiltin || td.Define.Kind != TypeUsage_HeaderStruct {
			return false
		}
	}
	return true
}

// IsStructField reports whether a field holds a struct or an array of them.
func (self *TypeTable) IsStructField(tf *TypeDefine) bool {
	return tf != nil && self.IsStructKind(tf.FieldType)
}

// PathStep is one field on a column's path into nested structs. Index
// groups the columns of one element of an array of structs.
type PathStep struct {
	Field    *TypeDefine
	Index    int
	HasIndex bool
}

// StructValue is one nested struct in merged data: one cell per field of
// Type, in AllFieldByName order.
type StructValue struct {
	Type  string
	Cells []*Cell
	index int // element number from the column header, for arrays
}

// NewStructValue makes an empty struct of the given type.
func NewStructValue(types *TypeTable, typeName string) *StructValue {
	fields := types.AllFieldByName(typeName)
	v := &StructValue{Type: typeName, Cells: make([]*Cell, len(fields))}
	for i := range v.Cells {
		v.Cells[i] = &Cell{}
	}
	return v
}

// Empty reports whether no leaf of the struct holds a value.
func (self *StructValue) Empty() bool {
	for _, c := range self.Cells {
		if c.Value != "" || len(c.ValueList) > 0 || (c.Struct != nil && !c.Struct.Empty()) {
			return false
		}
		for _, e := range c.Structs {
			if !e.Empty() {
				return false
			}
		}
	}
	return true
}

// Element returns the element numbered index of an array-of-struct cell,
// adding it in index order.
func (self *Cell) Element(types *TypeTable, typeName string, index int) *StructValue {
	for _, e := range self.Structs {
		if e.index == index {
			return e
		}
	}
	e := NewStructValue(types, typeName)
	e.index = index
	self.Structs = append(self.Structs, e)
	sort.SliceStable(self.Structs, func(i, j int) bool { return self.Structs[i].index < self.Structs[j].index })
	return e
}

// DropEmptyElements removes elements whose columns were all empty, here
// and in nested structs.
func (self *Cell) DropEmptyElements() {
	if self.Struct != nil {
		for _, c := range self.Struct.Cells {
			c.DropEmptyElements()
		}
	}
	kept := self.Structs[:0]
	for _, e := range self.Structs {
		for _, c := range e.Cells {
			c.DropEmptyElements()
		}
		if !e.Empty() {
			kept = append(kept, e)
		}
	}
	self.Structs = kept
}

// VisitLeaves calls visit for every non-struct value under a field's cell,
// with the field that defines it: the cell itself for plain fields, every
// member cell of nested structs otherwise. Nil cells are skipped.
func VisitLeaves(types *TypeTable, field *TypeDefine, cell *Cell, visit func(field *TypeDefine, cell *Cell)) {
	if cell == nil {
		return
	}
	if !types.IsStructField(field) {
		visit(field, cell)
		return
	}
	members := types.AllFieldByName(field.FieldType)
	structs := cell.Structs
	if !field.IsArray() && cell.Struct != nil {
		structs = []*StructValue{cell.Struct}
	}
	for _, v := range structs {
		for i, member := range members {
			if i < len(v.Cells) {
				VisitLeaves(types, member, v.Cells[i], visit)
			}
		}
	}
}

// Key identifies the column's target: the field name, or the full path
// with element numbers (Reward.ItemID, Costs[2].Count).
func (self *HeaderField) Key() string {
	if len(self.Path) == 0 {
		if self.TypeInfo == nil {
			return ""
		}
		return self.TypeInfo.FieldName
	}
	var sb strings.Builder
	for i, step := range self.Path {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(step.Field.FieldName)
		if step.HasIndex {
			sb.WriteByte('[')
			sb.WriteString(strconv.Itoa(step.Index))
			sb.WriteByte(']')
		}
	}
	return sb.String()
}

// Top is the table field the column belongs to.
func (self *HeaderField) Top() *TypeDefine {
	if len(self.Path) > 0 {
		return self.Path[0].Field
	}
	return self.TypeInfo
}

// FieldIndex is the position of fieldName among objectType's fields, or -1.
func (self *TypeTable) FieldIndex(objectType, fieldName string) int {
	for i, td := range self.fieldsByObject[objectType] {
		if td.Define.FieldName == fieldName {
			return i
		}
	}
	return -1
}

// LeafCell returns the cell a member column fills under a table cell,
// creating the structs and array elements on the way.
func LeafCell(types *TypeTable, top *Cell, path []PathStep) *Cell {
	cell := top
	for i, step := range path[:len(path)-1] {
		var v *StructValue
		if step.Field.IsArray() {
			v = cell.Element(types, step.Field.FieldType, step.Index)
		} else {
			if cell.Struct == nil {
				cell.Struct = NewStructValue(types, step.Field.FieldType)
			}
			v = cell.Struct
		}
		cell = v.Cells[types.FieldIndex(step.Field.FieldType, path[i+1].Field.FieldName)]
	}
	return cell
}
