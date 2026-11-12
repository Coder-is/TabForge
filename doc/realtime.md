# 实时长连接（WebSocket 通道）

HTTP 端点适合登录、商城、邮件这类一问一答的业务；房间、战斗、聊天、组队需要服务器主动推送和低延迟，使用本文的**通道（channel）**。一条 WebSocket 连接同时承载三类消息：

| 类型 | 方向 | 是否应答 | 典型用途 |
| --- | --- | --- | --- |
| `request` | 客户端 → 服务器 | 一次响应或错误 | 登录、进房、购买 |
| `push` | 服务器 → 客户端 | 无 | 战斗开始、聊天消息、红点、公告 |
| `notify` | 客户端 → 服务器 | 无 | 移动同步、表情、心跳外的轻量上报 |

已实现：Go 服务端 `protocol/realtime`（WebSocket，以及可选的 KCP/UDP 传输，见第 4 节）、Go 客户端（机器人/压测/服务间）、TypeScript 客户端（浏览器、Node、Cocos/Laya 的标准 WebSocket，以及微信 `wx.connectSocket`）、Unity C# 客户端（`ClientWebSocket`：Standalone/Android/iOS；WebGL 经 jslib 桥接浏览器 WebSocket）、Godot 客户端（`WebSocketPeer`）、Unreal C++ 客户端（引擎 WebSockets 模块）。Lua 的长连接客户端尚未实现，见文末。

## 1. 在清单中声明通道

在 `contract.json` 中加入 `channels`（可以和 `endpoints` 共存，也可以只有通道）。完整示例见 [examples/realtime](../examples/realtime/Protocols/contract.json)：

```json
"channels": [{
  "id": "game",
  "path": "/ws/game",
  "auth": "bearer",
  "heartbeat_ms": 5000,
  "resume_window_ms": 60000,
  "messages": [
    { "id": 1,   "name": "login", "kind": "request", "rpc": "tabforge.game.Game.Login", "timeout_ms": 5000 },
    { "id": 100, "name": "move",  "kind": "notify",  "type": "tabforge.game.Move", "volatile": true },
    { "id": 201, "name": "chatMessage", "kind": "push", "type": "tabforge.game.ChatMessage" }
  ]
}]
```

| 字段 | 规则 |
| --- | --- |
| `id` / `path` | 通道标识与 WebSocket 路径；路径不能与 HTTP 端点重复 |
| `auth` | `none` 或 `bearer`；令牌放在握手帧 `hello` 中（浏览器 WebSocket 不能设置 Authorization 头） |
| `heartbeat_ms` | 1000–600000。客户端按此间隔发 `ping`；服务器 3 倍间隔 +1 秒收不到任何帧即断开，客户端 2 倍间隔 +1 秒收不到即重连 |
| `resume_window_ms` | 0–3600000。断线后会话保留多久等待恢复；0 表示断线即结束会话 |
| 消息 `id` | 1–65535，通道内唯一，是线上标识；**发布后不能改号或复用** |
| 消息 `name` | 生成代码使用的名字，通道内唯一 |
| `request` | 引用一个 unary RPC，必须有 `timeout_ms` |
| `push` / `notify` | 引用一个消息类型 `type`；`volatile: true` 表示“只要最新值”的消息，见第 3 节 |

通道消息不支持 `google.protobuf.Any`、`Struct`、`Value`、`ListValue`、`FieldMask` 和 proto2 group（二进制与 ProtoJSON 之间无法在没有类型注册表的情况下互转）；`Timestamp`、`Duration`、包装类型和 `Empty` 均支持。

生成方式不变：`-protocol=... -protocol_out=...` 或项目模式。存在通道时，`types.ts` 额外导出 `ChannelTypes`、`channels`、`codecSchema`，`runtime.json` 额外包含 `channels` 与 `codecSchema`，`PROTOCOL.md` 增加通道表。没有通道的清单输出与 schema hash 均不变。

`-protocol_against` 对通道检查：删除通道或消息、修改 id/kind/rpc/type/volatile、复用旧 id、缩短超时、修改路径或鉴权、修改载荷字段都是破坏性变更；新增 push 不是（所有客户端忽略未知 push id），新增 request/notify 也不是。

