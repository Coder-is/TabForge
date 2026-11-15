package gosrc

import "github.com/Coder-is/TabForge/v3/model"

func init() {
	// IsGenField leaves out fields (and their indices) tagged for model.ActionNoGenFieldGo.
	UsefulFunc["IsGenField"] = func(globals *model.Globals, tf *model.TypeDefine) bool {
		return !globals.CanDoAction(model.ActionNoGenFieldGo, tf)
	}
}
