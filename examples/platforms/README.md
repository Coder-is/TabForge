# 多平台验收工程

这些工程使用同一套示例协议，验证各端普通 JSON/SSE、Unicode、uint64、事件顺序、终止、取消、超时及错误边界。服务提供固定测试数据，不连接真实模型；坏帧路由仅用于验收。

## 准备与网页验收

从仓库根目录运行，需要 Go 和 Node 24+：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run build:platforms
go run ./examples/platforms/server
```

浏览器打开 `http://127.0.0.1:18083/browser.html`，点击“运行验收”；Fetch 与 XHR 各 12 项，共 24 项。默认同源。服务默认仅监听本机，`-listen` 可指定地址，`-contract` 指定清单，`-assets` 指定资产目录。

平台客户端连接 **18083**，不要连接基础协议演示的 18082。`/_platform/fault/*` 包括跳号、hash 错误、类型错误、提前 EOF、超大帧和非法 UTF-8 等故意异常；不部署到生产路由。手机的 `127.0.0.1` 指向手机自身，应按目标平台配置可访问地址、HTTPS 与合法域名。

## 目录与构建产物

| 目录 / 文件 | 内容 |
| --- | --- |
| [server/main.go](server/main.go) | Go 验收服务入口 |
| [smoke.ts](smoke.ts) | TS 共用验收用例 |
| [browser.html](browser.html) | 网页按钮与结果页 |
| [wechat/](wechat) | 微信开发者工具项目与自动化运行器 |
| [cocos/](cocos) | Creator 项目与可挂载验收组件；需手动创建场景 |
| [unity/](unity) | Unity UPM/Test Framework 工程与 PlayMode 测试 |
| [unreal/](unreal) | Unreal 工程，加载 Runtime 插件的 Automation 测试 |
| [reports/2026-10-03.json](reports/2026-10-03.json) | 已执行环境的历史验收记录 |

`build:platforms` 生成网页 `dist/browser.js`、微信 `vendor/tabforge.js`、Cocos JavaScript/声明文件，并复制 Unity Resources 的 runtime 元数据与 Unreal 源码插件。这些构建输出被 Git 忽略，首次克隆或修改 SDK/Proto 后需要重新生成。脚本不执行引擎编译，也不产生移动发布包。

## 编辑器与自动化

微信导入 `wechat/` 项目后运行页面验收；自动化 CLI/AppID/基础库要求见 [详细步骤](../../doc/platform-validation.md)。当前真实 `wx.request` 验证在开发者工具中完成，真机和小游戏构建尚未完成。

安装并激活 Unity 编辑器后，在保持 Go 验收服务运行时执行：

```bash
UNITY_EDITOR=/absolute/path/to/Unity node examples/platforms/run-unity.mjs
```

Unreal 先用目标引擎的 RunUAT BuildPlugin 生成插件包，再运行：

```bash
UNREAL_EDITOR=/absolute/path/to/UnrealEditor \
UNREAL_PLUGIN_BUILD=/absolute/path/to/packaged-plugin node examples/platforms/run-unreal.mjs
```

两个运行器设置服务/协议路径、清理旧报告并检查本次结果，缺环境或没有通过记录不能算成功。Cocos 用 Creator 3.8 打开工程，创建场景、挂载 `TabForgeSmoke`，选择 fetch/xhr/wechat 并查看控制台报告。

本机没有 Unity、Unreal、Cocos Creator 编辑器，目前只完成独立核心或 Web 网络后端测试。准备步骤、报告路径、目标构建与发布前待测项以 [平台验收矩阵](../../doc/platform-validation.md) 为准；Godot 原生接入和独立 headless 验证见 [Godot SDK](../../sdk/godot/README.md)。

2026-10-04 的重构回归包含 TS 25 项单元测试、Go/Node/C# 真实 HTTP 联调、Godot headless 与 C++ sanitizer，见[当次结果](../../doc/platform-validation.md#2026-10-04-重构回归)。网页 Fetch/XHR 仍为另一套 24 项用例；浏览器及微信开发者工具实测保留在 2026-10-03 历史报告。源码与本地验证环境见[开发维护指南](../../doc/development.md)。