## 2. 线上格式

WebSocket 子协议二选一：

| 子协议 | WebSocket 帧 | 载荷 |
| --- | --- | --- |
| `tabforge.realtime.v1.proto` | 二进制 | Protobuf 二进制 |
| `tabforge.realtime.v1.json` | 文本 | ProtoJSON 对象 |

每个 WebSocket 消息是一个帧（Frame）。二进制形式等价于下面的 Protobuf 定义；JSON 形式使用相同字段的小驼峰名，`kind` 写名称（`"request"`），`seq`/`ack`/`replyTo` 写十进制字符串，`payload` 是 ProtoJSON 对象，未知成员和重复成员都会被拒绝。

```protobuf
message Frame {
  uint32 kind = 1;      // 1 hello 2 welcome 3 request 4 response 5 push 6 notify 7 ping 8 pong 9 ack 10 close
  uint64 seq = 2;       // 可靠帧序号（每个方向从 1 递增）；控制帧和 volatile 帧为 0
  uint64 ack = 3;       // 已连续收到的对方最大 seq
  uint32 message = 4;   // 清单中的消息 id
  uint64 reply_to = 5;  // response：对应 request 的 seq
  bytes payload = 6;
  Error error = 7;      // response 失败或 close 原因
  Hello hello = 8;
  Welcome welcome = 9;
}
message Error   { string code = 1; string message = 2; bool retryable = 3; }
message Hello   { string protocol_version = 1; string schema_hash = 2; string token = 3; string session_id = 4; string resume_token = 5; }
message Welcome { string session_id = 1; string resume_token = 2; bool resumed = 3; uint32 heartbeat_ms = 4; uint32 resume_window_ms = 5; }
```

解码时跳过未知字段，便于以后扩展帧；各类帧必须满足的字段组合见 [frame.go](../protocol/realtime/frame.go) 中的 `validate`，所有实现执行同一套规则。

接收方忽略长度为 0 的 WebSocket 消息：它们不是帧（Godot 4.5 的 `WebSocketPeer` 在断线重连时偶尔会发出）。服务器关闭 WebSocket 时把错误码写进关闭原因（例如 `kicked`）；如果平台在连接关闭时丢掉了最后的 `close` 帧（Godot 4.5 实测如此），客户端仍能据此得知原因。

## 3. 会话与可靠性

**握手。** 客户端连接后第一帧必须是 `hello`，`ack` 填自己已收到的服务器最大序号。服务器检查协议版本和 schema hash（默认要求完全一致），新会话校验令牌，然后回复 `welcome`（含会话 id、恢复令牌、心跳间隔），失败则回复 `close` 并断开。

**序号与确认。** request、response、push、notify 是可靠帧，每个方向独立编号。发送方保留帧直到对方确认；任何帧都携带 `ack`，空闲时 50 ms 内补发独立的 `ack` 帧。接收方丢弃 `seq` 小于等于已收到序号的重复帧，遇到跳号视为协议错误。

**断线恢复。** 连接断开后，会话在 `resume_window_ms` 内保留。客户端自动重连（指数退避加随机抖动），用 `session_id + resume_token` 恢复；双方按对方的 `ack` 重发未确认的帧。因此在会话存续期间：

- 每个 request 在服务器上**最多执行一次**，响应恰好送达一次；断线时购买请求不会被重复扣款。
- push 和 notify 恰好送达一次，并按发送顺序处理。
- 同一会话的 request/notify 默认按到达顺序逐个处理（`Server.Concurrency = 1`），处理玩家状态时无需额外加锁。

**会话丢失。** 超出恢复窗口、服务器重启或主动结束会话后，恢复会得到 `session_expired`，客户端立即建立新会话并报告状态 `connected`（区别于 `resumed`），游戏应重新拉取状态。此时：

- 从未写到任何连接上的请求自动转入新会话发送；
- 已经发出但没有收到响应的请求以 `session_lost` 失败：服务器可能已经执行过，也可能没有，业务需要查询或用业务幂等键处理。

**volatile 消息。** 标记 `volatile` 的 push/notify 不编号、不缓存、不重发：断线时直接丢弃（Go 返回 `ErrDropped`，TS 的 `send.xxx()` 返回 `false`），发送队列满时也会丢弃。适合位置、血量等只关心最新值的高频状态，不要用于需要必达的数据。

