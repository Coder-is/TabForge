package luasrc

import (
	"path/filepath"

	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
)

func Generate(globals *model.Globals) ([]byte, error) {
	if err := gen.ValidateNames(globals, "lua"); err != nil {
		return nil, err
	}
	return gen.Render("luasrc", templateText_luasrc, globals, UsefulFunc)
}

func Output(globals *model.Globals, directory string) error {
	if err := gen.ValidateNames(globals, "lua"); err != nil {
		return err
	}
	typeData, err := gen.Render("luatype", templateText_luatype, globals, UsefulFunc)
	if err != nil {
		return err
	}
	if err := helper.WriteFile(filepath.Join(directory, "_"+globals.CombineStructName+"Type.lua"), typeData); err != nil {
		return err
	}
	for _, tab := range globals.Datas.AllTables() {
		context := struct {
			Tab *model.DataTable
			G   *model.Globals
		}{Tab: tab, G: globals}
		data, err := gen.Render("luadir", templateText_luadir, context, UsefulFunc)
		if err != nil {
			return err
		}
		if err := helper.WriteFile(filepath.Join(directory, tab.HeaderType+".lua"), data); err != nil {
			return err
		}
	}
	return nil
}
