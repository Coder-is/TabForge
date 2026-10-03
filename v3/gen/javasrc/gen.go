package javasrc

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/davyxu/protoplus/codegen"
)

func Generate(globals *model.Globals) (data []byte, err error) {
	if err := gen.ValidateNames(globals, "java"); err != nil {
		return nil, err
	}

	cg := codegen.NewCodeGen("javasrc").
		RegisterTemplateFunc(codegen.UsefulFunc).
		RegisterTemplateFunc(gen.UsefulFunc).
		RegisterTemplateFunc(UsefulFunc)

	err = cg.ParseTemplate(templateText, globals).Error()
	if err != nil {
		return
	}

	err = cg.WriteBytes(&data).Error()

	return
}
