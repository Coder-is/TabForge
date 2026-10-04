# Cocos Creator 插件

目标 Creator 3.8。解压对应平台 `tabforge-cocos-*.zip`，将 `tabforge/` 放进游戏项目 `extensions/`，在扩展管理器启用。扩展直接使用 JavaScript 并包含编译器，无需 npm install、Go 或 protoc。

TabForge → 项目面板可创建示例、校验、导出并导入、导入已有协议包和取消。源文件位于 `TabForge/`；也支持游戏根目录直接放 `tabforge.json`。日志及报告显示失败文件、工作表和单元格。

生成目录 `assets/resources/tabforge/` 包含 TS 类型、校验器和 ProtoJSON 数据。TS 引用没有 `.ts` 后缀，可由 Creator 编译；重新导入保留仍存在的 `.meta`。`resources.load('tabforge/data/tables', JsonAsset, ...)` 读取数据。完整代码见 [第二版工作流](../../doc/editor-workflow.md)。

Node 进程入口、TS 编译与独立数据读取已验证；Creator 内部安装、面板和资产刷新仍需原生验收。导入的数据包需由第二版重新导出以携带数据根类型清单。
