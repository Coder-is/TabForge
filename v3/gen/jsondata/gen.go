package jsondata

import (
	"path/filepath"

	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

// Both JSON exporters share row conversion, but retain their own tag actions.

func Output(globals *model.Globals, directory string) error {
	for _, tab := range globals.Datas.AllTables() {
		data, err := document(globals, []*model.DataTable{tab}, model.ActionNoGenFieldJsonDir)
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
	return document(globals, globals.Datas.AllTables(), model.ActionNoGenFieldJson)
}