**关闭原因。** `close` 帧的 `retryable` 决定客户端是否重连：

| code | retryable | 含义 |
| --- | --- | --- |
| `session_expired` | 是 | 会话不可恢复，客户端新建会话 |
| `server_shutdown` | 是 | 服务器正在停机，客户端重连其他实例 |
| `overloaded` | 是 | 客户端长时间不确认，或待处理消息过多 |
| `kicked`、`duplicate_login`、`unauthorized`、`rate_limited`、`protocol_error`、`version_mismatch`、`schema_mismatch` | 否 | 客户端停止并报告 `closed` |

如果 `close` 帧恰好随断线丢失，服务器会在恢复窗口内记住不可重试的结束原因；客户端恢复时收到的是原始原因（例如 `kicked`），不会悄悄建立新会话。

## 4. Go 服务端

```go
contract, _ := protocol.Load("Generated/protocol/contract.json")
server := realtime.NewServer(contract)
server.Authenticate = func(ctx context.Context, channel, token string) (realtime.Identity, error) {
    userID, err := verify(token) // 应用自己的令牌校验
    return realtime.Identity{UserID: userID}, err
}
server.SingleSessionPerUser = true // 新登录顶掉旧会话（duplicate_login）

// 类型化处理函数：参数是 protoc-gen-go/项目模式生成的 Go 类型，注册时检查与清单一致。
realtime.OnRequest(server, "game", "buy", func(ctx context.Context, s *realtime.Session, req *gamepb.BuyRequest) (*gamepb.BuyReply, error) {
    if notEnough { return nil, &realtime.Error{Code: "not_enough_gold", Message: "金币不足"} }
    return &gamepb.BuyReply{Gold: left}, nil
})
realtime.OnNotify(server, "game", "move", func(ctx context.Context, s *realtime.Session, m *gamepb.Move) { ... })

s.Push("chatMessage", msg)          // 推送给一个会话
room := realtime.NewGroup()          // 房间/公会/对局；会话结束自动移出
room.Add(s)
room.Push("playerMoved", moved, func(other *realtime.Session) bool { return other == s }) // 每种编码只序列化一次
s.Close(realtime.CodeKicked, "违规")  // 踢人，客户端不会重连

mux.Handle("/ws/", server)
http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second} // 不要设置 WriteTimeout
server.Shutdown(ctx) // 通知所有客户端 server_shutdown 并等待处理函数结束
```

不想使用生成的 Go 类型时，用 `server.HandleRequest` / `HandleNotify` 接收 `dynamicpb` 消息。会话上的 `s.Values` 存放应用状态，`s.Context()` 在会话结束时取消；处理函数的 ctx 还带有消息的 `timeout_ms` 截止时间。断线**不会**取消正在运行的处理函数，结果在客户端恢复后送达。

| 选项 | 默认 | 说明 |
| --- | --- | --- |
| `OriginPatterns` | 空（只允许同源） | 浏览器跨域来源白名单；非浏览器客户端不受影响 |
| `RequireSchemaHash` | true | 握手必须携带一致的 schema hash |
| `MaxFrameBytes` | 1 MiB | 单帧上限，双向；启用压缩时按解压后大小计算 |
| `Compression` / `CompressionThreshold` | 关闭 / 库默认（128 或 512 字节） | permessage-deflate，见下文“消息压缩” |
| `MaxUnackedFrames` / `MaxUnackedBytes` | 4096 / 8 MiB | 每会话待确认缓存；超出以 `overloaded` 关闭慢客户端 |
| `MaxQueuedMessages` | 1024 | 每会话待处理的 request/notify |
| `MaxFramesPerSecond` | 0（不限） | 单连接读帧速率，超出以 `rate_limited` 关闭 |
| `Concurrency` | 1 | 同一会话同时处理的请求数；大于 1 时响应可能乱序完成（仍按 `replyTo` 对应） |
| `HandshakeTimeout` / `WriteTimeout` | 10 s | 握手与单次写入超时 |
| `OnSessionStart` / `OnSessionEnd` / `Observe` | — | 会话生命周期和每条消息的结果（不含令牌和载荷），用于日志与指标 |
| `Store` / `ExportSession` / `ImportSession` | 无 | 停机时把会话交给下一个实例，见下文“跨实例会话交接” |

