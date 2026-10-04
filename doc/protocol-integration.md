# 前后端协议接入指南

TabForge 用 Proto 定义消息和 RPC，用接口清单规定路由、鉴权、超时与流式事件。生成包让后端和客户端使用相同字段、类型及协议身份；SDK 负责 JSON/SSE 传输，业务处理由应用实现。配置表导出仍使用独立的 V3 入口。

## 1. 定义并生成协议

从 [chat.proto](../examples/protocol/proto/chat.proto) 与 [contract.json](../examples/protocol/contract.json) 开始：普通 RPC 配置 `http_json`，`returns (stream ...)` RPC 配置 `http_sse`。v1 只支持 POST、`none`/`bearer` 鉴权和这两种传输。流式响应消息以一个 oneof 定义事件，清单映射事件名并标记结束事件。

在仓库根目录执行：

```bash
# 已有描述文件时，可直接校验和生成。
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated
# 修改示例 Proto 后，重新编译描述文件并生成；需要安装 protoc。
bash examples/protocol/Make.sh
```

自建协议时使用 `protoc --include_imports --descriptor_set_out=...` 生成描述文件，并让清单引用它。描述文件路径相对于清单所在目录解析。单独 `-protocol` 只校验，不输出文件。

| 生成文件 | 使用方与用途 |
| --- | --- |
| `contract.json` | Go 服务加载的可分发清单；描述文件路径改为同目录 `schema.pb` |
| `schema.pb` | 包含依赖的 Protobuf 描述文件，供 Go 动态消息与协议解析使用 |
| `types.ts` | TS ProtoJSON 类型、接口表、版本、schema hash 和字段验证规则 |
| `wire_schema.json` | 单独的 JSON 字段验证规则，供其他实现读取 |
| `protocol.gd` | Godot 使用的 `CONTRACT` 元数据 |
| `runtime.json` | Unity/Unreal 使用的版本、hash、接口表与字段规则 |
| `PROTOCOL.md` | 生成的接口、字段、鉴权与事件接入说明 |

把整套生成文件作为一个版本产物发布。客户端和后端默认严格匹配版本及 hash。生成器不代替 `protoc` 的语言插件；需要 Protobuf 二进制消息类时另行生成。Unity/Unreal 当前使用 JSON 对象及运行时验证，没有自动生成 C#/C++ 类型化服务桩。

## 2. 接入 Go 后端

`protocol.Load(path)` 加载清单与描述文件，`httptransport.New(contract)` 创建 handler，再按清单中的接口 ID 注册业务函数：

```go
// UnaryHandler
func(context.Context, proto.Message) (proto.Message, error)
// StreamHandler；每个完整事件消息调用一次 emit。
func(context.Context, proto.Message, func(proto.Message) error) error
```

用 `HandleUnary("chatComplete", handler)` / `HandleStream("chatStream", handler)` 注册并检查返回的错误；用 `httptransport.HTTPServer(addr, handler)` 启动服务。完整可运行代码见 [main.go](../examples/protocol/main.go)，生产选项见 [部署说明](production.md)。

输入是动态 Proto 消息，输出可用同一描述结构的生成消息或动态消息。业务只返回消息，包络、序号、字段检查及传输错误由服务统一处理。流必须发送清单标记的结束事件；仅从处理函数返回 nil 而没有结束事件会产生 `incomplete_stream`。

`bearer` 接口需要安装 `Authorize` 回调校验 token；跨域网页用 `WithCORS(handler, explicitOrigins)` 配置来源白名单。业务与模型供应商调用必须遵守 context 取消。容量、配额、日志、模型事件转换和幂等策略由后端提供。示例清单为 `auth=none`，示例服务没有安装 CORS 中间件。

## 3. 接入 TypeScript 客户端

以下代码以 `examples/protocol/client.ts` 所在目录为基准；放到业务项目时调整导入路径和服务地址：

```typescript
import { ProtocolClient, FetchTransport } from "../../sdk/typescript/runtime.ts";
import { operations, protocolVersion, schemaHash, wireSchema } from "./generated/types.ts";
import type { ProtocolTypes } from "./generated/types.ts";

const client = new ProtocolClient<ProtocolTypes>(operations,
  new FetchTransport("http://127.0.0.1:18082", protocolVersion, globalThis.fetch,
    { schemaHash, wireSchema }));
const request = { prompt: "你好", conversationId: "18446744073709551615" };
const response = await client.call("chatComplete", request);
console.log(response.text ?? "");

const cancel = new AbortController();
for await (const event of client.stream("chatStream", request, { signal: cancel.signal })) {
  if (event.payload.delta) console.log(event.payload.delta.text ?? "");
  if (event.payload.failed) throw new Error(event.payload.failed.message ?? "业务失败");
}
// 页面退出或用户停止时调用 cancel.abort()；提前退出迭代也会中止网络请求。
```

