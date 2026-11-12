# Godot 4 协议客户端

原生 GDScript 客户端支持 HTTP ProtoJSON 普通响应与 SSE 事件，不依赖 C# 或外部插件。最低目标 Godot 4.5.1；当前本地实际测试使用 macOS arm64 的官方 4.5.1 headless，CI 增加 Linux 4.5.1 验证任务。Godot 3 不支持此 SDK。

## 接入

将本目录的 protocol_client.gd、sse_parser.gd、utf8.gd、wire_validator.gd 复制到游戏的同一目录，再将生成包的 protocol.gd 放到项目内。生成文件携带接口元数据、schema hash 和字段验证规则，不注册全局 class_name。

协议生成、七类产物用途和 Go 后端注册见 [前后端指南](../../doc/protocol-integration.md)。Godot 使用 protocol.gd 的 CONTRACT，Unity/Unreal 使用 runtime.json；它们来自同一个生成包，发布时与后端保持同一版本/hash。

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

请求和响应都按 Go protojson 的输入规则验证：整数字段接受 JSON 数字或数字字符串，只要其十进制值精确为整数且在范围内（1.0、1e2、"7" 可以，1.5 不行）；64 位整数建议仍用字符串，避免 Godot float 丢精度；枚举使用协议名称或 int32 数字；bytes 接受标准/URL Base64，可省略填充；null 字段视为未设置（google.protobuf.Value 除外）；数组、map、required、oneof 和 well-known types 有对应检查。JSON 由 wire_validator.gd 内的严格解析器读取：拒绝重复键（包括转义后相同的键，如 "x" 与 "\u0078"）、扩展语法、孤立代理项与尾随内容，深度上限 64，网络消息最多 100000 个 JSON 值。信号在场景处理线程发出，回调应尽快返回。

## 数据文件

data_schema.gd 与 wire_validator.gd 是导入到游戏工程的数据加载器（不依赖网络客户端）。read_file/decode_bytes 先做严格 UTF-8 校验（拒绝过长编码、代理项、截断序列和 BOM），再解析 JSON。数据文件使用所有 TabForge 运行时一致的数据包预算：深度 64、最多 10000000 个 JSON 值（每个标量和容器各算 1 个，键不计），因此 20000 行的配置表在 Go、Node、Java、Python、Unity、Unreal 与 Godot 中同样能加载。返回值规范化为标准 ProtoJSON：32 位整数和枚举数字为 int，64 位整数为十进制 String，其它数字为 float，bytes 为带填充的标准 Base64，null 字段被移除，整数 map 键为规范十进制。

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

## 实时长连接

`realtime_client.gd` 是 WebSocket 通道客户端（基于 `WebSocketPeer`，桌面、移动与 Web 导出均可使用），协议与可靠性语义见 [实时长连接](../../doc/realtime.md)。默认使用二进制 Protobuf 帧（`realtime_codec.gd` 依据生成的 `CONTRACT.codecSchema` 与 ProtoJSON Dictionary 互转），`{"encoding": "json"}` 使用 JSON 帧。

```gdscript
const Realtime = preload("res://addons/tabforge/realtime_client.gd")
const PROTOCOL = preload("res://generated/protocol.gd")

var client := Realtime.new()

func _ready() -> void:
	add_child(client) # _process 中轮询，所有信号都在主线程触发
	client.configure("wss://game.example.com/ws/game", PROTOCOL.CONTRACT, "game", {"token": func(): return login_token})
	client.state_changed.connect(func(state: String, error: Dictionary) -> void:
		# connected：新会话，重新拉取状态；resumed：原会话继续；closed：被踢、顶号等，不再重连
		print(state, error))
	client.on_push("chatMessage", func(message: Dictionary) -> void: print(message.text))
	client.start()
	var login := client.request("login", {"nickname": "勇者"})
	login.completed.connect(func(reply: Dictionary) -> void: print(reply.playerId))
	login.failed.connect(func(error: Dictionary) -> void: print(error.code)) # session_lost 表示结果未知
	client.notify("move", {"position": {"x": 1.0, "y": 2.0}, "tick": 1}) # volatile：掉线时返回 false
```

- 消息是 ProtoJSON 形态的 Dictionary：64 位整数为十进制字符串，枚举为名称。发送前按 `wireSchema` 校验并规范化，失败的请求在下一帧以 `invalid_message` 触发 `failed`。
- 节点离开场景树时关闭会话；需要跨场景保持会话时把它放在自动加载（Autoload）节点下。
- 64 位整数在 GDScript 中按两个 32 位分量处理，`uint64` 最大值等极端值与 Go 一致。
- **KCP（UDP）**：地址写成 `kcp://host:7001/ws/game` 即连接 Go 服务端的 `ServeKCP`（见 [KCP 传输](../../doc/realtime.md)），弱网下没有 TCP 的队头阻塞，其余用法不变。服务器设置了 `KCPKey` 时，在选项中传入 `"kcp_server_key": Marshalls.base64_to_raw("...")`（服务器的 32 字节 X25519 公钥），所有数据报加密并认证服务器；不传则为明文 KCP。两端加密配置不一致时以 `protocol_error` 关闭，不会反复重连。桌面与移动导出可用；Web 导出没有 UDP，`configure` 返回 `ERR_UNAVAILABLE`。实现为纯 GDScript（`kcp_core.gd`、`kcp_crypto.gd`、`kcp_socket.gd`），在 `_process` 中轮询，不开线程；握手中的 X25519（每次约 40 ms）放在 `WorkerThreadPool` 上，重连不会卡住主线程。加密由 GDScript 完成，满载数据报约 0.7 ms，适合游戏消息量，不适合大批量传输。

验证：`go test ./protocol/realtime` 在设置 `GODOT_BIN` 后以 headless 方式运行 [tests/realtime.gd](tests/realtime.gd)：模拟服务器单元测试、与 Go 对 2800 条随机消息的编解码一致性，以及随机断线代理下连接 Go 服务端的压测（每个请求恰好执行一次、推送不丢不重不乱序、踢人在断线时也能送达），两种编码。2026-11-09 在 Docker（Linux）中用 Godot 4.5.1 连续 10 轮通过。KCP 由 `TestGodotKCPClient` 验证：加密原语与 Go 生成的用例和固定密钥向量逐字节一致、两个 GDScript KCP 在 20% 丢包的虚拟链路上按序送达、经丢包与换端口的 UDP 代理连接 `ServeKCP`（明文与加密、两种编码），以及加密配置不一致时停止；2026-11-12 在本机（Windows，Godot 4.5.1）连续 5 轮通过。移动导出、Web 导出与编辑器内运行尚未实测。
