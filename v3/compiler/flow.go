package compiler

import (
	"github.com/Coder-is/TabForge/v3/checker"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
)

func Compile(globals *model.Globals) (ret error) {

	defer func() {

		switch err := recover().(type) {
		case *report.TableError:
			ret = err
		case nil:
		default:
			panic(err)
		}

	}()

	model.InitBuiltinTypes(globals.Types)

	report.Log.Debugf("Loading Index file: '%s'... ", globals.IndexFile)
	err := LoadIndexTable(globals, globals.IndexFile)

	if err != nil {
		return err
	}

	// 项目导出提供可预加载的Getter: 一次并发解析索引列出的全部表文件
	if preloader, ok := globals.TableGetter.(helper.Preloader); ok {
		names := make([]string, 0, len(globals.IndexList))
		for _, pragma := range globals.IndexList {
			names = append(names, pragma.TableFileName)
		}
		preloader.Preload(names)
	}

	// 测试时, 这个Getter会被提前设置为MemFile, 普通导出时, 这个Getter为空
	if globals.TableGetter == nil {
		tabLoader := helper.NewFileLoader(!globals.ParaLoading, globals.CacheDir)

		if globals.ParaLoading {
			for _, pragma := range globals.IndexList {
				tabLoader.AddFile(pragma.TableFileName)
			}

			tabLoader.Commit()
		}

		globals.TableGetter = tabLoader
	}

	var kvList, dataList model.DataTableList

	// 加载多种表
	err = loadVariantTables(globals, &kvList, &dataList)

	if err != nil {
		return err
	}

	report.Log.Debugln("Checking types...")
	checker.CheckType(globals.Types)
	checker.PreCheck(&dataList)

	if kvList.Count() > 0 {
		report.Log.Debugln("Merge key-value tables...")

		// 合并所有的KV表行
		var mergedKV model.DataTableList
		MergeData(&kvList, &mergedKV, globals.Types)

		// 完整KV表转置为普通数据表
		for _, tab := range mergedKV.AllTables() {

			dataList.AddDataTable(transposeKVtoData(globals.Types, tab))
		}
	}

	// KV转置后, 再检查一次
	checker.CheckType(globals.Types)

	report.Log.Debugln("Merge data tables...")

	// 合并所有的数据表
	MergeData(&dataList, &globals.Datas, globals.Types)

	checker.PostCheck(globals)

	return nil
}
