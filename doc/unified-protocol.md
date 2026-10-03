# TabForge 统一协议工具

目标是用同一套类型和接口约定连接后端、游戏客户端、网页、小程序及大模型应用。开发者维护 Proto 和接口清单，工具检查两者的一致性，生成类型和接入文档；各平台网络适配器负责实际收发。

目前已实现 Proto RPC/清单校验、schema hash、TypeScript 类型和字段验证规则、Godot 元数据、Go HTTP 服务、TypeScript fetch、Godot GDScript、Unity C#、Unreal C++、Cocos Fetch/XHR 与微信小程序客户端，支持普通响应和 SSE。加固范围与升级要求见 [生产部署说明](production.md)，四端的运行方法与实际验证状态见 [平台验收](platform-validation.md)。

## 协议的组成

| 来源 | 负责的内容 |
| --- | --- |
| `.proto` | 消息、字段编号、类型、枚举、oneof、RPC 请求/响应、是否服务端流式 |
| `contract.json` | 协议版本、RPC 到路由的映射、传输方式、鉴权、超时、流式事件与结束条件 |
| 生成文件 | 前端类型、接口元数据、字段与接入文档、可分发的清单和描述文件 |
| 运行时适配器 | 平台网络 API、增量解码、分帧、错误、取消 |
| 应用后端 | 业务逻辑、鉴权实现、模型供应商适配、额度、持久化和回放 |

类型与字段编号以 Proto 为唯一来源。接口清单引用真实的 Proto RPC；工具拒绝不存在的接口及与 RPC 流式声明冲突的配置。表格继续用于配置数据。

```text
                    项目维护的 Proto
                  /                 \
        Excel/CSV 配置映射        RPC + 接口清单
                |                     |
        配置 JSON / Protobuf      协议校验与生成
                                  /         \
                         类型和接入文档    运行时适配器
                                          /      \
                                        后端     客户端
```

原有 V3 导表流程继续使用原有命令。新协议入口 `-protocol` 不需要索引表、类型表或 `-proto_map`，两种入口的输出参数不能混用。清单中的描述文件路径相对于清单解析；原有索引表路径仍相对于命令执行目录解析。

## v1 传输约定

所有接口使用 POST，请求正文直接使用对应消息的 **ProtoJSON**。普通成功响应：

```json
{
  "protocolVersion": "1.0.0",
  "schemaHash": "生成包的 schema hash",
  "requestId": "req-1",
  "data": {"text": "你好", "usage": {"outputTokens": "3"}}
}
```

传输错误使用非 2xx 状态，`error` 替代 `data`：

```json
{
  "protocolVersion": "1.0.0",
  "schemaHash": "生成包的 schema hash",
  "requestId": "req-1",
  "error": {"code": "timeout", "message": "request deadline exceeded", "retryable": true}
}
```

请求默认必须带 `X-Protocol-Version` 与 `X-Protocol-Schema`，匹配生成包中的版本和 schema hash，否则返回 409。版本使用 major.minor.patch，可附加 prerelease 后缀。`X-Request-ID` 可省略，由服务端生成；填写时仅接受 1–128 位 ASCII 字母、数字、下划线、点和连字符。bearer 接口默认拒绝访问，直到应用提供鉴权回调。请求正文默认最大 1 MiB。

流响应使用 `text/event-stream`：

```text
id: 1
event: text.delta
data: {"protocolVersion":"1.0.0","schemaHash":"生成包的hash","requestId":"req-1","sequence":"1","payload":{"delta":{"text":"你"}}}

id: 2
event: completed
data: {"protocolVersion":"1.0.0","schemaHash":"生成包的hash","requestId":"req-1","sequence":"2","payload":{"completed":{"response":{"text":"你好"}}}}

```

`payload` 是完整的响应 Proto 消息。流响应必须只包含一个 oneof，其成员都是消息；清单将每个成员映射为事件名，至少标记一个结束事件。`id` 与 `sequence` 一致，从十进制字符串 `"1"` 连续递增，每次请求重新开始。

完成或业务失败事件结束流；没有结束事件就 EOF 表示 `incomplete_stream`。响应头已经发送后的传输失败使用保留事件 `protocol.error`，其 data 包含 error，不包含 payload。客户端退出迭代或 AbortSignal 取消会中止 HTTP 请求；供应商工作是否同步停止由后端处理。v1 不自动重试、重连、续传，也不承诺 `Last-Event-ID` 回放。

网络分片可能只有半个 UTF-8 字符或半个事件，也可能包含多个事件。必须先增量解码 UTF-8，再分帧，最后解析 JSON。SSEParser 支持 BOM、CR/LF/CRLF、注释和多个 data 行，默认限制每帧为 1 MiB 的 JavaScript 字符数。超时覆盖整个调用或流；Go 处理函数必须遵守 context 取消。HTTP 部署还需配置请求读取超时与反向代理缓冲。

## 类型与演进

