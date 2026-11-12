# Unity 原生网络适配

UPM 包 `com.tabforge.protocol`，目标 Unity 2022.3 / Unity 6 原生运行时，依赖官方 `com.unity.nuget.newtonsoft-json` 3.2.2。从 Package Manager 的 Add package from disk 选择本目录 package.json，或使用仓库验收项目。

Unity 接收的是 ProtoJSON `JToken`；生成的 `runtime.json` 规定接口、字段和类型，运行前验证请求，接收后验证响应。SDK 不把生成的 TS 类型误当成 C# 类。需要强类型消息时可另外用 protoc C# 并用对应 ProtoJSON 编解码器转换。

把业务生成包中的 runtime.json 作为 TextAsset 放入项目并传给 Configure；示例资产构建会复制到验收工程 Resources。它必须与后端版本/hash 相同，不能只替换 Proto 消息而保留旧元数据。完整生成与后端注册见 [前后端指南](../../doc/protocol-integration.md)。

```csharp
using Newtonsoft.Json.Linq;
using TabForge.Protocol;
// 客户端组件及所有调用在主线程创建。
var client = gameObject.AddComponent<TabForgeClient>();
client.Configure("https://api.example.com", runtimeTextAsset.text);
var request = new JObject { ["prompt"] = "你好", ["conversationId"] = "18446744073709551615" };
var handle = client.Stream("chatStream", request);
handle.EventReceived += value => UnityEngine.Debug.Log(value.Payload);
handle.Completed += value => UnityEngine.Debug.Log("completed");
handle.Failed += error => UnityEngine.Debug.LogError(error.Code);
// 普通请求：client.Request("chatComplete", request)。
// 场景切换前可 handle.Cancel()；组件 OnDisable 会清理全部请求。
```

`UnityWebRequest` + `DownloadHandlerScript` 在请求期间逐块接收。回调都在主线程；Cancel 可跨线程调用，网络销毁留在主线程。调用返回后下一帧启动，因此可先订阅事件。HTTP 重定向关闭，bearer token 不会被自动转发；SDK 不自动重试。超时使用全程时钟，并结合底层超时。默认普通正文 1 MiB、单帧 1 MiB UTF-16 字符、最多 16 个并发请求。流不积累全程正文；响应错误、终止、取消、禁用组件后都会释放网络资源。

Completed 表示协议流已结束；`failed` 这类业务终止事件仍通过 EventReceived/Completed 交付，需要业务自行检查 payload。Failed 回调用于传输/协议错误。

字段规则与 Go protojson 一致：整数可为 JSON 数字或数字字符串（`1.0`、`1e2` 视为整数，64 位整数也可为数字），null 字段视为未设置，bytes 接受标准/URL Base64 及省略填充。严格 UTF-8/JSON 解析拒绝非法编码、扩展语法、重复键（含转义后相同的键）、未知字段、oneof 冲突及事件序号错误。JSON 深度按容器嵌套计（根为 0），节点按全部 JSON 值计：网络消息为 64/100000（另有 1 MiB 正文上限），数据文件（`DataSchema`）为 64/10000000，与 Go/Node/Java/Python/Godot/Unreal 的数据加载器一致；`DataSchema.Decode` 返回规范 ProtoJSON（64 位整数为十进制字符串、Base64 补齐、去掉 null 字段）。请求元数据是受信任的生成包，应与后端一起发布。

**WebGL 的 SSE 暂不支持**，需要 JS fetch 桥；普通请求仍走 UnityWebRequest。IL2CPP、Android/iOS、代理和 HTTPS 证书须在目标构建验证。本机没有 Unity 编辑器，当前通过的是 .NET 8 下 C# 核心及 Go HTTP/SSE 联调，不是 UnityWebRequest 实测。

## 源码职责

| 文件 | 职责 |
| --- | --- |
| [ProtocolCore.cs](Runtime/ProtocolCore.cs) | 协议元数据、错误、JSON 入口、流事件与会话校验 |
| [JsonSyntax.cs](Runtime/JsonSyntax.cs) | 严格 JSON 语法检查与预算，数据加载时保留数字原文 |
| [SseParser.cs](Runtime/SseParser.cs) | SSE 增量分帧 |
| [WireValidator.cs](Runtime/WireValidator.cs) | 字段与 Protobuf 内建类型校验 |
| [TabForgeClient.cs](Runtime/TabForgeClient.cs) | UnityWebRequest、请求生命周期与主线程回调 |
| [RealtimeCodec.cs](Runtime/RealtimeCodec.cs) | 长连接帧格式与二进制 Protobuf ↔ ProtoJSON 编解码 |
| [RealtimeClient.cs](Runtime/RealtimeClient.cs) | 长连接会话：握手、序号确认、重连恢复、请求/推送/通知 |
| [ClientWebSocketTransport.cs](Runtime/ClientWebSocketTransport.cs) | 基于 ClientWebSocket 的默认传输 |
| [TabForgeRealtime.cs](Runtime/TabForgeRealtime.cs) | 在 Update 中驱动长连接的组件 |

UPM 自动编译 Runtime 目录。手工集成源码时应复制完整 Runtime 目录；单独引用 `ProtocolCore.cs` 无法编译。独立 .NET 核心测试显式包含前四个文件，不包含 Unity 网络层。

## 验证

