package javasrc

import "github.com/Coder-is/TabForge/v3/model"

func init() {
	// IsGenField leaves out fields (and their indices) tagged for model.ActionNoGenFieldJava.
	UsefulFunc["IsGenField"] = func(globals *model.Globals, tf *model.TypeDefine) bool {
		return !globals.CanDoAction(model.ActionNoGenFieldJava, tf)
	}
}
