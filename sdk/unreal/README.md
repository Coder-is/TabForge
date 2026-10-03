# Unreal C++ 网络适配

`TabForgeProtocol.uplugin` 提供 Runtime 模块，目标 UE 5.5+ 的 HTTP API；调用方 Build.cs 添加 `TabForgeProtocol`、`Json`。安装到项目 Plugins/TabForgeProtocol 后构建。此插件提供 C++ API，尚未提供 Blueprint 节点。

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

HTTP 接收使用 `SetResponseBodyReceiveStream(FArchive)`，Serialize 在 HTTP 线程只放入有限队列，游戏线程增量分帧/校验/回调，不依赖完整响应缓存。默认普通正文与单帧最大 1 MiB 字节，队列最大 1 MiB / 128 块，同时最多 16 请求。消费过慢显式报 backpressure；底层不支持接收流时返回 unsupported_transport。UTF-8、JSON 重复键/语法、字段类型、64 位数字字符串、oneof、schema、request ID 和顺序均检查。全程 deadline、CancelAll 和终止事件会停止 HTTP。

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
