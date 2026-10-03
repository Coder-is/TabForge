# 协议模块生产部署与验收

加固覆盖 HTTP ProtoJSON/SSE、Go 服务端和各客户端；已加入 Unity、Unreal、Cocos 与微信专用适配，实际验证边界见 [平台验收](platform-validation.md)。它提供生产部署需要的传输边界与回归测试；业务容量、认证策略、模型供应商和实际目标平台仍由接入项目验证。

## 固定协议身份

生成包现在包含 contract.json、schema.pb、types.ts、PROTOCOL.md、wire_schema.json、protocol.gd 和 runtime.json。schema hash 对清单与描述文件计算 SHA-256，排除描述文件的路径和源注释；包括语言选项在内的其他描述内容仍参与身份计算。

Go 服务端默认要求 X-Protocol-Version 与 X-Protocol-Schema 一致，否则 409。所有响应包络增加 schemaHash；各客户端检查包络与字段规则，拒绝类型错误、溢出、未知字段和 oneof 冲突。Hash 用于发现部署差异，不是鉴权或签名。

这是相对于上一版的接入约定变化。升级时要同时部署客户端生成文件和服务端。临时兼容旧客户端可设 RequireSchemaHash=false，但仍会拒绝显式填写错误 hash 的请求；不能用它替代明确的版本发布策略。

TS 初始化示例：

```typescript
import { operations, protocolVersion, schemaHash, wireSchema } from "./generated/types.ts";
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

## 协议升级

```bash
go run . -protocol=new/contract.json -protocol_against=released/contract.json
```

检查范围包括接口删除，路由/鉴权/RPC/传输更改，缩短 deadline，字段删除/编号或 JSON 名称/类型/presence/oneof/default 变化，以及枚举和事件不兼容。无破坏变更时退出 0，有破坏变更时列出原因并非零退出。

比较方向是旧客户端接入新服务端。旧客户端会严格拒绝未知响应字段和事件，所以新增响应字段或事件也报告为破坏变化；可选请求字段的增加通常保持旧请求可用。检查器不做自动版本协商，也不证明业务语义相同。相同 schema hash 仍是默认运行条件，升级需要协调客户端版本。

生成文件先写临时文件并 sync，再替换目标，避免单个文件被写到一半。整目录不是原子事务；CI 中生成到独立目录，验证完成后把整个目录作为同一发布产物部署，不要一边生成一边让服务端读取。

## 运行环境与验收

依赖升级至 Protobuf 1.36.12、兼容层 1.5.4、x/net 0.59.0、x/text 0.42.0。这些依赖需要 Go 1.26；项目最低补丁版固定为 1.26.6，toolchain 与 CI 固定 1.26.8，避免自动回落到有已知问题的 1.26.0。TypeScript fetch 适配器要求 ES2022 与标准 fetch/Streams/AbortController/TextDecoder；其他网络平台需对应 Transport。

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
go test -race -count=1 ./...
GODOT_BIN=/absolute/path/to/godot go test -race -count=1 ./protocol/httptransport -run Godot
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

CI 在 Linux/macOS/Windows 检查 Go 与 TS，另外用经过 SHA-512 验证的 Godot 4.5.1 Linux 运行时测试 GDScript。当前本地 Godot 实测是 macOS arm64 原生 headless；Godot Web 流、移动导出及真实模型服务没有据此获得验证。新增四端专用适配的独立核心和网络后端结果，与编辑器/真机结果分别记录在 [平台验收](platform-validation.md)。

2026-10-03 本地验收通过：完整 Go 竞态测试、TypeScript 类型检查与 24 项测试、Godot 单元测试及真实 Go 服务联调、CLI 七类产物生成与升级拦截。最后的 HTTP 大小限制修改另通过协议模块竞态回归和 Godot 联调。Go 1.26.8 下 govulncheck 未发现已知可达漏洞；另通过 Chromium Fetch/XHR 各 12 项、微信开发者工具 12 项、C# 核心 Go HTTP/SSE 联调、C++ 核心 sanitizer 测试；SDK 开发依赖 npm audit 未发现已知漏洞。远程 CI 结果仍需提交后确认。

上线项目还需根据自身目标流数量和消息大小做容量测试，配置 TLS、鉴权/配额、代理禁用 SSE 缓冲与合适的流超时，并测试滚动部署期间的旧客户端。这里没有内置自动重连、回放、幂等重试、WebSocket 或供应商转换；配置依然由原有 V3 导表入口导出。
