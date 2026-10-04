package model

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTypeTableLookupCompatibility(t *testing.T) {
	var table TypeTable
	first := &TypeDefine{Kind: TypeUsage_Enum, ObjectType: "Actor", Name: "相同别名", FieldName: "First", Value: "1"}
	second := &TypeDefine{Kind: TypeUsage_Enum, ObjectType: "Actor", Name: "相同别名", FieldName: "Second", Value: "2"}
	builtin := &TypeDefine{Kind: TypeUsage_HeaderStruct, ObjectType: "Builtin", FieldName: "ID", IsBuiltin: true}
	other := &TypeDefine{Kind: TypeUsage_HeaderStruct, ObjectType: "Data", Name: "标识", FieldName: "ID"}
	for _, field := range []*TypeDefine{first, builtin, second, other} {
		table.AddField(field, nil, 0)
	}
	if table.FieldByName("Actor", "相同别名") != second || table.FieldByName("Actor", "First") != first {
		t.Fatal("field alias precedence changed")
	}
	if table.GetEnumValue("Actor", "相同别名").Define != first || table.GetEnumValue("Actor", "Second").Define != second {
		t.Fatal("enum alias precedence changed")
	}
	if table.ResolveEnumValue("Actor", "unknown") != "1" || table.ResolveEnumValue("Missing", "unknown") != "" {
		t.Fatal("enum default behavior changed")
	}
	if !table.IsEnumKind("Actor") || table.IsEnumKind("Data") || !table.ObjectExists("Data") || table.ObjectExists("Missing") {
		t.Fatal("object/kind lookup failed")
	}
	if !reflect.DeepEqual(table.AllFieldByName("Actor"), []*TypeDefine{first, second}) {
		t.Fatal("field ordering changed")
	}
	if !reflect.DeepEqual(table.AllFields(), []*TypeDefine{first, second, other}) {
		t.Fatal("builtin filtering/order changed")
	}
	if !reflect.DeepEqual(table.rawStructNames(false), []string{"Data"}) || !reflect.DeepEqual(table.rawStructNames(true), []string{"Builtin", "Data"}) || !reflect.DeepEqual(table.rawEnumNames(false), []string{"Actor"}) {
		t.Fatal("type name filtering/order changed")
	}
	// Returned slices must not let callers overwrite the stored field order.
	fields := table.AllFieldByName("Actor")
	fields[0] = other
	if table.AllFieldByName("Actor")[0] != first {
		t.Fatal("caller modified the internal field list")
	}
	table.AddField(&TypeDefine{Kind: TypeUsage_HeaderStruct, ObjectType: "Data", FieldName: "Name"}, nil, 0)
	if len(table.AllFieldByName("Data")) != 2 || table.FieldByName("Data", "Name") == nil {
		t.Fatal("new fields were not indexed")
	}
}

var benchmarkField *TypeDefine

func BenchmarkTypeFieldLookup(b *testing.B) {
	table := NewSymbolTable()
	for i := 0; i < 100; i++ {
		for j := 0; j < 30; j++ {
			table.AddField(&TypeDefine{ObjectType: fmt.Sprintf("Table%d", i), Name: fmt.Sprintf("列%d", j), FieldName: fmt.Sprintf("Field%d", j)}, nil, 0)
		}
	}
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkField = table.FieldByName("Table99", "Field29")
		}
	})
	b.Run("original_scan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, td := range table.fields {
				field := td.Define
				if field.ObjectType == "Table99" && (field.Name == "Field29" || field.FieldName == "Field29") {
					benchmarkField = field
				}
			}
		}
	})
}
