package javasrc

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
)

func Generate(globals *model.Globals) ([]byte, error) {
	if err := gen.ValidateNames(globals, "java"); err != nil {
		return nil, err
	}
	return gen.Render("javasrc", templateText, globals, UsefulFunc)
}
