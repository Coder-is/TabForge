# Godot 4 协议客户端

原生 GDScript 客户端支持 HTTP ProtoJSON 普通响应与 SSE 事件，不依赖 C# 或外部插件。最低目标 Godot 4.5.1；当前本地实际测试使用 macOS arm64 的官方 4.5.1 headless，CI 增加 Linux 4.5.1 验证任务。Godot 3 不支持此 SDK。

## 接入

将本目录的 protocol_client.gd、sse_parser.gd、utf8.gd、wire_validator.gd 复制到游戏的同一目录，再将生成包的 protocol.gd 放到项目内。生成文件携带接口元数据、schema hash 和字段验证规则，不注册全局 class_name。

```gdscript
extends Node
const Client = preload("res://tabforge/protocol_client.gd")
const Contract = preload("res://generated/protocol.gd")
var client: Node

func _ready() -> void:
    client = Client.new()
    add_child(client)
    assert(client.configure("https://api.example.com", Contract.CONTRACT) == OK)
    var request = client.request("chatComplete", {
        "prompt": "你好", "conversationId": "18446744073709551615"
    }, {"token": "application_token"})
    request.completed.connect(func(data): print(data.get("text", "")))
    request.failed.connect(func(error): print(error.code, error.message))

    var stream = client.stream("chatStream", {"prompt": "你好"})
    stream.event_received.connect(func(event):
        if event.payload.has("delta"):
            print(event.payload.delta.get("text", ""))
        if event.payload.has("failed"):
            print(event.payload.failed.get("message", ""))
    )
    stream.failed.connect(func(error): print(error.code, error.message))
    # stream.cancel() cancels an active request.
```

request 方法避免与 Godot Object.call 冲突。Request 是 RefCounted 句柄，包含 done、result、error、request_id 和 cancel。completed 表示普通响应已收到，或流已收到声明的结束事件；业务 failed 事件仍通过 event_received 发出，需要应用处理。failed 信号表示传输或验证失败。所有结果延迟到下一轮处理，调用者可以先连接信号；移除客户端节点会取消请求。

请求和响应都按 ProtoJSON 规则验证：64 位整数保持字符串，不要转为 Godot int；枚举使用协议名称或 int32；bytes 为 Base64；数组、map、required、oneof 和 well-known types 有对应检查。信号在场景处理线程发出，回调应尽快返回。

## 网络与限制

HTTPClient 以非阻塞 poll 驱动；HTTPS 使用 TLSOptions.client 的证书校验。整个请求超时来自接口清单。普通响应默认最多 1 MiB，SSE 默认每帧最多 1 MiB 字节，可调整客户端 max_response_bytes / max_frame_bytes，并与服务端限制保持一致。流式解析先按换行字节切分再解码 UTF-8，因此字符跨网络分片不会被拆坏。未收到结束事件即断开会报 incomplete_stream；没有自动重试、续传或供应商回放。

原生平台支持 SSE。**Godot Web 导出当前明确拒绝此 SDK 的 SSE 调用**，需要另接 JavaScript fetch 流桥；不能把原生验证结果当作 Web 流式支持。Godot Android 要在导出预设开启 INTERNET 权限，移动平台发布和 TLS 真实服务仍需项目实际验证。协议工具不内置模型供应商适配器。

网络 API 参考 [Godot HTTPClient 文档](https://docs.godotengine.org/en/4.5/classes/class_httpclient.html)。

## 验证

```bash
godot --headless --path sdk/godot --script tests/unit.gd
GODOT_BIN=/absolute/path/to/godot go test -race -count=1 ./protocol/httptransport -run Godot
```

Go 集成测试启动真实 HTTP 服务，验证普通/SSE、Unicode、大整数、输入类型错误、取消、超时、提前结束和协议身份不匹配。没有设置 GODOT_BIN 的 Go 测试会跳过引擎验证；CI 的独立 Godot 任务会下载固定版本、验证 SHA-512 后执行这些测试。
