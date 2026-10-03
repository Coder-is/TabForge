package pbdata

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type exampleFileGetter struct{ dir string }

func (getter exampleFileGetter) GetFile(name string) (helper.TableFile, error) {
	return helper.NewFileLoader(true, "").GetFile(filepath.Join(getter.dir, name))
}

func externalFixture(t *testing.T) (*model.Globals, ExternalMapping, string) {
	t.Helper()
	dir, err := ioutil.TempDir("", "tabtoy-external-proto-")
	if err != nil {
		t.Fatal(err)
	}
	example, err := filepath.Abs(filepath.Join("..", "..", "example", "existingproto"))
	if err != nil {
		t.Fatal(err)
	}
	globals := model.NewGlobals()
	globals.IndexFile = "Index.csv"
	globals.IndexGetter = exampleFileGetter{example}
	globals.TableGetter = exampleFileGetter{example}
	if err := compiler.Compile(globals); err != nil {
		t.Fatal(err)
	}
	globals.ProtoDescriptorFile = filepath.Join(example, "schema.pb")
	globals.ProtoMappingFile = filepath.Join(dir, "mapping.json")
	data, err := ioutil.ReadFile(filepath.Join(example, "mapping.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mapping ExternalMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		t.Fatal(err)
	}
	writeExternalMapping(t, globals, mapping)
	return globals, mapping, dir
}

func writeExternalMapping(t *testing.T, globals *model.Globals, mapping ExternalMapping) {
	t.Helper()
	data, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(globals.ProtoMappingFile, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func field(message protoreflect.Message, name string) protoreflect.FieldDescriptor {
	return message.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func decodeExternal(t *testing.T, globals *model.Globals, data []byte) *dynamicpb.Message {
	t.Helper()
	schema, err := loadExternalSchema(globals)
	if err != nil {
		t.Fatal(err)
	}
	message := dynamicpb.NewMessage(schema.root)
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestExistingProtoExport(t *testing.T) {
	globals, _, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	data, err := Generate(globals)
	if err != nil {
		t.Fatal(err)
	}
	// The supplied root's field number is 10, not a generated sequential number.
	number, _, n := protowire.ConsumeTag(data)
	if n < 0 || number != 10 {
		t.Fatalf("did not use the existing field numbers: %d", number)
	}
	root := decodeExternal(t, globals, data).ProtoReflect()
	items := root.Get(field(root, "items")).List()
	if items.Len() != 2 {
		t.Fatalf("got %d items", items.Len())
	}
	first := items.Get(0).Message()
	if first.Get(field(first, "id")).Int() != 1001 || first.Get(field(first, "name")).String() != "新手剑" {
		t.Fatal("scalar columns were not mapped")
	}
	reward := first.Get(field(first, "reward")).Message()
	if reward.Get(field(reward, "item_id")).Int() != 2001 || reward.Get(field(reward, "count")).Uint() != ^uint64(0) {
		t.Fatal("imported nested message or uint64 precision was lost")
	}
	cost := first.Get(field(first, "costs")).List().Get(0).Message()
	if cost.Get(field(cost, "item_id")).Int() != 2002 || cost.Get(field(cost, "count")).Uint() != 3 {
		t.Fatal("repeated message ProtoJSON was not parsed")
	}
	levels := first.Get(field(first, "levels")).List()
	if levels.Len() != 3 || levels.Get(2).Int() != 10 {
		t.Fatal("split scalar array was not exported")
	}
	stats := first.Get(field(first, "stats")).Map()
	if stats.Get(protoreflect.ValueOfString("attack").MapKey()).Int() != 12 {
		t.Fatal("map ProtoJSON was not exported")
	}
	if first.Get(field(first, "rarity")).Enum() != 1 || string(first.Get(field(first, "icon_hash")).Bytes()) != "abc" {
		t.Fatal("enum or bytes were not exported")
	}
	second := items.Get(1).Message()
	if !first.Has(field(first, "unlock_level")) || first.Get(field(first, "unlock_level")).Int() != 0 || second.Has(field(second, "unlock_level")) {
		t.Fatal("explicit zero and blank optional field presence were not preserved")
	}
	if first.WhichOneof(first.Descriptor().Oneofs().ByName("effect")).Name() != "text_effect" || second.WhichOneof(second.Descriptor().Oneofs().ByName("effect")).Name() != "power_effect" {
		t.Fatal("oneof values were not preserved")
	}
	settings := root.Get(field(root, "settings")).Message()
	if settings.Get(field(settings, "max_level")).Int() != 100 {
		t.Fatal("singular table was not exported")
	}
	jsonData, err := GenerateJSON(globals)
	if err != nil {
		t.Fatal(err)
	}
	fromJSON := dynamicpb.NewMessage(root.Descriptor())
	if err := protojson.Unmarshal(jsonData, fromJSON); err != nil || !proto.Equal(root.Interface(), fromJSON) {
		t.Fatalf("ProtoJSON did not round trip: %v", err)
	}
	output := filepath.Join(dir, "new", "by-table")
	if err := Output(globals, output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Item", "Settings"} {
		data, err := ioutil.ReadFile(filepath.Join(output, name+".pbb"))
		if err != nil {
			t.Fatal(err)
		}
		partial := decodeExternal(t, globals, data).ProtoReflect()
		if name == "Item" && (partial.Get(field(partial, "items")).List().Len() != 2 || partial.Has(field(partial, "settings"))) {
			t.Fatal("per-table export contains the wrong tables")
		}
		if name == "Settings" && (!partial.Has(field(partial, "settings")) || partial.Get(field(partial, "items")).List().Len() != 0) {
			t.Fatal("singular per-table export contains the wrong tables")
		}
	}
}

func TestExistingProtoMapDestination(t *testing.T) {
	globals, mapping, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	item := mapping.Tables["Item"]
	item.Field, item.KeyField = "by_id", "ID"
	mapping.Tables["Item"] = item
	delete(mapping.Tables, "Settings")
	writeExternalMapping(t, globals, mapping)
	data, err := Generate(globals)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeExternal(t, globals, data).ProtoReflect()
	items := root.Get(field(root, "by_id")).Map()
	if items.Len() != 2 || root.Has(field(root, "settings")) {
		t.Fatal("map destination or table selection failed")
	}
	itemMessage := items.Get(protoreflect.ValueOfInt32(1001).MapKey()).Message()
	if itemMessage.Get(field(itemMessage, "name")).String() != "新手剑" {
		t.Fatal("map key points to wrong message")
	}
	globals.Datas.GetDataTable("Item").GetValueByName(2, "ID").Value = "1001"
	if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), "duplicate map key") {
		t.Fatalf("duplicate map keys were not rejected: %v", err)
	}
}

func TestExistingProtoSchemaOrderDoesNotChangeWireNumbers(t *testing.T) {
	globals, _, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	before, err := Generate(globals)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ioutil.ReadFile(globals.ProtoDescriptorFile)
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	for _, file := range set.File {
		for _, message := range file.MessageType {
			for i, j := 0, len(message.Field)-1; i < j; i, j = i+1, j-1 {
				message.Field[i], message.Field[j] = message.Field[j], message.Field[i]
			}
		}
	}
	data, err = proto.Marshal(&set)
	if err != nil {
		t.Fatal(err)
	}
	globals.ProtoDescriptorFile = filepath.Join(dir, "reordered.pb")
	if err := ioutil.WriteFile(globals.ProtoDescriptorFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Generate(globals)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("descriptor field order changed the output: %v", err)
	}
}

func TestExistingProtoMappingValidation(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*ExternalMapping)
	}{
		{"unknown root", "find root_message", func(m *ExternalMapping) { m.RootMessage = "missing.Type" }},
		{"enum root", "not a message", func(m *ExternalMapping) { m.RootMessage = "example.common.Rarity" }},
		{"unknown table", "unknown table", func(m *ExternalMapping) { m.Tables["Missing"] = m.Tables["Item"] }},
		{"unknown column", "unknown source columns", func(m *ExternalMapping) { m.Tables["Item"].Fields["Missing"] = "id" }},
		{"unknown target", "unknown Proto field", func(m *ExternalMapping) { m.Tables["Item"].Fields["Name"] = "missing" }},
		{"overlapping columns", "overlapping destinations", func(m *ExternalMapping) { m.Tables["Item"].Fields["Costs"] = "reward" }},
		{"repeated traversal", "non-singular message", func(m *ExternalMapping) { m.Tables["Item"].Fields["Name"] = "costs.item_id" }},
		{"array mismatch", "array column", func(m *ExternalMapping) { m.Tables["Item"].Fields["Levels"] = "reward.count" }},
		{"missing map key", "requires key_field", func(m *ExternalMapping) { v := m.Tables["Item"]; v.Field = "by_id"; m.Tables["Item"] = v }},
		{"unknown map key", "unknown key_field", func(m *ExternalMapping) {
			v := m.Tables["Item"]
			v.Field, v.KeyField = "by_id", "Missing"
			m.Tables["Item"] = v
		}},
		{"key without map", "only valid for map", func(m *ExternalMapping) { v := m.Tables["Item"]; v.KeyField = "ID"; m.Tables["Item"] = v }},
		{"scalar root field", "must contain messages", func(m *ExternalMapping) { v := m.Tables["Item"]; v.Field = "settings.max_level"; m.Tables["Item"] = v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globals, mapping, dir := externalFixture(t)
			defer os.RemoveAll(dir)
			tc.change(&mapping)
			writeExternalMapping(t, globals, mapping)
			if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestExistingProtoValueValidation(t *testing.T) {
	cases := []struct{ column, value, want string }{
		{"ID", "2147483648", "value out of range"},
		{"RewardCount", "18446744073709551616", "value out of range"},
		{"Costs", "{", "valid ProtoJSON"},
		{"Costs", `[{"unknown":1}]`, "unknown field"},
		{"Stats", `{"attack":"bad"}`, "invalid value"},
		{"Rarity", "MISSING", "unknown enum value"},
		{"Rarity", "123", "unknown enum value"},
		{"IconHash", "not base64!", "invalid value"},
		{"PowerEffect", "99", "oneof"},
	}
	for _, tc := range cases {
		t.Run(tc.column+"="+tc.value, func(t *testing.T) {
			globals, _, dir := externalFixture(t)
			defer os.RemoveAll(dir)
			globals.Datas.GetDataTable("Item").GetValueByName(1, tc.column).Value = tc.value
			if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Items.csv") {
				t.Fatalf("want %q with cell context, got %v", tc.want, err)
			}
		})
	}
}

func TestExistingProtoInvalidDescriptors(t *testing.T) {
	globals, _, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	data, err := ioutil.ReadFile(globals.ProtoDescriptorFile)
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		t.Fatal(err)
	}
	// Drop imported common.proto to simulate omitting --include_imports.
	set.File = set.File[len(set.File)-1:]
	data, err = proto.Marshal(&set)
	if err != nil {
		t.Fatal(err)
	}
	globals.ProtoDescriptorFile = filepath.Join(dir, "missing-import.pb")
	if err := ioutil.WriteFile(globals.ProtoDescriptorFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), "--include_imports") {
		t.Fatalf("missing import was not reported: %v", err)
	}
	globals.ProtoMappingFile = ""
	if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("missing option was not reported: %v", err)
	}
}

func TestExistingProtoDefaultsAndArrayFormats(t *testing.T) {
	globals, mapping, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	tab := globals.Datas.GetDataTable("Item")
	// Same-name columns require no per-column mapping, and explicit empty paths skip.
	tab.HeaderByName("Name").TypeInfo.FieldName = "name"
	delete(mapping.Tables["Item"].Fields, "Name")
	mapping.Tables["Item"].Fields["IconHash"] = ""
	writeExternalMapping(t, globals, mapping)
	// Repeated scalar cells can use a whole JSON array instead of a splitter.
	tab.HeaderByName("Levels").TypeInfo.ArraySplitter = ""
	tab.GetValueByName(1, "Levels").Value = "[2,4,8]"
	tab.GetValueByName(2, "Levels").Value = "[]"
	// Repeated messages can use multiple JSON cells split by the source definition.
	tab.HeaderByName("Costs").TypeInfo.ArraySplitter = "|"
	tab.GetValueByName(1, "Costs").ValueList = []string{`{"itemId":1,"count":"2"}`, `{"itemId":3,"count":"4"}`}
	tab.GetValueByName(2, "Costs").ValueList = nil
	// Local spreadsheet enum aliases are resolved before using the Proto enum.
	globals.Types.AddField(&model.TypeDefine{Kind: model.TypeUsage_Enum, ObjectType: "LocalRarity", FieldName: "High", Name: "史诗", Value: "2"}, nil, 0)
	tab.HeaderByName("Rarity").TypeInfo.FieldType = "LocalRarity"
	tab.GetValueByName(1, "Rarity").Value = "史诗"
	data, err := Generate(globals)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeExternal(t, globals, data).ProtoReflect()
	first := root.Get(field(root, "items")).List().Get(0).Message()
	if first.Get(field(first, "name")).String() != "新手剑" || len(first.Get(field(first, "icon_hash")).Bytes()) != 0 {
		t.Fatal("same-name mapping or column exclusion failed")
	}
	if first.Get(field(first, "levels")).List().Get(2).Int() != 8 || first.Get(field(first, "costs")).List().Len() != 2 || first.Get(field(first, "rarity")).Enum() != 2 {
		t.Fatal("alternate array formats or enum aliases failed")
	}
}

func TestExistingProtoRejectsMultipleRowsForSingularMessage(t *testing.T) {
	globals, _, dir := externalFixture(t)
	defer os.RemoveAll(dir)
	tab := globals.Datas.GetDataTable("Settings")
	row := tab.AddRow()
	tab.MustGetCell(row, 0).Value = "200"
	if _, err := Generate(globals); err == nil || !strings.Contains(err.Error(), "multiple rows for singular") {
		t.Fatalf("singular table accepted multiple rows: %v", err)
	}
}

func TestExistingProtoRejectsInvalidMappingJSON(t *testing.T) {
	for _, text := range []string{
		`{"root_message":"example.config.Tables","tables":{},"typo":true}`,
		`{} {}`,
		`null`,
	} {
		t.Run(text, func(t *testing.T) {
			globals, _, dir := externalFixture(t)
			defer os.RemoveAll(dir)
			if err := ioutil.WriteFile(globals.ProtoMappingFile, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Generate(globals); err == nil {
				t.Fatal("invalid mapping JSON was accepted")
			}
		})
	}
}
