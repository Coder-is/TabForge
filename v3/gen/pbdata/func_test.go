package pbdata

import (
	"testing"

	"github.com/Coder-is/TabForge/v3/model"
)

// 空枚举值与其他导出器一致, 取第一个枚举值
func TestEmptyEnumUsesFirstMember(t *testing.T) {
	globals := model.NewGlobals()
	globals.Types.AddField(&model.TypeDefine{Kind: model.TypeUsage_Enum, ObjectType: "Actor", Name: "首个", FieldName: "First", Value: "3"}, nil, 0)
	globals.Types.AddField(&model.TypeDefine{Kind: model.TypeUsage_Enum, ObjectType: "Actor", FieldName: "NoAlias", Value: "5"}, nil, 0)
	field := &model.TypeDefine{FieldType: "Actor"}
	if got := tableValue2PbValue(globals, "", field).Enum(); got != 3 {
		t.Fatalf("empty enum = %d", got)
	}
}
