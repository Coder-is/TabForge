# Unity Editor 插件

目标 Unity 2022.3+。解压对应平台 `tabforge-unity-*.zip`，通过 Package Manager → Add package from disk 选择 `com.tabforge.editor/package.json`。离线环境需要项目已有 `com.unity.nuget.newtonsoft-json` 3.2.2；UPM 依赖由 Unity 安装。

Tools → TabForge → 项目导出：创建完整示例、校验、导出并导入、导入已有 Generated 包、取消和查看报告。源文件默认在游戏项目 `TabForge/` 下，也可在窗口选择外部源项目。插件自带当前平台工具，无需 Go、Node、protoc。

生成目录为 `Assets/TabForgeGenerated/`，只放工具生成的文件。运行时 C# DTO、校验器和 JSON 位于此目录；重新导入保留仍存在的资产 `.meta`。使用方式见 [第二版工作流](../../doc/editor-workflow.md)。

C# DTO 是 ProtoJSON 类型：64 位整数和 bytes 为字符串，缺失值保持 null；不提供 Protobuf 二进制 API。加载前检查 oneof、枚举、map 和 well-known types。C# 实际编译及数据读取已测试，Unity Editor 安装和窗口交互仍需原生验收。
