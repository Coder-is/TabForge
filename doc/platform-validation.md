# 四端接入与平台验收

四端专用网络代码已加入仓库。支持声明以实际测试环境为准：代码存在、独立核心通过、编辑器运行、目标设备运行是不同阶段。

首次业务接入见 [前后端指南](protocol-integration.md)，验收目录与构建产物见 [工程入口](../examples/platforms/README.md)。Godot 原生 headless 验证另见 [Godot SDK](../sdk/godot/README.md)。

## 2026-10-03 验收矩阵

| 平台 | 接入实现 | 已执行 | 未执行 |
| --- | --- | --- | --- |
| 网页 / Cocos Web 网络后端 | Fetch 字节流 / 渐进 XHR | macOS 上 Codex 内置 Chromium 154，两个后端各 12 项真实 Go HTTP 验收 | Safari/Firefox、Creator 场景内运行 |
| 微信小程序 | wx.request ArrayBuffer + enableChunked | 开发者工具 2.01.2510260、基础库 3.11.2，12 项真实网络验收 | Android/iOS 微信真机、微信小游戏发布构建 |
| Unity 2022.3 / Unity 6 原生目标 | UPM、UnityWebRequest、DownloadHandlerScript、主线程回调 | .NET SDK 8.0.425 + Newtonsoft 13.0.2，核心编译、每字节切片、严格 JSON/类型和 Go HTTP/SSE 联调 | Unity 编辑器编译/PlayMode、IL2CPP、移动构建；WebGL SSE 需 JS 桥 |
| Unreal 5.5+ 目标 | Runtime 插件、FHttpModule、FArchive 接收流、游戏线程 ticker | macOS clang++ C++17 独立 SSE/JSON 核心，ASan/UBSan | 插件引擎构建、Automation 原生 HTTP、移动平台与其他版本 |
| Cocos Creator 3.8 目标 | Fetch/XHR/微信三种显式后端 | 同一适配器浏览器 24 项、Node 真实 HTTP 回归 | Creator 编辑器、JSB、Android/iOS；原生 XHR 增量能力需逐平台验证 |

本机没有 Unity、Unreal、Cocos Creator 编辑器，因此后三者不能标记为已实测兼容。微信开发者工具的 iPhone 模拟器也不代表真机已通过。本次记录见 [机器可读结果](../examples/platforms/reports/2026-10-03.json)。

统一用例包括普通 JSON、中文/emoji、uint64 最大值、SSE 序号和终止、发送前类型检查、服务器尚未结束时收事件后取消、提前退出迭代、全程 deadline、服务器缺少结束事件、跳号/hash/字段错误、无结束事件 EOF、超大帧。Node 回归另检查取消/退出/超时让服务端 context 真正结束；单元测试补非法 UTF-8、分片、队列背压、不支持实时分块等边界。

## 共用服务与验收资产

