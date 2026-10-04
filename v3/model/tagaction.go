package model

import (
	"fmt"
	"strings"
)

const (
	ActionNoGenFieldJson     = "nogenfield_json"
	ActionNoGenFieldJsonDir  = "nogenfield_jsondir"
	ActionNoGenFieldBinary   = "nogenfield_binary"
	ActionNoGenFieldPbBinary = "nogenfield_pbbin"
	ActionNoGennFieldLua     = "nogenfield_lua"
	ActionNoGennFieldCsharp  = "nogenfield_csharp"
	ActionNoGenTable         = "nogentab"
)

// 用tag选中目标, 做action
type TagAction struct {
	Verb string
	Tags []string
}

// action1:tag1+tag2|action2:tag1+tag3
func ParseTagAction(script string) (ret []TagAction, err error) {

	for _, as := range strings.Split(script, "|") {
		actionPairs := strings.Split(as, ":")

		var ta TagAction

		switch len(actionPairs) {
		case 2:
			ta.Verb = actionPairs[0]
			ta.Tags = strings.Split(actionPairs[1], "+")
			ret = append(ret, ta)
		default:
			err = fmt.Errorf("invalid action format")
			return
		}

	}

	return
}

func (self *Globals) CanDoAction(action string, obj interface{}) bool {
	if header, ok := obj.(*HeaderField); ok {
		if header.TypeInfo == nil {
			return false
		}
		obj = header.TypeInfo
	}
	tagged, ok := obj.(interface{ ContainTag(string) bool })
	if !ok {
		return false
	}
	for _, selection := range self.TagActions {
		if selection.Verb != action {
			continue
		}
		for _, tag := range selection.Tags {
			if tagged.ContainTag(tag) {
				return true
			}
		}
	}
	return false
}
