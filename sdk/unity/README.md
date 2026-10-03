# Unity 原生网络适配

UPM 包 `com.tabforge.protocol`，目标 Unity 2022.3 / Unity 6 原生运行时，依赖官方 `com.unity.nuget.newtonsoft-json` 3.2.2。从 Package Manager 的 Add package from disk 选择本目录 package.json，或使用仓库验收项目。

Unity 接收的是 ProtoJSON `JToken`；生成的 `runtime.json` 规定接口、字段和类型，运行前验证请求，接收后验证响应。SDK 不把生成的 TS 类型误当成 C# 类。需要强类型消息时可另外用 protoc C# 并用对应 ProtoJSON 编解码器转换。

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

```bash
dotnet run --project sdk/unity/Tests/CoreTests.csproj -- examples/protocol/generated/runtime.json
# Go 跨语言测试会启动实际服务：
DOTNET_BIN=dotnet go test -race -v ./internal/platformtest
# 编辑器 PlayMode 验收（先启动平台服务、生成验收资产）：
UNITY_EDITOR=/absolute/path/to/Unity node examples/platforms/run-unity.mjs
```

PlayMode 用例覆盖 JSON/SSE、Unicode、uint64、实时事件、取消、超时、错误帧及组件退出清理；运行脚本检查结果 XML，缺环境不算通过。详见 [平台验收](../../doc/platform-validation.md)。

API 依据：[DownloadHandlerScript](https://docs.unity.com/en-us/engine/6000.3/script-reference/unityengine/networking/downloadhandlerscript)、[官方 Newtonsoft 包](https://docs.unity3d.com/Packages/com.unity.nuget.newtonsoft-json@3.2/manual/index.html)。
