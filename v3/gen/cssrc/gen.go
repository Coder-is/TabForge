package cssrc

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
)

func Generate(globals *model.Globals) ([]byte, error) {
	if err := gen.ValidateNames(globals, "csharp"); err != nil {
		return nil, err
	}
	return gen.Render("cssrc", templateText, globals, UsefulFunc)
}
