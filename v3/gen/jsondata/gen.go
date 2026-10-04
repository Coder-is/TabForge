package jsondata

import (
	"encoding/json"
	"path/filepath"

	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

func metadata(globals *model.Globals) map[string]interface{} {
	return map[string]interface{}{
		"@Tool":    "github.com/Coder-is/TabForge",
		"@Version": globals.Version,
	}
}

// Both JSON exporters share row conversion, but retain their own tag actions.
func tableData(globals *model.Globals, tab *model.DataTable, action string) []map[string]interface{} {
	headers := globals.Types.AllFieldByName(tab.OriginalHeaderType)
	var rows []map[string]interface{}
	for row := 1; row < len(tab.Rows); row++ {
		values := make(map[string]interface{})
		for col, field := range headers {
			if globals.CanDoAction(action, field) {
				continue
			}
			values[field.FieldName] = wrapValue(globals, tab.GetCell(row, col), field)
		}
		rows = append(rows, values)
	}
	return rows
}

func Output(globals *model.Globals, directory string) error {
	for _, tab := range globals.Datas.AllTables() {
		fileData := metadata(globals)
		fileData[tab.HeaderType] = tableData(globals, tab, model.ActionNoGenFieldJsonDir)
		data, err := json.MarshalIndent(fileData, "", "\t")
		if err != nil {
			return err
		}
		if err := helper.WriteFile(filepath.Join(directory, tab.HeaderType+".json"), data); err != nil {
			return err
		}
	}
	return nil
}

func Generate(globals *model.Globals) ([]byte, error) {
	fileData := metadata(globals)
	for _, tab := range globals.Datas.AllTables() {
		fileData[tab.HeaderType] = tableData(globals, tab, model.ActionNoGenFieldJson)
	}
	return json.MarshalIndent(fileData, "", "\t")
}
