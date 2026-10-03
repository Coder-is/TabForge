package main

import "flag"

// 标准参数
var (
	// 显示版本号
	paramVersion = flag.Bool("version", false, "Show version")

	// 工作模式
	paramMode = flag.String("mode", "v3", "export mode (only v3 is supported)")

	// 并发加载表格
	paramPara = flag.Bool("para", false, "load tables in parallel by your cpu count")

	// 缓存目录和开关
	paramCacheDir = flag.String("cachedir", "./.tabtoycache", "cache file output dir")
	paramUseCache = flag.Bool("usecache", false, "use cache file enhanced exporting speed")
)

var (
	// 导出代码中包/命名空间名称
	paramPackageName = flag.String("package", "", "override the package name in table @Types")

	// 导出代码中类型名
	paramCombineStructName = flag.String("combinename", "Table", "combine struct name, code struct flagstr")

	// 单文件导出
	paramProtoOut    = flag.String("proto_out", "", "output protobuf define (*.proto)")
	paramPbBinaryOut = flag.String("pbbin_out", "", "output protobuf binary (*.pbb)")
	paramLuaOut      = flag.String("lua_out", "", "output lua code (*.lua)")
	paramJsonOut     = flag.String("json_out", "", "output json format (*.json)")
	paramJsonTypeOut = flag.String("jsontype_out", "", "output json type (*.json)")
	paramCSharpOut   = flag.String("csharp_out", "", "output c# class and deserialize code (*.cs)")
	paramGoOut       = flag.String("go_out", "", "output golang code (*.go)")
	paramBinaryOut   = flag.String("binary_out", "", "output binary format(*.bin)")
	paramJavaOut     = flag.String("java_out", "", "output java code (*.java)")

	// 按表多文件导出
	paramJsonDir     = flag.String("json_dir", "", "output json format (*.json) to dir")
	paramLuaDir      = flag.String("lua_dir", "", "output lua format (*.lua) to dir")
	paramBinaryDir   = flag.String("binary_dir", "", "output binary format (*.bin) to dir")
	paramPbBinaryDir = flag.String("pbbin_dir", "", "output binary format (*.pbb) to dir")
)
