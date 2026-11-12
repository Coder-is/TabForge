# Unreal C++ 网络适配

`TabForgeProtocol.uplugin` 提供 Runtime 模块，目标 UE 5.5+ 的 HTTP API；调用方 Build.cs 添加 `TabForgeProtocol`、`Json`。安装到项目 Plugins/TabForgeProtocol 后构建。此插件提供 C++ API，尚未提供 Blueprint 节点。

将业务生成包的 runtime.json 内容读入 RuntimeJson，再创建 Client；部署时与服务端统一版本/hash。示例打包只复制源码插件，引擎编译需另外执行。协议生成、Go 后端与错误处理见 [前后端指南](../../doc/protocol-integration.md)。

```cpp
#include "TabForgeClient.h"
FTabForgeError Error;
auto Client = FTabForgeClient::Create(TEXT("https://api.example.com"), RuntimeJson, Error);
if (!Client.IsValid()) { /* handle Error */ return; }
auto Data = MakeShared<FJsonObject>();
Data->SetStringField(TEXT("prompt"), TEXT("你好"));
Data->SetStringField(TEXT("conversationId"), TEXT("18446744073709551615"));
FTabForgeCallbacks Callbacks;
Callbacks.OnEvent = [](const FTabForgeEvent& Event) { /* game thread */ };
Callbacks.OnComplete = [](const TSharedPtr<FJsonValue>& Payload) { /* terminal payload */ };
Callbacks.OnError = [](const FTabForgeError& Failure) { /* code, message, retryable */ };
auto Request = Client->Stream(TEXT("chatStream"), MakeShared<FJsonValueObject>(Data), MoveTemp(Callbacks));
// 普通接口：Client->Call；结束使用时 Client->CancelAll()。
```

持有 Client 至请求结束。Create、Call、Stream、CancelAll 在 game thread 调用；单请求 Cancel 可跨线程。所有用户回调在 game thread 的 core ticker 执行。请求方法不会同步触发回调。业务消息使用 FJsonValue，字段以生成的 runtime.json 检查，需要 Protobuf 二进制类型时另外使用 protoc C++。

OnComplete 表示协议完成或终止；`failed` 等业务终止事件仍通过 OnEvent/OnComplete 交付，业务自行检查 payload。OnError 用于传输/协议错误。Client 也应在 game thread 释放。

HTTP 接收使用 `SetResponseBodyReceiveStream(FArchive)`，Serialize 在 HTTP 线程只放入有限队列，游戏线程增量分帧/校验/回调，不依赖完整响应缓存。默认普通正文与单帧最大 1 MiB 字节，队列最大 1 MiB / 128 块，同时最多 16 请求。消费过慢显式报 backpressure；底层不支持接收流时返回 unsupported_transport。UTF-8、JSON 重复键/语法、字段类型、oneof、schema、request ID 和顺序均检查；字段取值规则与 Go protojson 一致（如整数接受 `1.0`、`1e2` 和数字字符串，`null` 视为未设置），64 位整数建议以十进制字符串发送。JSON 深度预算为 64（根值为 0 层，按容器嵌套计），节点预算按 JSON 值计数：网络消息 100,000，数据包（TabForgeData）10,000,000，各运行时一致。FJsonObject 键不区分大小写，因此仅大小写不同的重复成员会被拒绝。全程 deadline、CancelAll 和终止事件会停止 HTTP。

本机没有 Unreal 编辑器，已验证独立 C++17 SSE/严格 JSON 核心并通过地址与未定义行为 sanitizer；**插件引擎编译和 HTTP 后端尚未实测**。HTTPS、平台重定向行为、移动打包及其他引擎版本需要验收，生产路由应直接返回协议响应，避免依赖 HTTP 重定向。自动重试、重连、回放不在此版本范围内。

```bash
clang++ -std=c++17 -Wall -Wextra -Werror -fsanitize=address,undefined \
  -I sdk/unreal/Source/TabForgeProtocol/Public sdk/native/tests/sse_test.cpp -o /tmp/tabforge-sse-test
/tmp/tabforge-sse-test
# 在已安装引擎机器上先用 RunUAT BuildPlugin 构建插件，然后运行：
UNREAL_EDITOR=/absolute/path/to/UnrealEditor \
UNREAL_PLUGIN_BUILD=/absolute/path/to/packaged-plugin node examples/platforms/run-unreal.mjs
```

插件带 `TabForge.Protocol.Parser` 与 `TabForge.Protocol.NativeHTTP` Automation 用例。NativeHTTP 需要服务地址和 runtime.json，缺少时明确失败；运行器检查两个测试报告，空报告/未执行不算成功。准备步骤见 [平台验收](../../doc/platform-validation.md)。

