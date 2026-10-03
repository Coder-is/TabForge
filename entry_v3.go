package main

import (
	"flag"
	"fmt"
	"github.com/Coder-is/TabForge/build"
	"github.com/Coder-is/TabForge/v3/compiler"
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/gen/bindata"
	"github.com/Coder-is/TabForge/v3/gen/cssrc"
	"github.com/Coder-is/TabForge/v3/gen/gosrc"
	"github.com/Coder-is/TabForge/v3/gen/javasrc"
	"github.com/Coder-is/TabForge/v3/gen/jsondata"
	"github.com/Coder-is/TabForge/v3/gen/jsontype"
	"github.com/Coder-is/TabForge/v3/gen/luasrc"
	"github.com/Coder-is/TabForge/v3/gen/pbdata"
	"github.com/Coder-is/TabForge/v3/gen/pbsrc"
	"github.com/Coder-is/TabForge/v3/helper"
	"github.com/Coder-is/TabForge/v3/model"
	"github.com/Coder-is/TabForge/v3/report"
	"os"
	"strings"
	"sync"
)

type V3GenEntry struct {
	name          string
	genSingleFile gen.GenSingleFile
	genCustom     gen.GenCustom
	param         *string
}

// v3新增
var (
	paramIndexFile       = flag.String("index", "", "input multi-files configs")
	paramTagAction       = flag.String("tag_action", "", "do action by tag selected target, format: action1:tag1+tag2|action2:tag1+tag3")
	paramProtoDescriptor = flag.String("proto_desc", "", "existing Protobuf FileDescriptorSet, compiled with --include_imports")
	paramProtoMapping    = flag.String("proto_map", "", "JSON mapping from tables/columns to existing Protobuf messages")
	paramProtoJSONOut    = flag.String("pbjson_out", "", "output ProtoJSON using -proto_desc and -proto_map")

	v3GenList = []V3GenEntry{
		{name: "gosrc", genSingleFile: gosrc.Generate, param: paramGoOut},
		{name: "jsondata", genSingleFile: jsondata.Generate, param: paramJsonOut},
		{name: "jsontype", genSingleFile: jsontype.Generate, param: paramJsonTypeOut},
		{name: "luasrc", genSingleFile: luasrc.Generate, param: paramLuaOut},
		{name: "cssrc", genSingleFile: cssrc.Generate, param: paramCSharpOut},
		{name: "bindata", genSingleFile: bindata.Generate, param: paramBinaryOut},
		{name: "javasrc", genSingleFile: javasrc.Generate, param: paramJavaOut},
		{name: "pbsrc", genSingleFile: pbsrc.Generate, param: paramProtoOut},
		{name: "pbdata", genSingleFile: pbdata.Generate, param: paramPbBinaryOut},
		{name: "pbjson", genSingleFile: pbdata.GenerateJSON, param: paramProtoJSONOut},

		{name: "jsondir", genCustom: jsondata.Output, param: paramJsonDir},
		{name: "luadir", genCustom: luasrc.Output, param: paramLuaDir},
		{name: "binarydir", genCustom: bindata.Output, param: paramBinaryDir},
		{name: "pbdatadir", genCustom: pbdata.Output, param: paramPbBinaryDir},
	}
)

func genFile(globals *model.Globals, entry V3GenEntry) error {
	filename := *entry.param

	if entry.genSingleFile != nil {
		if data, err := entry.genSingleFile(globals); err != nil {
			return err
		} else {

			report.Log.Infof("  [%s] %s", entry.name, filename)

			err = helper.WriteFile(filename, data)

			if err != nil {
				return err
			}
		}
	}

	if entry.genCustom != nil {
		if err := entry.genCustom(globals, *entry.param); err != nil {
			return err
		} else {
			report.Log.Infof("  [%s] %s", entry.name, filename)
		}
	}

	return nil
}

func GenFileByList(globals *model.Globals) error {
	return genFiles(globals, v3GenList)
}

// Wait for every exporter, including when another exporter fails.
func genFiles(globals *model.Globals, entries []V3GenEntry) error {
	errors := make([]error, len(entries))
	var tasks sync.WaitGroup
	for i, entry := range entries {

		if *entry.param == "" {
			continue
		}

		tasks.Add(1)
		go func(i int, entry V3GenEntry) {
			defer tasks.Done()
			if err := genFile(globals, entry); err != nil {
				errors[i] = fmt.Errorf("%s (%s): %v", entry.name, *entry.param, err)
			}
		}(i, entry)
	}
	tasks.Wait()

	var messages []string
	for _, err := range errors {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) != 0 {
		return fmt.Errorf("generation failed: %s", strings.Join(messages, "; "))
	}
	return nil
}

func V3Entry() {
	globals := model.NewGlobals()
	globals.Version = build.Version
	globals.ParaLoading = *paramPara
	if *paramUseCache {
		globals.CacheDir = *paramCacheDir
	}
	globals.IndexFile = *paramIndexFile
	globals.PackageName = *paramPackageName
	globals.CombineStructName = *paramCombineStructName
	globals.GenBinary = *paramBinaryOut != "" || *paramBinaryDir != ""
	globals.ProtoDescriptorFile = *paramProtoDescriptor
	globals.ProtoMappingFile = *paramProtoMapping

	idxloader := helper.NewFileLoader(true, globals.CacheDir)
	globals.IndexGetter = idxloader

	var err error
	if err = validateProtoOptions(); err != nil {
		goto Exit
	}
	if *paramTagAction != "" {
		globals.TagActions, err = model.ParseTagAction(*paramTagAction)
		if err != nil {
			goto Exit
		}
	}

	err = compiler.Compile(globals)

	if err != nil {
		goto Exit
	}

	report.Log.Debugln("Generate files...")
	err = GenFileByList(globals)
	if err != nil {
		goto Exit
	}

	return
Exit:
	report.Log.Errorln(err)
	os.Exit(1)
}

func validateProtoOptions() error {
	if (*paramProtoDescriptor == "") != (*paramProtoMapping == "") {
		return fmt.Errorf("-proto_desc and -proto_map must be provided together")
	}
	if *paramProtoDescriptor != "" && *paramProtoOut != "" {
		return fmt.Errorf("-proto_out generates a new schema; use protoc with your existing schema when using -proto_desc")
	}
	if *paramProtoJSONOut != "" && *paramProtoDescriptor == "" {
		return fmt.Errorf("-pbjson_out requires -proto_desc and -proto_map")
	}
	return nil
}