```bash
dotnet run --project sdk/unity/Tests/CoreTests.csproj -- examples/protocol/generated/runtime.json
# Go 跨语言测试会启动实际服务：
DOTNET_BIN=dotnet go test -race -count=1 -v ./internal/platformtest
# 编辑器 PlayMode 验收（先启动平台服务、生成验收资产）：
UNITY_EDITOR=/absolute/path/to/Unity node examples/platforms/run-unity.mjs
```

PlayMode 用例覆盖 JSON/SSE、Unicode、uint64、实时事件、取消、超时、错误帧及组件退出清理；运行脚本检查结果 XML，缺环境不算通过。详见 [平台验收](../../doc/platform-validation.md)。

本地联调启用 HTTP 代理时，将回环地址加入现有 `NO_PROXY` / `no_proxy`；环境配置与其他开发检查见[维护指南](../../doc/development.md)。

API 依据：[DownloadHandlerScript](https://docs.unity.com/en-us/engine/6000.3/script-reference/unityengine/networking/downloadhandlerscript)、[官方 Newtonsoft 包](https://docs.unity3d.com/Packages/com.unity.nuget.newtonsoft-json@3.2/manual/index.html)。

## 实时长连接

`RealtimeClient` 是 WebSocket 通道客户端，协议与可靠性语义见 [实时长连接](../../doc/realtime.md)：请求/推送/通知、断线自动重连并恢复会话、未确认帧重发且请求不会重复执行，默认使用二进制 Protobuf 帧（`ProtobufCodec` 依据 runtime.json 的 `codecSchema` 与 ProtoJSON `JObject` 互转）。

```csharp
using Newtonsoft.Json.Linq;
using TabForge.Protocol;

var host = new GameObject("Realtime");
DontDestroyOnLoad(host); // 跨场景保持会话
var client = host.AddComponent<TabForgeRealtime>().Configure(runtimeTextAsset.text, new RealtimeOptions
{
    Url = "wss://game.example.com/ws/game",
    Channel = "game",
    Token = () => loginToken, // 只在新会话时发送
});
client.StateChanged += (state, error) =>
{
    // Connected：新会话，重新拉取游戏状态；Resumed：原会话继续，没有丢失消息；
    // Closed：被踢、顶号、鉴权失败等，不会再重连。
};
client.On("chatMessage", message => Debug.Log(message["text"]));
client.Connect();

var login = client.Request("login", new JObject { ["nickname"] = "勇者" });
login.Completed += reply => Debug.Log(reply["playerId"]);
login.Failed += error => Debug.LogWarning(error.Code); // session_lost 表示结果未知
// 或者：var reply = await client.Request("login", ...).Task;
client.Notify("move", new JObject { ["tick"] = 1 }); // volatile：掉线时返回 false
```

- `TabForgeRealtime` 在 `Update` 中调用 `Tick()`，所有回调（状态、推送、请求结果）都在主线程执行；GameObject 销毁或退出游戏时关闭会话。不使用组件时自行在一个线程上定期调用 `Tick()`。公开方法可以跨线程调用。
- 消息是 ProtoJSON 形态的 `JObject`：64 位整数为十进制字符串，枚举为名称，时间戳为 RFC 3339 字符串。发送前按 runtime.json 校验并规范化，不合法的消息直接抛出 `invalid_message`。注意 `JObject.Parse` 默认会把时间戳字符串转成 `DateTime`，构造消息时请用 `Json.Parse` 或 `new JObject`。
- 默认传输为 `ClientWebSocketTransport`（`System.Net.WebSockets.ClientWebSocket`），适用于 Standalone、Android、iOS；可通过 `ClientWebSocketTransport.Configure` 设置代理或证书。
- **WebGL**：WebGL 播放器没有 ClientWebSocket，默认工厂自动改用 `WebGLWebSocketTransport`，经 `Runtime/Plugins/WebGL/TabForgeWebSocket.jslib` 桥接到浏览器 WebSocket，代码无需改动（`RealtimeOptions.Socket` 保持 null）。浏览器事件进入队列，仍在 `Tick` 中回调。浏览器 WebSocket 不能设置请求头，令牌照常放在 `hello` 中；跨域部署需在服务器 `OriginPatterns` 中允许页面来源。
- `Encoding = RealtimeEncoding.Json` 使用 JSON 帧，便于抓包调试。

验证：`go test ./protocol/realtime` 在设置 `DOTNET_BIN` 后构建 [RealtimeTests](Tests/Realtime/RealtimeTests.csproj)，运行模拟服务器单元测试、与 Go 对 2800 条随机消息的编解码一致性检查，以及在随机断线代理下连接 Go 服务端的压测（每个请求恰好执行一次、推送不丢不重不乱序、踢人在断线时也能送达），两种编码各一次。2026-11-09 在 Docker（Linux，.NET 8）中通过。这是普通 .NET 运行时，不是 Unity 引擎：编辑器 PlayMode、IL2CPP、Android/iOS 构建尚未实测。
WebGL 桥的验证：`TestUnityWebGLBridgeUnderChaos`（需要 node）在 Node 中以模拟的 Emscripten 堆加载 jslib，代替 C# 侧调用它，让 TypeScript 客户端的全部帧经过该桥，在随机断线代理下跑与上面相同的恰好一次与顺序校验，两种编码；堆在每次调用后写入垃圾数据，桥若保留堆引用会立即失败。C# 路由（按 id 分发、关闭只通知一次、关闭后丢弃事件）由 `unit` 用伪造的 jslib 覆盖；`TestCSharpWebGLBranchCompiles` 以 `UNITY_WEBGL` 编译 P/Invoke 分支。真实 Unity WebGL 构建与浏览器运行尚未实测。
