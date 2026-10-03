package compiler

import (
	"github.com/Coder-is/TabForge/v3/checker"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func LoadTypeTable(typeTab *model.TypeTable, indexGetter helper.FileGetter, fileName string) error {

	tabs, err := LoadDataTable(indexGetter, fileName, "TypeDefine", "TypeDefine", typeTab)

	if err != nil {
		return err
	}

	for _, tab := range tabs {

		for row := 1; row < len(tab.Rows); row++ {

			var objtype model.TypeDefine

			if !ParseRow(&objtype, tab, row, typeTab) {
				continue
			}

			if objtype.Kind == model.TypeUsage_None {
				report.ReportError("UnknownTypeKind", objtype.ObjectType, objtype.FieldName)
			}

			checker.CheckFieldDefinition(typeTab, &objtype, tab, row, "DuplicateTypeFieldName")

			typeTab.AddField(&objtype, tab, row)
		}

	}

	return nil
}
