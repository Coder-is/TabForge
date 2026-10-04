# 协议模块生产部署与验收

加固覆盖 HTTP ProtoJSON/SSE、Go 服务端和各客户端；已加入 Unity、Unreal、Cocos 与微信专用适配，实际验证边界见 [平台验收](platform-validation.md)。已执行的核心和网络测试提供传输边界的回归依据；Unity/Unreal/Creator 编辑器与移动真机尚未完成验收，不能据此标记所有目标平台已达到生产标准。业务容量、认证策略、模型供应商和实际目标构建仍由接入项目验证。首次接入见 [前后端指南](protocol-integration.md)。

## 固定协议身份

生成包现在包含 contract.json、schema.pb、types.ts、PROTOCOL.md、wire_schema.json、protocol.gd 和 runtime.json。schema hash 对清单与描述文件计算 SHA-256，排除描述文件的路径和源注释；包括语言选项在内的其他描述内容仍参与身份计算。

Go 服务端默认要求 X-Protocol-Version 与 X-Protocol-Schema 一致，否则 409。所有响应包络增加 schemaHash；各客户端检查包络与字段规则，拒绝类型错误、溢出、未知字段和 oneof 冲突。Hash 用于发现部署差异，不是鉴权或签名。

升级时要同时部署客户端生成文件和服务端。对于未发送 schema hash 的早期客户端，临时兼容可设 RequireSchemaHash=false，但仍会拒绝显式填写错误 hash 的请求；不能用它替代明确的版本发布策略。新增 runtime.json 等输出文件本身不改变 hash，身份变化由清单与描述内容决定。

TS 初始化示例（导入路径以 examples/protocol/client.ts 为基准）：

```typescript
import { FetchTransport } from "../../sdk/typescript/runtime.ts";
import { protocolVersion, schemaHash, wireSchema } from "./generated/types.ts";
const baseURL = "https://api.example.com";
const transport = new FetchTransport(baseURL, protocolVersion, globalThis.fetch, {
  schemaHash, wireSchema,
  maxResponseBytes: 1048576,
  maxFrameChars: 1048576
});
```

生成规则描述 ProtoJSON 的输出形态：64 位整数必须为十进制字符串，默认字段可省略，非有限浮点数为标准字符串，bytes 为有填充的标准 Base64。Timestamp、Duration 等按输出规则检查。Any 的类型必须存在于描述包中。Go 输入仍使用标准 ProtoJSON 解码器，同时额外拒绝重复 JSON 成员与过深输入。此 SDK 验证不声称代替官方全部 ProtoJSON conformance 测试。

## HTTP 服务

使用 httptransport.HTTPServer(addr, handler)，而不是裸 http.ListenAndServe：默认请求头超时 5 秒、空闲连接超时 60 秒、请求头限制 32 KiB。协议 handler 设置每个请求的读写 deadline；全局 WriteTimeout 保持未设置，避免把长 SSE 流截断。

每个请求/普通响应/事件帧默认限制为 1 MiB，可用 MaxBodyBytes、MaxResponseBytes 调整。超限请求返回 413/request_too_large。Go 输出再按目标描述文件检查，避免处理函数链接到同名但不同结构的消息。普通与流式处理函数的 panic 转为不暴露内部详情的 internal 错误。

事件限制包含 SSE 标头和完整包络，校验失败不消耗序号；超限的业务错误会转换为简短 internal 错误。MaxResponseBytes 必须容纳协议包络，否则可能只能返回 HTTP 状态或关闭流，无法发送有效的错误正文。

SSE 默认每 15 秒发送注释心跳，不占事件序号；HeartbeatInterval=0 可关闭。写入/flush 失败会取消处理 context。并发 emit 被串行化；终止事件后拒绝继续发事件。业务处理和供应商 SDK 必须遵守 context，工具不能强制终止一个忽略取消的业务 goroutine。达到写 deadline 或连接已断开时也无法保证送达最后一条错误事件。

Observe 回调提供请求 ID、接口、HTTP 状态、字节数、耗时、传输错误码和结束事件名。它不包含 token 或正文；回调必须快速、非阻塞，并由应用自行接入日志/指标系统。接口注册可以并发，但 options、contract 与 Observe 应在开始服务前配置，运行中保持不变。

跨域网页使用 WithCORS(handler, explicitOrigins)。它只允许精确的来源白名单及协议请求头，不默认放行 *；CORS 不承担鉴权。bearer 接口仍需应用安装 Authorize 回调。演示清单 auth=none 仅供本地演示。

示例服务已加入 SIGINT/SIGTERM 下的优雅停止；Shutdown 最多等待 10 秒，超时后关闭连接。实际应用应把相同生命周期接入自己的服务管理。

## 客户端限制

以下为默认值，1 MiB = 1,048,576。JSON 正文与 SSE 单帧分别计数，流式实现通常不缓存全程正文。SDK 参数见各自 README/源代码。

