# 开发维护指南

命令均在仓库根目录执行。用户接入从[项目首页](../README.md)和[协议指南](protocol-integration.md)开始；这里说明源码职责、扩展方式和开发验证。最近一次代码整理见 [2026-10-04 重构记录](refactoring.md)。

## 按职责定位源码

| 要修改的行为 | 源码入口 |
| --- | --- |
| CLI 参数、模式选择 | [flag.go](../flag.go)、[main.go](../main.go)、[entry_v3.go](../entry_v3.go) |
| 索引、类型和数据表编译、合并 | [v3/compiler/](../v3/compiler)；流程入口 [flow.go](../v3/compiler/flow.go) |
| 字段、枚举、重复值和输出名称校验 | [v3/checker/](../v3/checker)、[gen/names.go](../v3/gen/names.go) |
| 编译后的表格模型、标签动作 | [v3/model/](../v3/model) |
| XLSX/CSV、并发加载、缓存 | [v3/helper/](../v3/helper)；加载入口 [fileloader.go](../v3/helper/fileloader.go) |
| 配置源码和数据导出 | [v3/gen/](../v3/gen)；共享模板入口 [template.go](../v3/gen/template.go) |
| 协议清单、描述与字段规则 | [contract.go](../protocol/contract.go)、[schema.go](../protocol/schema.go) |
| 协议产物与升级检查 | [generate.go](../protocol/generate.go)、[compatibility.go](../protocol/compatibility.go) |
| HTTP 路由与普通请求 | [server.go](../protocol/httptransport/server.go) |
| SSE 写入、序号与心跳 | [stream.go](../protocol/httptransport/stream.go) |
| 响应校验、错误包络 | [response.go](../protocol/httptransport/response.go) |
| 服务选项与 Observe 回调 | [server.go](../protocol/httptransport/server.go)；结果结构在 [options.go](../protocol/httptransport/options.go) |
| CORS 与 HTTPServer | [options.go](../protocol/httptransport/options.go) |
| TS 客户端、Fetch、SSE 与包络 | [runtime.ts](../sdk/typescript/runtime.ts) |
| 微信/Cocos 共用回调传输、队列与取消 | [callback_transport.ts](../sdk/typescript/callback_transport.ts) |
| TS 字段与内建类型校验、增量 UTF-8 | [schema.ts](../sdk/typescript/schema.ts)、[utf8.ts](../sdk/typescript/utf8.ts) |
| Unity 核心与引擎网络层 | [Unity 源码说明](../sdk/unity/README.md#源码职责) |
| 跨语言真实 HTTP 验证 | [internal/platformtest/](../internal/platformtest)、[HTTP 测试](../protocol/httptransport/server_test.go) |

V3 编译结果与网络协议描述分别由各自模块维护。平台适配器负责宿主网络和生命周期；消息、包络与 SSE 规则尽量复用现有核心。修改时优先使用明确的小函数、提前返回和标准库；出现实际重复后再提取共用逻辑。

## 扩展或修改导出器

1. 在 `v3/gen/` 的对应包内实现生成逻辑。单文件使用 `Generate(*model.Globals) ([]byte, error)`，目录输出使用 `Output(*model.Globals, string) error`，类型定义见 [genfunc.go](../v3/gen/genfunc.go)。
2. 在 `flag.go` 声明输出参数，并在 `entry_v3.go` 的 `v3GenList` 注册生成函数。单文件写入由 CLI 完成，目录导出器逐个调用 `helper.WriteFile`。
3. 文本模板使用 `gen.Render`，传入该语言的 `template.FuncMap`；保留各语言必要的格式化步骤。Go 源码导出仍使用现有 parser/printer 规则，修改格式化器会影响生成文件。
4. 标签动作按输出格式选择。例如普通合并 JSON 用 `nogenfield_json`，分表 JSON 用 `nogenfield_jsondir`；共用行转换时仍须传入正确动作。
5. 增加能证明行为的用例：非法输入、格式边界、目录创建和标签过滤。涉及生成格式时，用同一输入比较修改前后的产物，明确记录有意变化。

`helper.WriteFile` 自动创建父目录。多个输出并发执行，等待全部结束后汇总失败；成功文件可能已经写出。普通 V3 文件写入及整个输出集合均不保证原子性。协议生成包另有临时文件替换逻辑，详见[生产说明](production.md#协议升级)。

协议示例的生成文件由 Proto 与清单派生，修改源输入后运行 `bash examples/protocol/Make.sh`。SDK 修改后重新构建平台资产；不要只替换生成包中的某个文件。

## 基础检查

需要项目规定的 Go 版本和 Node 24+。首次安装开发依赖后：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run build:platforms
go test -race -count=1 ./...
git diff --check
```

`npm test` 执行 TS 单元测试；Go 测试另启动服务执行 Node 真实 HTTP/SSE 联调。`build:platforms` 检查示例打包与资产复制，不执行引擎编译。Go 测试使用 `-count=1` 避免复用测试缓存；关注输出中的 `SKIP`，缺少可选运行时不等于验证通过。

修改字段查找的性能路径时，可单独运行已有基准：

```bash
go test ./v3/model -run '^$' -bench BenchmarkTypeFieldLookup -benchmem
```

## 可选运行时与引擎验证

安装相应运行时后，使用路径或 PATH 中的命令名：

```bash
DOTNET_BIN=dotnet go test -race -count=1 -v ./internal/platformtest
GODOT_BIN=/absolute/path/to/godot go test -race -count=1 -v ./protocol/httptransport -run Godot
```

也可同时设置 `DOTNET_BIN` 与 `GODOT_BIN` 后执行完整 `go test -race -count=1 -v ./...`。未设置时，对应 C# / Godot 用例会跳过；Node 未安装或版本不足也会跳过其联调。

启用了系统 HTTP 代理的环境，将 `127.0.0.1`、`localhost`、`::1` 加入现有 `NO_PROXY` / `no_proxy`，让本地联调请求直达回环服务。保留原有排除项。

C++ sanitizer、Unity PlayMode、Unreal Automation、微信及 Creator 的步骤见[平台验收](platform-validation.md)。独立 C# 核心测试不编译 `UnityWebRequest`，独立 C++ 测试不编译 Unreal 插件；发布具体引擎目标时仍需运行对应工程和目标构建。

历史验收按日期记录：2026-10-03 包含浏览器与微信开发者工具实测；2026-10-04 包含重构后的核心、真实 HTTP 联调和产物一致性回归。具体结果见[验收记录](platform-validation.md#2026-10-04-重构回归)，本地记录不代表远程 CI 已通过。