沿用 [官方 ProtoJSON 映射](https://protobuf.dev/programming-guides/json/)：64 位整数用十进制字符串，bytes 用 Base64，枚举通常用名称，字段使用 lowerCamelCase 或显式 json_name。数组、map 和 well-known types 使用标准映射。

生成的 TypeScript 类型描述**序列化后的 JSON 形态**。字段可因默认值或 presence 规则省略；oneof 禁止同时填写多个成员。Go 输入解析检查未知字段、溢出、oneof 和重复 JSON 成员；各客户端同时使用生成的 wireSchema 检查字段、类型、map、数组和消息结构，并校验包络与事件顺序。它们不直接等于某个 Protobuf 二进制库的 message 对象。

删除 Proto 字段需 reserved 原编号与名称，不能重用旧编号。ProtoJSON 的字段名和枚举名也有兼容性要求。`-protocol_against` 可检查旧客户端到新服务端的破坏变化；默认严格要求版本与 hash 相同，仍需协调部署，不提供自动版本协商。

## 大模型普通与流式响应

示例的 ChatRequest、ChatResponse、ChatEvent 覆盖文本增量、工具调用增量、用量、完成和业务失败。普通返回的 ChatResponse 与流的 completed.response 使用同一类型。

供应商事件由后端适配为这些业务消息。工具调用以 call_id 区分，arguments_delta 是字符串片段，需要拼接完整后解析。结束原因、工具结果、音频/图像分块、结构化输出和会话管理按业务扩展 Proto。协议工具不内置供应商密钥或某家供应商的结束标记。

当前示例是确定性演示，**没有调用真实模型，也没有实现供应商适配器**。HTTP SSE framing 参考 [WHATWG 标准](https://html.spec.whatwg.org/multipage/server-sent-events.html)。

## 当前平台状态

| 平台 | 当前能力 | 尚需完成 |
| --- | --- | --- |
| Go 后端 | ProtoJSON HTTP/SSE 服务，与 TS 实际互通 | 类型化服务端桩与发布包 |
| 网页、Node | TS 类型与 fetch 适配器，Node 24 和 Chromium 154 实测 | Safari/Firefox 等其他浏览器 |
| Cocos Creator 3.8 目标 | Fetch/XHR/微信适配，Web 网络后端实测 | Creator 编辑器、JSB 和移动构建验证 |
| Laya、其他 TS 引擎 | 可复用类型、CallbackTransport 和流解析核心 | 各自平台适配与验证 |
| 微信小程序/小游戏 API | wx.request 分块、原生 UTF-8 解码和取消，开发者工具 12 项实测 | 小程序真机、小游戏构建；其他厂商需单独适配 |
| Unity 2022.3 / Unity 6 原生目标 | UPM、UnityWebRequest、流读取、取消与主线程回调；C# 核心真实 HTTP 联调 | Unity 编辑器、IL2CPP、移动构建；WebGL SSE 桥 |
| Unreal 5.5+ 目标 | Runtime 插件、HTTP 接收流、游戏线程回调；独立 C++ 核心 sanitizer 测试 | 引擎编译、HTTP 后端 Automation 与移动验证 |
| Godot 4.5.1+ 原生 | GDScript HTTP/SSE 客户端、字段验证、取消和超时，4.5.1 macOS headless 实测 | Web 流桥、移动导出与其他引擎版本验证 |
| Lua | 已有配置 Lua 输出 | 网络类型/编解码与网络适配 |
| Java、Python、Rust 等后端 | 可用各自 protoc 插件 | 协议工具的语言运行时适配 |

缺少 fetch、ReadableStream、AbortController 或 TextDecoder 的平台需实现 Transport。可复用协议与分帧核心，但不能据此宣称对应引擎或小程序已经验证兼容。

## 后续实施顺序

1. **平台验证**：运行已提供的 Unity PlayMode、Unreal Automation、Cocos 场景组件及微信真机验收，补齐目标构建与版本矩阵；继续完善 WebGL 流桥与其他平台网络 API。
2. **传输扩展**：WebSocket、Protobuf 二进制帧、必要的 NDJSON。先规定关联 ID、分帧、心跳、背压、重连、幂等与回放语义，再生成适配器。v1 拒绝尚未实现的传输名。
3. **协议演进**：继续扩大已实现的兼容性和字段验证覆盖，补版本协商、OpenAPI/JSON Schema、类型化服务端桩与发布包。
4. **模型与多模态适配**：按供应商及接口版本映射普通/流式响应、工具调用、结束原因和错误；建立录制事件回归测试后再验证真实服务。

共同验收目标：各语言序列化同一测试数据得到一致结果，任意分片位置不破坏 Unicode 或消息边界，取消、超时和中途失败不会被误判为成功。

## 运行与检查

从仓库根目录运行：

```bash
# 使用仓库已包含的描述文件，无须安装 protoc。
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated

# 修改 Proto 后重新生成，需要 protoc。
bash examples/protocol/Make.sh

# 启动确定性的本地演示。
go run ./examples/protocol

# TypeScript 编译器及示例打包器仅为开发依赖，运行时无 npm 依赖。
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
go test -race ./...
```

不指定 `-protocol_out` 时只校验；生成目录写入并覆盖 contract.json、schema.pb、types.ts、PROTOCOL.md、wire_schema.json、protocol.gd 和 runtime.json。整个目录作为同一发布产物分发。二进制消息代码仍由 protoc 和各语言插件生成。

跨语言集成测试需要 Node 24+；缺少该运行时的本地 Go 测试会跳过此项，CI 安装 Node 24 并执行全部验证。