**消息压缩。** `server.Compression = realtime.CompressionContextTakeover`（或 `CompressionNoContextTakeover`）开启 permessage-deflate（RFC 7692），只对超过 `CompressionThreshold` 的消息生效。压缩由握手协商：客户端不支持时自动不压缩，开启后不会影响任何已有客户端。

| 模式 | 压缩率 | 每连接内存 | 适用 |
| --- | --- | --- | --- |
| `CompressionContextTakeover` | 高（跨消息复用 32 KiB 窗口，JSON 编码收益最大） | 固定约 1.2 MB 写缓冲 | 连接数适中、消息重复度高 |
| `CompressionNoContextTakeover` | 较低（每条消息独立压缩） | 写缓冲取自共享池 | 大量空闲长连接 |

客户端支持情况：浏览器（Safari 除外）、Node 22+、Go 客户端（`Client.Compression`）会协商压缩；微信需 `wechatSocketFactory(wx, { perMessageDeflate: true })`；Unity `ClientWebSocket`、Godot `WebSocketPeer` 不协商，保持原样。二进制 Protobuf 编码本身已很紧凑，开启前请用实际消息做基准测试；`MaxFrameBytes` 按解压后大小检查，解压后超限的消息会被拒绝并断开连接。

**跨实例会话交接（滚动发布）。** 默认会话只保存在本进程内存中；多实例部署时，负载均衡需要按会话把恢复请求路由回同一实例（例如按 `session_id` 粘性），否则恢复会得到 `session_expired` 并新建会话。设置 `server.Store` 后，`Shutdown` 会把会话交给下一个实例，客户端看到的是 `resumed`，不会丢失任何东西：

1. 停止接收新的 request/notify：既不执行，也不确认。客户端恢复后会把它们重发给新实例。
2. 等待已排队和正在运行的处理函数结束，它们的响应进入待确认缓存（受 `Shutdown` 的 ctx 限制，超时的会话按原方式结束）。
3. 冻结会话，把序号、待确认帧和 `ExportSession` 返回的应用状态写入存储，写完后才发送 `server_shutdown` 通知客户端重连。
4. 新实例收到恢复请求时，从存储中原子地取出快照（取出即删除），先调用 `ImportSession`，再调用 `OnSessionStart`（此时 `s.Restored()` 为 true），然后继续这个会话。

```go
server.Store = redisStore{rdb}                 // 任何支持“取出即删除”的存储
server.ExportSession = func(s *realtime.Session) ([]byte, error) { return json.Marshal(playerState(s)) }
server.ImportSession = func(s *realtime.Session, id *realtime.Identity, state []byte) error {
    return restorePlayer(s, id, state)        // 恢复 Values，按需填写 id.Data
}

type redisStore struct{ rdb *redis.Client }   // github.com/redis/go-redis/v9，Redis 6.2+
func (r redisStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
    return r.rdb.Set(ctx, key, value, ttl).Err()
}
func (r redisStore) Take(ctx context.Context, key string) ([]byte, error) {
    v, err := r.rdb.GetDel(ctx, key).Bytes()
    if err == redis.Nil { return nil, nil }
    return v, err
}
```

- `Take` 必须是原子的“取出并删除”，保证一个会话只会在一个实例上继续，请求不会被执行两次。存储的键由会话 id 和恢复令牌的 SHA-256 生成：没有令牌既读不到快照，也无法删除它。
- 如果客户端在旧实例排空期间自己断线并先连上新实例，新实例会看到“交接中”标记，回复可重试的 `server_shutdown`，客户端退避后再恢复；标记带有固定截止时间（最长 30 秒，不超过 `Shutdown` 的 ctx），旧实例中途崩溃时也不会让客户端无限重试。
- 只有通过 `Shutdown` 交接的会话可以跨实例继续。进程崩溃时内存中的会话无法保存，客户端会得到 `session_expired`（与没有 `Store` 时相同）。存储写入失败时也按原方式结束，并记录 `handoff_failed` 事件。
- 被踢下线、关闭帧又随断线丢失的会话，结束原因也会一并写入存储，客户端在新实例上恢复时仍会收到 `kicked`。
- `Values`、`Identity.Data` 和房间（`Group`）成员关系只存在于本进程，需要通过 `ExportSession`/`ImportSession` 携带，或在 `OnSessionStart` 中重建。交接后旧实例对该会话调用 `OnSessionEnd`，原因码为 `handed_off`。快照完成后在旧实例上的 `Push` 返回 `ErrSessionClosed`。
- 交接只用于同一契约（同一 schema hash）的实例之间，例如发布服务端代码。契约变化时新实例会拒绝旧快照，客户端握手本身也会因 `schema_mismatch` 失败，需要随新客户端一起发布。

