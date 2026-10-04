package gen

import (
	"bytes"
	"text/template"
)

// Render executes an exporter template with shared and language-specific helpers.
func Render(name, source string, data interface{}, functions template.FuncMap) ([]byte, error) {
	tmpl, err := template.New(name).Funcs(UsefulFunc).Funcs(functions).Parse(source)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
