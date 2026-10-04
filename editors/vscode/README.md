# TabForge VS Code 插件

安装便携发行包中的 `.vsix`，打开包含 `tabforge.json` 的项目，运行命令 **TabForge: 导出项目**。插件优先使用 VSIX 内置工具，再尝试项目 `Tools/TabForge`；指定 toolPath 时优先采用该路径。不调用 Go、Node CLI 或 protoc。

第二版新增 **TabForge: 校验项目**（不替换产物）、**TabForge: 查看最近报告**、tabforge.json 配置补全和 Problems 诊断。Proto 错误可跳转源码；Excel 错误显示工作簿、工作表和单元格。成功后清除对应项目的旧诊断。

设置 `tabforge.exportOnSave=true` 后，Proto、CSV、Excel 与 JSON 变更会触发延迟导出。Excel 可以在外部应用编辑；生成目录不会触发再次导出。失败信息保留在 TabForge 输出面板。多人项目使用同一份配置；多根工作区可选择对应项目。

源码目录没有预编译二进制，可设置绝对 `tabforge.toolPath` 后用 VS Code 扩展开发宿主运行。使用 `go run ./cmd/package` 构建四个平台的便携包和带工具的 VSIX。支持 VS Code 桌面，不支持浏览器 VS Code；远程宿主需提供该宿主可运行的工具。Unity/Cocos/Godot 独立插件见 [第二版工作流](../../doc/editor-workflow.md)。

测试覆盖项目查找、带空格路径和真实工具进程。VSIX 包需在目标平台 VS Code 中安装验收；这与命令行导出测试分开记录。