**KCP（UDP）传输。** 弱网（移动网络丢包、高延迟）下，TCP 的队头阻塞会让一次丢包拖住后面所有消息。`ServeKCP` 在 UDP 上用 KCP 承载同样的帧，可与 WebSocket 同时运行：

```go
pc, _ := net.ListenPacket("udp", ":7001")
server.KCPKey = kcpKey                     // *ecdh.PrivateKey（X25519），开启加密，见下文；nil 为明文
go server.ServeKCP(pc)                     // 与 mux.Handle("/ws/", server) 共用会话
// 停机：先 server.Shutdown(ctx)（发送 server_shutdown 并交接），再 pc.Close()

client, _ := realtime.NewClient(contract, "game", "kcp://game.example.com:7001/ws/game") // Go 客户端
client.KCPServerKey = kcpPublicKey         // 服务器公钥，随客户端发布
```

生成与加载密钥（私钥只放在服务器上，例如密钥管理服务或环境变量）：

```go
key, _ := ecdh.X25519().GenerateKey(rand.Reader)       // 一次性生成
privateB64 := base64.StdEncoding.EncodeToString(key.Bytes())
publicB64 := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())

raw, _ := base64.StdEncoding.DecodeString(privateB64)  // 服务器启动时
kcpKey, _ := ecdh.X25519().NewPrivateKey(raw)
pub, _ := base64.StdEncoding.DecodeString(publicB64)   // 客户端
kcpPublicKey, _ := ecdh.X25519().NewPublicKey(pub)
```