| 实现 | 普通响应 / SSE 帧上限 | 其他边界 |
| --- | --- | --- |
| Go 服务端 | 请求、响应、完整 SSE 帧各 1 MiB 字节 | 每接口全程 deadline；心跳 15 秒 |
| TS Fetch | 正文 1 MiB 字节；帧 1 MiB UTF-16 字符 | 使用可读字节流按消费进度读取 |
| 微信 / Cocos 回调后端 | 正文与响应头前缓存各 1 MiB 字节；帧 1 MiB UTF-16 字符 | 队列 256 个事件 / 1 MiB UTF-8 data 字节，超限报 backpressure |
| Cocos XHR | 同回调后端 | 累计 responseText 另限 4 MiB UTF-16 字符；由平台解码 UTF-8 |
| Unity | 正文 1 MiB 字节；帧 1 MiB UTF-16 字符 | 最多 16 个并发请求 |
| Unreal | 正文、帧各 1 MiB 字节 | 待处理队列 1 MiB / 128 块，最多 16 个并发请求 |
| Godot | 正文、帧各 1 MiB 字节 | 原生 HTTPClient；Web SSE 尚未实现 |

UTF-16 字符数与 UTF-8 字节数不同，中文和 emoji 会占不同空间。调整限制时同时考虑服务端完整包络开销、客户端计数单位和消息峰值；有界队列不会替代业务容量测试。XHR 无法暂停累计正文增长，也不能提供原始字节级非法 UTF-8 检查。

## 协议升级

```bash
go run . -protocol=new/contract.json -protocol_against=released/contract.json
```

检查范围包括接口删除，路由/鉴权/RPC/传输更改，缩短 deadline，字段删除/编号或 JSON 名称/类型/presence/oneof/default 变化，以及枚举和事件不兼容。无破坏变更时退出 0，有破坏变更时列出原因并非零退出。

比较方向是旧客户端接入新服务端。旧客户端会严格拒绝未知响应字段和事件，所以新增响应字段或事件也报告为破坏变化；可选请求字段的增加通常保持旧请求可用。检查器不做自动版本协商，也不证明业务语义相同。相同 schema hash 仍是默认运行条件，升级需要协调客户端版本。

生成文件先写临时文件并 sync，再替换目标，避免单个文件被写到一半。整目录不是原子事务；CI 中生成到独立目录，验证完成后把整个目录作为同一发布产物部署，不要一边生成一边让服务端读取。

## 运行环境与验收

依赖使用 Protobuf 1.36.12、兼容层 1.5.4、x/net 0.59.0、x/text 0.42.0。项目最低 Go 补丁版固定为 1.26.6，toolchain 与 CI 固定 1.26.8。TypeScript Fetch 源码面向 ES2022 与标准 fetch/Streams/AbortController/TextDecoder；微信/Cocos 示例打包为 ES2020，使用对应平台 Transport。原生 SDK 的宿主和版本要求见 [接入指南](protocol-integration.md#4-选择平台适配器)。

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run build:platforms
go test -race -count=1 ./...
GODOT_BIN=/absolute/path/to/godot go test -race -count=1 ./protocol/httptransport -run Godot
DOTNET_BIN=dotnet go test -race -count=1 ./internal/platformtest
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

CI 配置见 [test.yml](../.github/workflows/test.yml)：主任务在 Linux/macOS/Windows 检查 Go、TS 与平台示例构建；Godot 任务用经过 SHA-512 验证的 4.5.1 Linux 运行时测试 GDScript；native-cores 在 Linux/macOS 使用 .NET 8.0.425 执行 C# 核心 HTTP 联调，并用 clang++ 执行 C++ ASan/UBSan。未设置 GODOT_BIN / DOTNET_BIN 的本地 Go 测试会跳过对应运行时用例，不能把跳过当作通过。

本地 Godot 实测是 macOS arm64 原生 headless；Web 流、移动导出与真实模型服务没有据此获得验证。四端独立核心和网络后端结果，与编辑器/真机结果分别记录在 [平台验收](platform-validation.md)。

2026-10-03 本地验收通过：完整 Go 竞态测试、TypeScript 类型检查与 24 项测试、Godot 单元测试及真实 Go 服务联调、CLI 七类产物生成与升级拦截。Go 1.26.8 下 govulncheck 未发现已知可达漏洞；另通过 Chromium Fetch/XHR 各 12 项、微信开发者工具 12 项、C# 核心 Go HTTP/SSE 联调、C++ 核心 sanitizer 测试；当次 SDK 开发依赖 npm audit 未发现已知漏洞。平台环境与结果见 [历史记录](../examples/platforms/reports/2026-10-03.json)。

适配代码和 CI 配置已随提交 `8b0dbb2` 推送到 main；远程执行结果以 [GitHub Actions](https://github.com/Coder-is/TabForge/actions) 为准。本地通过记录不代表远程 CI 结果已核实。

上线项目还需根据自身目标流数量和消息大小做容量测试，配置 TLS、鉴权/配额、代理禁用 SSE 缓冲与合适的流超时，并测试滚动部署期间的旧客户端。这里没有内置自动重连、回放、幂等重试、WebSocket 或供应商转换；配置依然由原有 V3 导表入口导出。
