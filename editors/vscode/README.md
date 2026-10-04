# TabForge VS Code 插件

安装便携发行包中的 `.vsix`，打开包含 `tabforge.json` 的项目，运行命令 **TabForge: 导出项目**。插件优先使用项目 `Tools/TabForge` 的工具，否则使用 VSIX 内置的对应平台工具；不调用 Go、Node CLI 或 protoc。

设置 `tabforge.exportOnSave=true` 后，Proto、CSV、Excel 与 JSON 变更会触发延迟导出。Excel 可以在外部应用编辑；生成目录不会触发再次导出。失败信息保留在 TabForge 输出面板。多人项目使用同一份配置；多根工作区可选择对应项目。

源码目录没有预编译二进制，可设置绝对 `tabforge.toolPath` 后用 VS Code 扩展开发宿主运行。使用 `go run ./cmd/package` 构建四个平台的便携包和带工具的 VSIX。第一版支持 VS Code 桌面，不支持远程扩展宿主、浏览器 VS Code 或其他引擎的编辑器面板。

测试覆盖项目查找、带空格路径和真实工具进程。VSIX 包需在目标平台 VS Code 中安装验收；这与命令行导出测试分开记录。