- 第 2–3 节的帧、握手、序号、恢复和关闭原因完全相同，只是换了承载层；会话在两种传输间共享，同一编码下客户端可以通过任一传输恢复会话。`MaxFrameBytes` 同样生效；不做 permessage-deflate 压缩，`OriginPatterns` 不适用。
- 线上格式（版本 1）：标准 KCP（与 ikcp.c 2.1.1 兼容；nodelay、10 ms 间隔、快速重传 2、关闭拥塞控制、窗口 256、MTU 1200、死链 40 次重传），conv 由客户端随机生成。每条 KCP 消息首字节低 7 位是类型（0 open、1 二进制、2 文本、3 close），`0x80` 表示同一消息还有后续分片（每片最多 16 KiB）。客户端先发 open `路径\n子协议`，服务器回 open `子协议` 或 close `原因`。
- 5 字节数据报 `conv + 0xFF` 是重置：服务器收到未知会话（NAT 换了端口、服务器重启）的数据时回复它，客户端立即重连并恢复会话，不必等超时。客户端空闲 1 秒发送 `conv + 0xFE` 保活，NAT 映射变化在空闲时也能被发现。
- 负载均衡需按 UDP 四元组（或 conv）把数据报路由到同一实例；NAT 重绑定后的新地址会被重置，客户端通过恢复回到原会话，不会重复执行请求。
- **加密。** 设置 `Server.KCPKey` 后，`ServeKCP` 只接受加密会话。客户端用预先配置的服务器公钥认证服务器（类似证书固定，可防中间人），在 KCP 之前完成 1 个往返的握手：客户端发 hello（`conv + 0xFD + 版本 1 + 客户端临时公钥`，补零到 64 字节），服务器回 welcome（`conv + 0xFC + 服务器临时公钥 + 认证标签`，53 字节，不会比 hello 大）；hello 丢失时客户端每 250 ms 重发，服务器对重复的 hello 回复相同的 welcome；握手完成前收到另一个合法的 hello（前一个在路上损坏），服务器丢弃旧的半开会话、改用新的。双方用两次 X25519（双方临时密钥、客户端临时密钥与服务器长期密钥）经 HKDF-SHA256 派生两个方向各自的 ChaCha20-Poly1305（RFC 8439）密钥，具有前向保密：服务器私钥日后泄露，也解不开之前录下的流量。之后每个数据报为 `conv + 0xFB + 8 字节计数器 + 密文`，连 KCP 的序号、确认和窗口一起加密认证；被篡改或伪造的数据报按丢包处理，重放的数据报会被 KCP 按序号丢弃。每个数据报增加 29 字节，KCP 分段相应缩小。选用 ChaCha20-Poly1305 而不是 AES-GCM，是因为 Unity（netstandard2.1）和 Godot 没有内置的 AEAD，而它可以用纯代码安全地（常数时间）实现。其他客户端的实现者可用 `protocol/realtime/testdata/kcp_crypto_vectors.json` 中固定密钥的握手、密钥与数据报逐字节核对（各字段的计算见 `kcpcrypto.go` 开头的注释）。
- 两端配置不一致时客户端不重试，以 `protocol_error` 停止：明文客户端连加密服务器会收到 `conv + 0xFA`（`ErrKCPEncryptionRequired`），加密客户端连明文服务器，每个 hello 都会收到重置，连续 3 次后停止（`ErrKCPEncryptionUnsupported`；只收到一两次重置不会停止，以免损坏的数据报或伪造的重置让客户端放弃）。公钥配错时客户端认不出 welcome，表现为一直连不上（`connecting`/`disconnected`）。更换服务器密钥需要先发布带新公钥的客户端，目前每个 `ServeKCP` 只有一个密钥，换钥期间可以在另一个端口用新密钥运行第二个 `ServeKCP`。
- 重置帧（`0xFF`）和拒绝帧（`0xFA`）不加密：失去会话的一方已没有密钥。能看到流量的攻击者可以伪造它们迫使客户端重连，但会话会通过恢复继续，请求也不会重复执行；这与直接丢弃数据报的效果相同。
- 不设置 `KCPKey` 时**不加密**：令牌、恢复令牌和载荷都是明文，能看到流量的人可以冒用会话，只适合内网、VPN/专线或游戏加速器隧道。
- 每个 `ServeKCP` 最多 `MaxKCPConversations`（100000）个会话。未认证的 open（或 hello）在 `HandshakeTimeout` 内占用一个名额，伪造源地址的洪泛可以占满它；加密时每个新 hello 还要在服务器上做 3 次 X25519 运算，需要在网络层限速。所有回复都不比触发它的数据报大，不会放大反射流量。
- 目前只有 Go 客户端支持 `kcp://`；TypeScript（浏览器无 UDP）、Unity、Godot、Unreal 客户端仍使用 WebSocket。

## 5. TypeScript 客户端

```ts
import { RealtimeClient } from "@tabforge/protocol-runtime/realtime";
import { channels, codecSchema, protocolVersion, schemaHash, wireSchema } from "./Generated/protocol/types";
import type { ChannelTypes } from "./Generated/protocol/types";

const client = new RealtimeClient<ChannelTypes["game"]>({
  url: "wss://game.example.com/ws/game",
  channel: channels.game,
  protocolVersion, schemaHash, wireSchema,
  codecSchema,                       // 提供即使用二进制 Protobuf；省略或 encoding: "json" 使用 JSON
  token: async () => loginToken,      // 只在新会话时发送
  onState: (state, error) => { /* connected | resumed | disconnected | closed */ },
});
client.on("chatMessage", m => ui.addChat(m));      // 类型来自 ChannelTypes
await client.connect();
const me = await client.api.login({ nickname: "勇者" });
client.send.move({ position: { x: 1, y: 2 }, tick }); // volatile，掉线时返回 false
client.close();
```

