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

64 位整数按十进制字符串验证，严格 UTF-8/JSON 解析拒绝非法编码、扩展语法、重复键、未知字段、oneof 冲突及事件序号错误。JSON 深度/节点预算分别为 64/100000。请求元数据是受信任的生成包，应与后端一起发布。

**WebGL 的 SSE 暂不支持**，需要 JS fetch 桥；普通请求仍走 UnityWebRequest。IL2CPP、Android/iOS、代理和 HTTPS 证书须在目标构建验证。本机没有 Unity 编辑器，当前通过的是 .NET 8 下 C# 核心及 Go HTTP/SSE 联调，不是 UnityWebRequest 实测。

## 源码职责

| 文件 | 职责 |
| --- | --- |
| [ProtocolCore.cs](Runtime/ProtocolCore.cs) | 协议元数据、错误、JSON 入口、流事件与会话校验 |
| [JsonSyntax.cs](Runtime/JsonSyntax.cs) | 严格 JSON 语法检查 |
| [SseParser.cs](Runtime/SseParser.cs) | SSE 增量分帧 |
| [WireValidator.cs](Runtime/WireValidator.cs) | 字段与 Protobuf 内建类型校验 |
| [TabForgeClient.cs](Runtime/TabForgeClient.cs) | UnityWebRequest、请求生命周期与主线程回调 |

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
