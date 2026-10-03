package model

import (
	"encoding/json"
	"fmt"
)

type TypeData struct {
	Define *TypeDefine
	Tab    *DataTable // 类型引用的表
	Row    int        // 类型引用的原始数据(DataTable)中的行
}

type TypeTable struct {
	fields         []*TypeData
	fieldsByObject map[string][]*TypeData
	fieldsByName   map[typeFieldKey]*TypeDefine
	enumValues     map[typeFieldKey]*TypeData
	enumKinds      map[string]bool
}

type typeFieldKey struct {
	objectType string
	name       string
}

func (self *TypeTable) ToJSON() []byte {

	data, _ := json.MarshalIndent(self.AllFields(), "", "\t")

	return data
}

func (self *TypeTable) Print() {

	fmt.Println(string(self.ToJSON()))
}

// refData，类型表对应源表的位置信息
// 插入后请保持 ObjectType、Name、FieldName 和 Kind 不变，以保证索引有效。
func (self *TypeTable) AddField(tf *TypeDefine, data *DataTable, row int) {

	if self.FieldByName(tf.ObjectType, tf.FieldName) != nil {
		panic("Duplicate table field: " + tf.FieldName)
	}

	if self.fieldsByObject == nil {
		self.fieldsByObject = make(map[string][]*TypeData)
		self.fieldsByName = make(map[typeFieldKey]*TypeDefine)
		self.enumValues = make(map[typeFieldKey]*TypeData)
		self.enumKinds = make(map[string]bool)
	}
	td := &TypeData{
		Tab:    data,
		Define: tf,
		Row:    row,
	}
	self.fields = append(self.fields, td)
	self.fieldsByObject[tf.ObjectType] = append(self.fieldsByObject[tf.ObjectType], td)
	for _, name := range []string{tf.Name, tf.FieldName} {
		key := typeFieldKey{tf.ObjectType, name}
		// Field lookup uses the last matching alias; enum lookup uses the first.
		self.fieldsByName[key] = tf
		if self.enumValues[key] == nil {
			self.enumValues[key] = td
		}
	}
	if tf.Kind == TypeUsage_Enum {
		self.enumKinds[tf.ObjectType] = true
	}
}

func (self *TypeTable) Raw() []*TypeData {
	return self.fields
}

func (self *TypeTable) AllFields() (ret []*TypeDefine) {

	for _, td := range self.fields {
		if !td.Define.IsBuiltin {
			ret = append(ret, td.Define)
		}
	}

	return
}

// 类型是枚举
func (self *TypeTable) IsEnumKind(objectType string) bool {

	return self.enumKinds[objectType]
}

func (self *TypeTable) ResolveEnum(objectType, value string) *TypeData {

	t := self.GetEnumValue(objectType, value)

	if t != nil {
		return t
	}

	enumFields := self.getEnumFields(objectType)

	if len(enumFields) == 0 {
		return nil
	}

	// 默认取第一个
	return enumFields[0]
}

func (self *TypeTable) GetEnumValue(objectType, value string) *TypeData {
	return self.enumValues[typeFieldKey{objectType, value}]
}

// 匹配枚举值
func (self *TypeTable) ResolveEnumValue(objectType, value string) string {

	t := self.ResolveEnum(objectType, value)
	if t == nil {
		return ""
	}

	return t.Define.Value
}

func (self *TypeTable) getEnumFields(objectType string) (ret []*TypeData) {

	return self.fieldsByObject[objectType]
}

func (self *TypeTable) EnumNames() (ret []string) {

	return self.rawEnumNames(BuiltinSymbolsVisible)
}

func (self *TypeTable) StructNames() (ret []string) {

	return self.rawStructNames(BuiltinSymbolsVisible)
}

// 获取所有的结构体名
func (self *TypeTable) rawStructNames(all bool) (ret []string) {

	return self.namesByKind(TypeUsage_HeaderStruct, all)
}

// 获取所有的枚举名
func (self *TypeTable) rawEnumNames(all bool) (ret []string) {

	return self.namesByKind(TypeUsage_Enum, all)
}

func (self *TypeTable) namesByKind(kind TypeUsage, all bool) (ret []string) {
	seen := make(map[string]bool)
	for _, td := range self.fields {
		tf := td.Define
		if tf.Kind == kind && (all || !tf.IsBuiltin) && !seen[tf.ObjectType] {
			ret = append(ret, tf.ObjectType)
			seen[tf.ObjectType] = true
		}
	}
	return
}

// 对象的所有字段
func (self *TypeTable) AllFieldByName(objectType string) (ret []*TypeDefine) {

	for _, td := range self.fieldsByObject[objectType] {
		ret = append(ret, td.Define)
	}

	return
}

// 数据表中表头对应类型表
func (self *TypeTable) FieldByName(objectType, name string) (ret *TypeDefine) {

	return self.fieldsByName[typeFieldKey{objectType, name}]
}

func (self *TypeTable) ObjectExists(objectType string) bool {

	return len(self.fieldsByObject[objectType]) > 0
}

func NewSymbolTable() *TypeTable {
	return new(TypeTable)
}

var BuiltinSymbolsVisible bool