- 请求、推送和通知使用与 HTTP 相同的 ProtoJSON 形态类型（64 位整数是十进制字符串，枚举是名称）。发送前用 `wireSchema` 校验并规范化，不合法的消息不会上网；二进制模式下 `codecSchema` 负责与 Protobuf 互转，结果与 Go `protojson` 输出一致。
- 请求超时默认取清单的 `timeout_ms`，可用 `{ timeoutMS, signal }` 缩短或取消。超时只代表客户端不再等待，服务器可能仍会执行。
- 待确认帧默认最多 1024 个（`maxUnacked`），超出时新请求立即以 `overloaded` 失败，不会无限堆积。
- push 处理函数按到达顺序同步调用；抛出的异常交给 `onError`，不影响后续消息。
- 运行环境：标准 WebSocket（浏览器、Node 22+、Cocos Creator、Laya）用默认的 `webSocketFactory()`；微信小程序/小游戏传入 `socket: wechatSocketFactory(wx)`（[sdk/wechat/realtime.ts](../sdk/wechat/realtime.ts)）。其他平台实现 `SocketFactory` 的 `send/close` 与 `open/message/close` 三个回调即可。编解码不依赖 TextEncoder、BigInt64Array；需要 BigInt。

## 6. Go、Unity、Godot 与 Unreal 客户端

`realtime.NewClient(contract, "game", "ws://host/ws/game")` 提供与 TS 相同的重连、恢复和重发逻辑：`Connect`、`realtime.Call[*gamepb.LoginReply](ctx, c, "login", req)`、`c.Notify`、`realtime.HandlePush[*gamepb.ChatMessage](c, "chatMessage", fn)`、`OnState`。适合机器人、压测和服务间连接。

Unity（C#）使用 `RealtimeClient` 与 `TabForgeRealtime` 组件，消息为 ProtoJSON `JObject`，所有回调在主线程执行，用法见 [Unity SDK](../sdk/unity/README.md#实时长连接)。WebGL 播放器自动使用随包提供的浏览器 WebSocket 桥（jslib），无需额外配置。

