package pbdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ExternalMapping describes how compiled tables populate an existing message.
// Fields uses TypeDefine.FieldName as keys. An empty destination excludes a column.
type ExternalMapping struct {
	RootMessage string                  `json:"root_message"`
	Tables      map[string]TableMapping `json:"tables"`
}

type TableMapping struct {
	Field    string            `json:"field"`
	Fields   map[string]string `json:"fields,omitempty"`
	KeyField string            `json:"key_field,omitempty"`
}

type externalColumn struct {
	col    int
	source *model.TypeDefine
	path   []protoreflect.FieldDescriptor
}

type externalTable struct {
	table     *model.DataTable
	path      []protoreflect.FieldDescriptor
	message   protoreflect.MessageDescriptor
	columns   []externalColumn
	keyColumn int
}

type externalSchema struct {
	root   protoreflect.MessageDescriptor
	types  *protoregistry.Types
	tables []externalTable
}

func loadExternalSchema(globals *model.Globals) (*externalSchema, error) {
	if globals.ProtoDescriptorFile == "" || globals.ProtoMappingFile == "" {
		return nil, fmt.Errorf("existing Protobuf export requires both -proto_desc and -proto_map")
	}
	data, err := ioutil.ReadFile(globals.ProtoDescriptorFile)
	if err != nil {
		return nil, fmt.Errorf("read Proto descriptor: %v", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("decode Proto descriptor: %v", err)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return nil, fmt.Errorf("resolve Proto descriptors (compile with --include_imports): %v", err)
	}
	data, err = ioutil.ReadFile(globals.ProtoMappingFile)
	if err != nil {
		return nil, fmt.Errorf("read Proto mapping: %v", err)
	}
	var mapping ExternalMapping
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mapping); err != nil {
		return nil, fmt.Errorf("decode Proto mapping: %v", err)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return nil, fmt.Errorf("Proto mapping must contain exactly one JSON object")
	}
	if mapping.RootMessage == "" || len(mapping.Tables) == 0 {
		return nil, fmt.Errorf("Proto mapping requires root_message and at least one table")
	}
	desc, err := files.FindDescriptorByName(protoreflect.FullName(strings.TrimPrefix(mapping.RootMessage, ".")))
	if err != nil {
		return nil, fmt.Errorf("find root_message %q: %v", mapping.RootMessage, err)
	}
	root, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("root_message %q is not a message", mapping.RootMessage)
	}
	schema := &externalSchema{root: root, types: new(protoregistry.Types)}
	files.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		err = registerExternalMessages(schema.types, file.Messages())
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	var tableNames []string
	for name := range mapping.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	for _, name := range tableNames {
		tab := globals.Datas.GetDataTable(name)
		if tab == nil {
			return nil, fmt.Errorf("Proto mapping references unknown table %q", name)
		}
		binding, err := schema.bindTable(globals, tab, mapping.Tables[name])
		if err != nil {
			return nil, fmt.Errorf("table %s: %v", name, err)
		}
		for _, previous := range schema.tables {
			if pathsOverlap(previous.path, binding.path) {
				return nil, fmt.Errorf("tables %s and %s map to overlapping root fields", previous.table.HeaderType, name)
			}
		}
		schema.tables = append(schema.tables, *binding)
	}
	return schema, nil
}

func registerExternalMessages(types *protoregistry.Types, messages protoreflect.MessageDescriptors) error {
	for i := 0; i < messages.Len(); i++ {
		md := messages.Get(i)
		if err := types.RegisterMessage(dynamicpb.NewMessageType(md)); err != nil {
			return err
		}
		if err := registerExternalMessages(types, md.Messages()); err != nil {
			return err
		}
	}
	return nil
}

func resolveExternalPath(message protoreflect.MessageDescriptor, path string) ([]protoreflect.FieldDescriptor, error) {
	if path == "" {
		return nil, fmt.Errorf("destination field path is empty")
	}
	parts := strings.Split(path, ".")
	var fields []protoreflect.FieldDescriptor
	for i, name := range parts {
		fd := message.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			fd = message.Fields().ByJSONName(name)
		}
		if fd == nil {
			return nil, fmt.Errorf("unknown Proto field %q in %s (path %q)", name, message.FullName(), path)
		}
		fields = append(fields, fd)
		if i < len(parts)-1 {
			if fd.Message() == nil || fd.IsList() || fd.IsMap() {
				return nil, fmt.Errorf("path %q traverses a non-singular message field %s", path, fd.FullName())
			}
			message = fd.Message()
		}
	}
	return fields, nil
}

func pathsOverlap(a, b []protoreflect.FieldDescriptor) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i].FullName() != b[i].FullName() {
			return false
		}
	}
	return true
}