在仓库根目录执行：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run build:platforms
go run ./examples/platforms/server
```

服务默认仅监听 127.0.0.1:18083；`/_platform/fault/*` 故意返回坏数据，**只用于验收，不部署到生产路由**。生成 runtime.json、协议 hash、事件规则和 SDK 必须来自同一版本。

浏览器打开 `http://127.0.0.1:18083/browser.html`，点击“运行验收”，应显示 24/24。默认同源，不需要扩大 CORS 白名单。

## 微信开发者工具

构建资产后导入 `examples/platforms/wechat`，使用测试 AppID，点击页面按钮。项目关闭合法域名检查是为了本地测试；正式包恢复域名检查并配置 HTTPS 地址。真机测试需合法测试 AppID、域名与测试账号，在真机上重新验收。

自动化使用腾讯 `miniprogram-automator` 0.12.1，单独装在临时工具目录，**不加入 SDK 的依赖树**。该官方测试工具依赖较旧，只用于本地验收，不随产品分发。

```bash
npm install --prefix /tmp/tabforge-platform-tools --save-exact miniprogram-automator@0.12.1
# macOS；其他系统指定对应 cli 路径。
/Applications/wechatwebdevtools.app/Contents/MacOS/cli auto \
  --project "$PWD/examples/platforms/wechat" --auto-port 9420 --trust-project
# 等待开发者工具完成项目编译后连接：
WECHAT_WS=ws://127.0.0.1:9420 \
TABFORGE_AUTOMATOR=/tmp/tabforge-platform-tools/node_modules/miniprogram-automator \
TABFORGE_REPORT=/tmp/tabforge-wechat-report.json node examples/platforms/wechat/run.mjs
```

首次加载可能尚未提供基础库版本，官方 automator.launch 会报错；用 CLI 完成启动后 connect 可避免这一时序问题。热重载可能丢失正在等待的自动化响应；运行器 60 秒截止，等编译结束再连接，不把空报告当成功。测试期间不要重新打包 vendor。`TABFORGE_TEST_URL` 可覆盖服务地址。

## Unity 编辑器

验收工程 `examples/platforms/unity` 已配置本地 UPM 包及 Test Framework，build:platforms 放入 Resources 元数据。安装并激活目标编辑器后：

```bash
UNITY_EDITOR=/absolute/path/to/Unity node examples/platforms/run-unity.mjs
```

脚本启动 PlayMode 测试，自动设置 TABFORGE_TEST_URL / TABFORGE_RUNTIME_JSON，检查 XML 内实际通过的测试，报告位于 unity/TestResults.xml。直接用 Test Runner 时需要设置这两个环境变量；缺服务地址会显示 ignored，不能记为通过。Tests/Engine/NetworkTests.cs 覆盖原生网络及组件关闭释放。

CoreTests.csproj 是另外的 .NET 核心测试，仅编译协议核心，不编译 UnityWebRequest 适配层：

```bash
DOTNET_BIN=dotnet go test -race -count=1 -v ./internal/platformtest
```

## Unreal 引擎

build:platforms 将源码插件复制到验收项目。先在目标引擎机器执行官方 RunUAT 的 BuildPlugin（macOS 示例），输出目录选在仓库之外：

```bash
/absolute/path/to/Engine/Build/BatchFiles/RunUAT.sh BuildPlugin \
  -Plugin="$PWD/sdk/unreal/TabForgeProtocol.uplugin" \
  -Package=/tmp/tabforge-unreal-plugin -TargetPlatforms=Mac
UNREAL_EDITOR=/absolute/path/to/UnrealEditor \
UNREAL_PLUGIN_BUILD=/tmp/tabforge-unreal-plugin node examples/platforms/run-unreal.mjs
```

Windows 使用 RunUAT.bat 并选择 Win64；Linux 选择 Linux。运行器复制构建产物，运行 TabForge.Protocol.Parser / NativeHTTP，检查 Saved/TabForgeReport/index.json 的两个 Success 状态。测试覆盖普通/流式、实时取消、全程超时、坏帧、CancelAll。为了单独验证帧限制，超大帧用例将测试队列放大至 4 MiB；生产默认队列仍为 1 MiB。

独立核心检查不需要引擎：

```bash
clang++ -std=c++17 -Wall -Wextra -Werror -fsanitize=address,undefined \
  -I sdk/unreal/Source/TabForgeProtocol/Public sdk/native/tests/sse_test.cpp -o /tmp/tabforge-sse-test
/tmp/tabforge-sse-test
```

## Cocos Creator

用 Creator 3.8 打开 `examples/platforms/cocos`。创建空场景、Node，挂载 assets/TabForgeSmoke.ts，设置 baseURL / backend 并运行。Web 使用 fetch 或 xhr；原生选择 xhr；微信构建选 wechat。控制台输出 TABFORGE_PLATFORM_REPORT，需逐项全通过。这个样例包含可挂载组件，场景需在编辑器内创建，当前没有经过 Creator 导入或编译。

手机的 127.0.0.1 指向手机自身。需要远程验收时服务显式监听局域网地址，同时按目标 Web origin 配置 CORS；正式环境使用 HTTPS。Native XHR 若只能完成后返回全文，SSE 应报 unsupported_transport，这种结果表示尚不满足流式接入要求，不应绕过用例。

## 自动回归与发布门槛

现有 [CI 配置](../.github/workflows/test.yml) 在 Linux/macOS/Windows 检查 Go、TS、平台示例构建；native-cores 任务在 Linux/macOS 执行 C++ sanitizer 与 C# 核心真实 HTTP 联调，Godot headless 任务另行执行。没有引擎许可证或真机 runner，CI 不执行 Unity/Unreal/Creator 编辑器或真机测试。配置已随 `8b0dbb2` 推送到 main，实际执行结果查看 [GitHub Actions](https://github.com/Coder-is/TabForge/actions)；本文记录的是本地验收，不声称远程任务已通过。

发布特定平台前，至少在其编辑器和一个实际目标构建运行上述用例，补齐 HTTPS、认证失败、代理缓冲、长流/慢消费者、场景退出和移动前后台切换。v1 不含 WebSocket、自动重连/回放、其他厂商小程序和模型供应商适配。