Godot（GDScript）使用 `realtime_client.gd` 节点，消息为 ProtoJSON Dictionary，在 `_process` 中轮询，用法见 [Godot SDK](../sdk/godot/README.md#实时长连接)。

Unreal（C++，UE 5.5+）使用 `FTabForgeRealtimeClient`（`TabForgeRealtime.h`），基于引擎 `WebSockets` 模块，消息为 ProtoJSON `FJsonValue`，固定使用二进制 Protobuf 子协议，回调在 game thread 的 core ticker 中执行，用法见 [Unreal SDK](../sdk/unreal/README.md#实时长连接)。会话逻辑与编解码在不依赖引擎的 `TabForgeRealtimeCore.h` 中，引擎层只做 WebSocket 与 `FJsonValue` 适配。

## 7. 验证

| 检查 | 内容 |
| --- | --- |
| `go test ./protocol/realtime` | 两种编码下的请求/推送/通知；随机切断 TCP 的代理下持续 2 秒并发购买（每轮约 1–3 万请求、40+ 次断线），校验每个请求恰好执行一次、推送不丢不重不乱序、会话不被替换；会话过期后的 `session_lost` 与未发出请求的转移；踢人、顶号、踢人帧丢失；握手拒绝；8 类协议违规；慢消费者；心跳超时；停机；房间广播；类型不匹配；跨实例交接：A→B→C 两次替换服务器并持续随机断线下购买仍恰好一次、计数经 Export/Import 延续、推送有序且会话不重建，运行中的处理函数结果经快照送达，排空期间提前到达新实例的恢复被要求重试，伪造令牌无法取走或删除快照，异构 schema、存储失败与踢人原因的处理 |
| 同上（KCP） | 明文与加密两种模式、两种编码下经 KCP 的请求/推送/通知与超过单个分片的大请求；经丢包 10%、重复 3%、0–15 ms 随机延迟（乱序）并随机重绑定客户端端口的 UDP 代理，持续至少 2 秒且至少 3 次重绑定的并发购买（每次重绑定后等客户端恢复再进行下一次），校验恰好一次、推送有序、每次重绑定都恢复会话而不重建；加密模式下代理另篡改 2% 的数据报，并校验令牌从未以明文出现在线路上（明文模式下校验它确实出现，证明检查有效）；未知路径与子协议被拒绝（客户端视为永久失败），未知会话收到重置，不足 5 字节的数据报不回复。加密握手：重复 hello 得到相同且不更大的 welcome，固定了其他公钥的客户端拒绝 welcome（去掉标签校验时该测试失败），两端配置不一致时客户端以 `protocol_error` 停止，非 X25519 密钥被拒绝；握手中损坏的 hello 被后续正确的 hello 替换，一两次重置不会让客户端放弃（撤回任一修复时测试失败）；固定密钥的测试向量（握手、派生密钥、密文逐字节一致）与 RFC 8439 2.8.2 的 AEAD 向量。2026-11-11 在本机（Windows）两组并行加压连续 10 次通过 |
| `go test ./protocol/realtime/internal/kcp` | 虚拟时钟下两端最高 40% 丢包、10% 重复、乱序与 3 秒中断；设置 `IKCP_SRC` 与 `CC_BIN` 时与上游 ikcp.c 经 UDP 互通 |
| 同上（需要 node） | TS 编解码与 Go 对 2800 条随机消息逐条一致（64 位极值、NaN/Inf、中文与 emoji、map、oneof、时间戳、未知枚举值），重新编码后 Go `proto.Equal`；Node 客户端在全程随机断线的代理下同样校验（含最后的 volatile 与踢人），两种编码 |
| 同上（设置 `DOTNET_BIN`） | Unity C# 核心（普通 .NET 8，不是 Unity 引擎）：模拟服务器单元测试、与 Go 的 2800 条编解码一致性、全程随机断线下的同样校验，两种编码；2026-11-09 在 Docker（Linux）中连续 5 轮通过 |
| 同上（需要 node） | Unity WebGL jslib 桥：在模拟的 Emscripten 堆中加载，TS 客户端全部帧经过它，全程随机断线下同样校验，两种编码；另以 `UNITY_WEBGL` 编译 C# P/Invoke 分支（设置 `DOTNET_BIN`）。2026-11-11 在本机（Windows，Node 24）与 Docker（Linux，.NET 8）中通过；Unity WebGL 构建与浏览器尚未实测 |
| 同上（设置 `GODOT_BIN`） | Godot 4.5.1 headless：模拟服务器单元测试、与 Go 的 2800 条编解码一致性、全程随机断线下的同样校验，两种编码；2026-11-09 在 Docker（Linux）中连续 10 轮通过 |
| 同上（设置 `CXX_BIN`，如 `clang++`） | Unreal 客户端的 C++ 核心（`TabForgeRealtimeCore.h`，不含引擎），以 ASan/UBSan 构建：模拟服务器单元测试（握手、确认、重复与跳号、恢复与重发、会话过期时的转移与 `session_lost`、超时、过载、心跳与空闲重连、关闭帧与关闭原因、volatile）；与 Go 的 2800 条编解码一致性；WebSocket 层由 Go 测试经 stdin/stdout 桥接，全程随机断线下做同样校验（恰好一次、推送顺序、volatile、踢人）。2026-11-11 在 Docker（Linux，g++ 14）中连续 6 轮通过，clang 19 单元测试通过；`FTabForgeRealtimeClient` 的引擎编译和 IWebSocket 尚未实测 |
| `npm test`（sdk/typescript） | 模拟服务器下的握手、校验、重复帧、恢复与过期、协议错误、永久关闭、超时/取消/背压、volatile 丢弃、微信适配 |
| `go test -race` | 2026-11-09 在 Docker `golang:1.26`（Linux）中通过；含压缩与跨实例交接的版本 2026-11-11 连续 3 轮通过 |

本地记录不代表远程 CI 已通过。浏览器、微信开发者工具、Unity 编辑器与 WebGL 构建、Unreal 引擎编译和真机上的长连接尚未实测；正式环境需使用 `wss://`，并在负载均衡/网关上放开 WebSocket 升级与长连接超时。

## 8. 尚未实现

- Lua 的长连接客户端（可使用 HTTP/SSE 客户端，或按本文第 2–3 节自行实现）；Unreal 客户端只支持二进制子协议，不支持 JSON 帧。
- 进程崩溃后的会话恢复（只有 `Shutdown` 交接的会话可跨实例继续）、TCP 传输；KCP 传输的非 Go 客户端，以及 KCP 服务器密钥的多密钥轮换。