func (schema *externalSchema) bindTable(globals *model.Globals, tab *model.DataTable, mapping TableMapping) (*externalTable, error) {
	path, err := resolveExternalPath(schema.root, mapping.Field)
	if err != nil {
		return nil, err
	}
	fd := path[len(path)-1]
	message := fd.Message()
	if fd.IsMap() {
		message = fd.MapValue().Message()
	}
	if message == nil {
		return nil, fmt.Errorf("destination %s must contain messages", fd.FullName())
	}
	binding := &externalTable{table: tab, path: path, message: message, keyColumn: -1}
	if fd.IsMap() {
		if mapping.KeyField == "" {
			return nil, fmt.Errorf("map destination %s requires key_field", fd.FullName())
		}
		for col, header := range tab.Headers {
			if header.TypeInfo != nil && header.TypeInfo.FieldName == mapping.KeyField {
				if header.TypeInfo.IsArray() {
					return nil, fmt.Errorf("key_field %q cannot be an array", mapping.KeyField)
				}
				binding.keyColumn = col
			}
		}
		if binding.keyColumn < 0 {
			return nil, fmt.Errorf("unknown key_field %q", mapping.KeyField)
		}
	} else if mapping.KeyField != "" {
		return nil, fmt.Errorf("key_field is only valid for map destinations")
	}
	known := make(map[string]bool)
	for col, header := range tab.Headers {
		tf := header.TypeInfo
		if tf == nil {
			continue
		}
		known[tf.FieldName] = true
		if globals.CanDoAction(model.ActionNoGenFieldPbBinary, tf) {
			continue
		}
		destination, explicit := mapping.Fields[tf.FieldName]
		if !explicit {
			destination = tf.FieldName
		}
		if destination == "" {
			continue
		}
		path, err := resolveExternalPath(message, destination)
		if err != nil {
			return nil, fmt.Errorf("column %s: %v", tf.FieldName, err)
		}
		leaf := path[len(path)-1]
		if tf.IsArray() && (!leaf.IsList() || leaf.IsMap()) {
			return nil, fmt.Errorf("array column %s requires a repeated Proto field; maps use a JSON object cell", tf.FieldName)
		}
		for _, previous := range binding.columns {
			if pathsOverlap(previous.path, path) {
				return nil, fmt.Errorf("columns %s and %s have overlapping destinations", previous.source.FieldName, tf.FieldName)
			}
		}
		binding.columns = append(binding.columns, externalColumn{col: col, source: tf, path: path})
	}
	var unknown []string
	for name := range mapping.Fields {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("mapping references unknown source columns: %s", strings.Join(unknown, ", "))
	}
	if len(binding.columns) == 0 {
		return nil, fmt.Errorf("no columns are mapped")
	}
	return binding, nil
}

func (schema *externalSchema) export(globals *model.Globals, tableName string) (*dynamicpb.Message, error) {
	root := dynamicpb.NewMessage(schema.root)
	for _, table := range schema.tables {
		if tableName != "" && table.table.HeaderType != tableName {
			continue
		}
		if len(table.table.Rows) <= 1 {
			continue
		}
		parent := root.ProtoReflect()
		for _, fd := range table.path[:len(table.path)-1] {
			if err := checkExternalOneof(parent, fd); err != nil {
				return nil, err
			}
			parent = parent.Mutable(fd).Message()
		}
		fd := table.path[len(table.path)-1]
		if err := checkExternalOneof(parent, fd); err != nil {
			return nil, err
		}
		rows := 0
		for row := 1; row < len(table.table.Rows); row++ {
			message, err := schema.exportRow(globals, &table, row)
			if err != nil {
				return nil, err
			}
			rows++
			switch {
			case fd.IsMap():
				cell := table.table.GetCell(row, table.keyColumn)
				if cell == nil || cell.Value == "" {
					return nil, fmt.Errorf("table %s row %d: map key is missing", table.table.HeaderType, row+1)
				}
				key, err := externalMapKey(cell.Value, fd.MapKey())
				if err != nil {
					return nil, fmt.Errorf("map key %s: %v", cell.String(), err)
				}
				values := parent.Mutable(fd).Map()
				if values.Has(key) {
					return nil, fmt.Errorf("duplicate map key %s", cell.String())
				}
				values.Set(key, protoreflect.ValueOfMessage(message))
			case fd.IsList():
				parent.Mutable(fd).List().Append(protoreflect.ValueOfMessage(message))
			default:
				if rows > 1 {
					return nil, fmt.Errorf("table %s has multiple rows for singular message %s", table.table.HeaderType, fd.FullName())
				}
				parent.Set(fd, protoreflect.ValueOfMessage(message))
			}
		}
	}
	return root, nil
}

func checkExternalOneof(message protoreflect.Message, field protoreflect.FieldDescriptor) error {
	if oneof := field.ContainingOneof(); oneof != nil {
		if previous := message.WhichOneof(oneof); previous != nil && previous.Number() != field.Number() {
			return fmt.Errorf("conflicting oneof fields %s and %s", previous.FullName(), field.FullName())
		}
	}
	return nil
}

// GenerateJSON exports ProtoJSON compatible with the supplied schema.
func GenerateJSON(globals *model.Globals) ([]byte, error) {
	schema, err := loadExternalSchema(globals)
	if err != nil {
		return nil, err
	}
	message, err := schema.export(globals, "")
	if err != nil {
		return nil, err
	}
	return protojson.MarshalOptions{Indent: "  ", Resolver: schema.types}.Marshal(message)
}

func outputExternal(globals *model.Globals, dir string) error {
	schema, err := loadExternalSchema(globals)
	if err != nil {
		return err
	}
	outputs := make(map[string][]byte)
	for _, table := range schema.tables {
		message, err := schema.export(globals, table.table.HeaderType)
		if err != nil {
			return err
		}
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
		if err != nil {
			return err
		}
		outputs[table.table.HeaderType] = data
	}
	for _, table := range schema.tables {
		if err := helper.WriteFile(filepath.Join(dir, table.table.HeaderType+".pbb"), outputs[table.table.HeaderType]); err != nil {
			return err
		}
	}
	return nil
}
