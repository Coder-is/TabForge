package gosrc

import (
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"

	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func Generate(globals *model.Globals) ([]byte, error) {
	if err := gen.ValidateNames(globals, "go"); err != nil {
		return nil, err
	}
	data, err := gen.Render("gosrc", templateText, globals, UsefulFunc)
	if err != nil {
		return nil, err
	}
	files := token.NewFileSet()
	source, err := parser.ParseFile(files, "", data, parser.ParseComments)
	if err != nil {
		report.Log.Infoln(string(data))
		return nil, err
	}
	var output bytes.Buffer
	// Preserve the established generated source formatting.
	config := printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}
	if err := config.Fprint(&output, files, source); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