API 依据：[UE 5.5 接收流](https://dev.epicgames.com/documentation/unreal-engine/API/Runtime/HTTP/Interfaces/IHttpRequest/SetResponseBodyReceiveStream?application_version=5.5)、[请求与线程策略](https://dev.epicgames.com/documentation/unreal-engine/API/Runtime/HTTP/Interfaces/IHttpRequest?application_version=5.5)。

## 实时长连接

`FTabForgeRealtimeClient`（`TabForgeRealtime.h`）是 WebSocket 通道客户端，协议与可靠性语义见 [实时长连接](../../doc/realtime.md)：请求/推送/通知，断线后自动重连并恢复会话，未确认帧会重发，同一请求不会重复执行。它使用引擎 `WebSockets` 模块（插件 Build.cs 已加入私有依赖），固定使用二进制 Protobuf 子协议 `tabforge.realtime.v1.proto`，不支持 JSON 帧。生成包的 runtime.json 需包含通道（`channels`、`codecSchema`）。

```cpp
#include "TabForgeRealtime.h"
FTabForgeRealtimeOptions Options;
Options.Url = TEXT("wss://game.example.com/ws/game");
Options.Channel = TEXT("game");
Options.Token = [] { return LoginToken; };      // 只在新会话时发送
FTabForgeError Error;
auto Client = FTabForgeRealtimeClient::Create(RuntimeJson, MoveTemp(Options), Error);
if (!Client.IsValid()) { /* Error */ return; }
Client->OnState = [](ETabForgeRealtimeState State, const FTabForgeError* Failure) {
    // Connected：新会话，重新拉取状态；Resumed：会话延续；Closed：踢下线、顶号、版本不符或 Close()
};
Client->OnPush(TEXT("chatMessage"), [](const TSharedPtr<FJsonObject>& Message) { /* game thread */ }, Error);
Client->Connect(Error);
auto Login = MakeShared<FJsonObject>();
Login->SetStringField(TEXT("nickname"), TEXT("勇者"));
FTabForgeRealtimeCallbacks Callbacks;
Callbacks.OnResponse = [](const TSharedPtr<FJsonObject>& Reply) { /* playerId 等 64 位整数为十进制字符串 */ };
Callbacks.OnError = [](const FTabForgeError& Failure) { /* 业务错误码、timeout、session_lost、closed 等 */ };
Client->Request(TEXT("login"), MakeShared<FJsonValueObject>(Login), MoveTemp(Callbacks), Error);
Client->Notify(TEXT("move"), MoveData, Error);   // volatile 消息离线时返回 false，Error.Code 为 dropped
Client->Close();                                   // 结束会话，不再重连
```

- Create、Connect、Request、Notify、OnPush、Close 和析构都在 game thread 调用；所有回调在 game thread 的 core ticker 中执行，方法本身不会同步触发回调。销毁 Client 即结束会话，之后不再调用任何回调。
- 发送前用 runtime.json 的 `wireSchema` 校验并规范化消息，不合法的消息不会发出（`invalid_message`）；收到的响应与推送解码后再校验一次。请求超时默认取清单的 `timeout_ms`，`TimeoutMS` 只能缩短它。
- 会话丢失时，未发出的请求转入新会话，已发出但没有响应的请求以 `session_lost` 失败；关闭帧丢失时，客户端根据 WebSocket 关闭原因得知 `kicked` 等原因。
- IWebSocket 不提供协商后的子协议，因此客户端不检查它（服务器只接受客户端提供的子协议）。单条消息上限 1 MiB；默认最多 1024 个待确认帧（`MaxUnacked`），超出时新请求立即以 `overloaded` 失败。

- **KCP（UDP）**：`Url` 写成 `kcp://host:7001/ws/game` 即连接 Go 服务端的 `ServeKCP`（见 [KCP 传输](../../doc/realtime.md)），弱网下没有 TCP 的队头阻塞，其余用法不变。服务器设置了 `KCPKey` 时，把它的 32 字节 X25519 公钥填入 `FTabForgeRealtimeOptions::KcpServerKey`，所有数据报加密并认证服务器；留空则为明文 KCP。两端加密配置不一致时以 `protocol_error` 关闭，不会反复重连。UDP 收发使用引擎 `Sockets` 模块（Build.cs 已加入私有依赖），每帧在 ticker 中轮询，帧率很低时确认会相应变慢；主机名在游戏线程上解析，建议直接写 IP 地址。KCP 协议、加密（X25519、ChaCha20-Poly1305、SHA-256，纯 C++、常数时间，不依赖 OpenSSL）和握手都在 `TabForgeKcp.h` 中，与会话引擎一样不依赖引擎。

会话引擎（握手、序号与确认、恢复与重发、心跳、退避、超时）和 ProtoJSON↔Protobuf 编解码都在 `TabForgeRealtimeCore.h` 中：纯 C++17，不使用异常，也不依赖引擎；`Private/TabForgeRealtime.cpp` 只负责适配 IWebSocket（KCP 时为 FSocket）和 FJsonValue，并注册 ticker。

验证：设置 `CXX_BIN`（如 `clang++`）后运行 `go test ./protocol/realtime -run NativeRealtime`，会以 ASan/UBSan 构建 [sdk/native/tests/realtime_test.cpp](../native/tests/realtime_test.cpp)。测试包括模拟服务器的单元测试，与 Go 对 2800 条随机消息的编解码一致性检查，以及连接 Go 服务端的随机断线压测（每个请求恰好执行一次，推送不丢、不重、不乱序，volatile 与踢人均正确）。压测中的 WebSocket 由 Go 测试经 stdin/stdout 桥接。2026-11-11 在 Docker（Linux，g++ 14；clang 19 单元测试）中通过。KCP 由 `-run NativeKCP` 验证（同样以 ASan/UBSan 构建）：加密原语与 Go 生成的 300 个 X25519、200 个 ChaCha20-Poly1305 用例及固定密钥向量逐字节一致；两个 KCP 在 20% 丢包的虚拟链路上按序送达 300 条消息；会话引擎经 `Conversation` 连接 Go `ServeKCP`，经丢包、重复、乱序并随机换端口的 UDP 代理，明文与加密下做同样的压测（加密时另篡改 2% 的数据报，并确认令牌不以明文出现）；加密配置不一致时停止。UDP socket 由 Go 测试经 stdin/stdout 桥接，相当于引擎的 FSocket 层。2026-11-12 在本机（Windows，llvm-mingw clang 23）两组并行连续 6 次通过。**`FTabForgeRealtimeClient` 的引擎编译、IWebSocket 与 FSocket 行为（分片、关闭原因、线程）与移动/主机平台尚未实测。**
