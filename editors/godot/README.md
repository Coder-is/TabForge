# Godot Editor 插件

目标 Godot 4.5.1+ 桌面。解压对应平台 `tabforge-godot-*.zip` 到游戏项目，使插件位于 `addons/tabforge/`，在 Project Settings → Plugins 启用 TabForge。

底部 TabForge 面板支持创建示例、校验、导出并导入、导入已有 Generated 包、取消。Project → Tools 也有导出入口。编译器随插件提供，无需 Go 或 protoc。源文件默认位于 `TabForge/`，也识别根目录的 `tabforge.json`。

生成目录 `res://tabforge_generated/` 包含数据加载器、结构脚本和 JSON 文件。GDScript 使用完整 Proto 消息名；64 位整数保持字符串。具体读取代码见 [第二版工作流](../../doc/editor-workflow.md)。打包游戏时，在 Export 的非资源文件过滤器包含 JSON（例如 `*.json`）。

已使用真实 Godot headless 验证数据读取、启动编译器和 EditorPlugin 注册；有窗口的按钮交互与 Windows 原生运行尚未验证。生成脚本的 `.uid` 在重新导入后保留；本版提供结构描述与字典校验，不生成独立的 GDScript 消息类。