普通请求正文直接是请求消息，没有 `data` 外层。成功响应的 `data` 由 `call` 解包；流事件的 `payload` 是完整 Proto 响应消息，保留 oneof 成员名。64 位整数用十进制字符串，bytes 用标准 Base64；默认字段可能被省略。

SDK 校验接口、请求/响应字段、版本、hash、request ID 和连续序号。`failed` 是业务定义的终止事件，应用检查其 payload；`protocol.error` 或本地验证失败是传输/协议错误，TS 抛出 `ProtocolError`，原生 SDK 调用错误回调。原生 SDK 的 Completed/OnComplete 仅表示协议正常终止，不代表业务必然成功。

本仓库 TS 包标记为 `private`，当前以源码或业务打包产物分发。浏览器不能直接执行 `.ts`；使用项目构建器，或运行已打包的验收网页。仓库示例可用 Node 24+ 的 TypeScript 支持运行，命令见 [协议示例](../examples/protocol/README.md)。

## 4. 选择平台适配器

| 平台 | 接入方式 | 分发与生命周期 |
| --- | --- | --- |
| 网页 / Node | [FetchTransport](../sdk/typescript/README.md) | 打包 SDK + `types.ts`；退出页面/任务取消请求 |
| 微信小程序 | [WechatTransport](../sdk/wechat/README.md) | 打包为 CommonJS，传入宿主 `wx`；页面卸载时取消 |
| Cocos | [CocosTransport](../sdk/cocos/README.md) | Web Fetch、原生 XHR 显式选择；微信目标使用 WechatTransport；组件销毁时取消 |
| Unity 原生 | [UPM 包](../sdk/unity/README.md) | 安装包、加载 `runtime.json`；主线程调用，组件禁用清理请求 |
| Unreal | [Runtime 插件](../sdk/unreal/README.md) | 构建插件、加载 `runtime.json`；持有 Client，游戏线程调用/释放 |
| Godot 原生 | [GDScript SDK](../sdk/godot/README.md) | 复制 SDK 脚本与 `protocol.gd`，使用 CONTRACT；节点退出时清理 |

目标运行时要求、配置代码和引擎验收命令见对应 SDK。没有现成后端的平台可实现 `Transport` 或 `CallbackNetwork`，复用字段与分帧规则。当前实现覆盖范围不等于所有引擎版本、小游戏或移动导出已经实测，具体状态见 [平台验收](platform-validation.md)。

## 5. 演示与平台验收

| 服务 | 启动命令 | 用途 |
| --- | --- | --- |
| `127.0.0.1:18082` | `go run ./examples/protocol` | 固定业务演示；普通响应“你好，TabForge”，流为两条文本、用量和完成共四个事件 |
| `127.0.0.1:18083` | `go run ./examples/platforms/server` | 平台验收；正常流两个事件，并提供取消/超时和坏帧场景 |

平台验收客户端应连接 18083，基础客户端演示连接 18082；两个服务的期望数据不同。`/_platform/fault/*` 仅用于测试，不加入生产路由。运行资产构建和编辑器工程见 [验收工程](../examples/platforms/README.md)。当前示例不连接真实大模型；接供应商时后端把普通响应/增量事件转换为自己的 Proto 消息。

## 6. 升级与排错

升级前保留旧生成包，并检查旧客户端接入新服务端：

```bash
go run . -protocol=new/contract.json -protocol_against=released/contract.json -protocol_out=build/protocol
```

有破坏变化时非零退出；成功后统一发布新生成包与服务。检查器会把新增响应字段/事件也视为破坏变化，因为旧客户端严格拒绝未知字段。兼容性检查通过不等于 hash 保持不变，也不会自动协商版本。

| 错误码 | 优先检查 |
| --- | --- |
| `version_mismatch` / `schema_mismatch` | 前后端是否加载同一生成包，有无旧包缓存或混合部署 |
| `bad_request` / `invalid_message` | ProtoJSON 字段、未知成员、oneof、64 位整数字符串和取值范围 |
| `invalid_sequence` | SSE id/sequence 是否从 1 连续递增，代理或业务是否重复/丢失帧 |
| `incomplete_stream` | 后端是否发送结束事件，代理是否缓冲、截断或超时 |
| `unsupported_transport` | 目标网络 API 是否在 HTTP 完成前提供真实增量数据 |
| `backpressure` | 消费回调是否过慢；降低生产速率或按部署容量调整有界队列 |
| `request_too_large` / `response_too_large` / `frame_too_large` | 消息与各端上限是否匹配，字节数与 UTF-16 字符数不能混用 |
| `timeout` / `cancelled` | 全程 deadline、主动取消、页面/场景退出及后端 context |

协议细节见 [架构](unified-protocol.md)，各 SDK 默认限制与生产发布要求见 [部署说明](production.md)。v1 没有内置 WebSocket、自动重试、重连或回放。
