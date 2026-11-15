# 开发优化计划（2026-11）

日期：2026-11-15，基于分支 `feat/unity-kcp`（`f3dfab1`）的代码审查。本页汇总安全、性能与功能三方面的待办，按顺序逐项实现；每完成一步更新本页“状态”列并单独提交。

审查方式：通读 `protocol/realtime`、`protocol/httptransport`、`v3/`、`project/`、`clientbundle/`、`internal/publish`、编辑器插件、后端数据包与 CI；运行 `govulncheck`、`npm audit`。下面每条都标出了源码位置，结论经对照代码核实。

本机环境为 Windows + Go + Node，没有 .NET、C++ 编译器、Godot 与 javac。需要修改 Unity/Godot/Unreal 客户端线协议的项目（见第五阶段）在本轮只给出设计，不在缺乏验证环境时改动。

**进度（2026-11-15）**：第一至第四阶段全部处理完毕，其中 2.3、4.3 经核实不需要修改，另外新增了 4.4（Lua）与 4.5（JSON）两项。每一步单独提交，都附有回归测试或基准。导表的改动用改动前后的产物逐字节比对验证。`go vet ./...`、`go test -count=1 ./...` 与 `go run ./cmd/release -check` 均通过。本机没有 C 编译器，无法运行 `-race`，竞态检查依赖 CI。第五阶段进行中，见下表。

## 第一阶段：安全修复

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 1.1 | Go 1.26.8 标准库有 11 个可达漏洞（net/http、crypto/tls、crypto/x509、net/textproto），影响 WebSocket 与 HTTP 服务；`x/crypto` 有一个不可达公告 | `go.mod`、`.github/workflows/*.yml` | 工具链升到 1.26.11，升级 `x/crypto` | 完成 |
| 1.2 | 表格字段别名允许 `\`，Java 编译器在词法分析前处理 `\u000a`，别名写在 `//` 注释里会提前换行并注入代码；改表的人可在构建 Java 的机器上执行任意代码 | `v3/checker/checker_type.go` `validFieldAlias`；`v3/gen/javasrc/text.go` | 别名拒绝 `\`，加回归测试 | 完成 |
| 1.3 | 每个 ping 都向发送队列追加一个 pong，队列无上限；`MaxFramesPerSecond` 默认关闭，ping 洪泛可使服务器内存无限增长 | `protocol/realtime/session.go` | 同一连接最多保留一个待发 pong | 完成 |
| 1.4 | KCP 收件箱只按条数（64）限制，每条最多 `MaxFrameBytes`（1 MiB），鉴权完成前单个会话可让服务器缓存约 64 MiB | `protocol/realtime/kcptransport.go` `pumpLocked` | 改为按字节限额 | 完成 |
| 1.5 | 发布任务有仓库写权限，却安装未锁版本的 pip 包；checkout 保留令牌 | `.github/workflows/release.yml` | 锁定版本，`persist-credentials: false`（依赖哈希与 Action 固定 SHA 留待有 Python 锁文件工具时补） | 完成 |

## 第二阶段：导表体验

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 2.1 | 每次导出整体替换输出目录，未变化的文件也获得新修改时间，Unity/Godot/Unreal 每次全部重新导入 | `internal/publish/publish.go` | 内容相同的暂存文件沿用旧文件的修改时间；整体相同时跳过替换 | 完成 |
| 2.2 | 项目模式逐个读取表格文件，多种输出格式串行生成 | `project/export.go` | 并发预加载表格文件；输出格式并发生成（完整示例产物逐字节一致；大表提速尚未实测） | 完成 |
| 2.3 | 导出器遇到未知类型直接 panic，用户只看到 `panic: …` | `v3/gen/bindata`、`v3/gen/pbdata`、`v3/compiler/merge.go`、`strtovalue.go` | 核实后不改：字段类型在检查阶段已限定为内建类型或枚举，输出表头来自同一类型定义，这些分支只是内部不变量，用户输入到不了 | 不需要 |
| 2.4 | `.xls` 被交给 xlsx 解析器，报出难懂的 zip 错误；自动发现又排除 `.xls`，两处不一致 | `v3/helper/fileloader.go` | 明确提示另存为 `.xlsx` | 完成 |
| 2.5 | 过时的 `// TODO KV表测试` 与注释掉的错误用例 | `v3/tests/data_test.go` | 删除过时 TODO；把注释掉的用例改成断言 `DataMissMatchTypeDefine` 的真实测试 | 完成 |

## 第三阶段：实时长连接性能

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 3.1 | 没有任何基准测试 | `protocol/realtime` | 增加帧编解码、KCP 加解密与握手、KCP 内存链路、端到端请求基准（结果见文末） | 完成 |
| 3.2 | 每个 UDP 数据报用 `fmt.Sprintf` 拼查找键并持有全局互斥锁；X25519 握手（约 100 µs）在锁内执行；连接数上限全局共享，无单地址限制，握手超时 10 s | `kcptransport.go` `find`、`serveKCPConn` | 结构体键 + `ReadFromUDPAddrPort` + 读写锁；握手移出收包协程并限并发；加密会话设独立“待定”名额（总 4096、每 IP 64、5 秒过期）。Call kcp 分配 88→73 次；单客户端基准不体现锁竞争 | 完成 |
| 3.3 | 每条消息单独 `Flush`，连续推送变成 N 个数据报、N 次加密与系统调用；发送路径复制 4 次、接收路径复制最多 5 次 | `session.go` `writeLoop`、`kcptransport.go`、`kcpcrypto.go`、`frame.go` | 写循环在还有排队帧时暂缓刷新、排空后统一发送；`kcp.SendWithHeader` 去掉分片拼接复制；明文不再预先复制、密文原地解密；单分片消息不再复制；编码缓冲预分配。加密输出缓冲未复用（需另加锁，收益小） | 完成 |
| 3.4 | 每条入站消息加服务器全局读锁；按 ID 查消息为线性扫描并复制结构体 | `session.go` `process`、`protocol/channel.go` | 解析契约时为消息建名称/ID 索引（服务端、客户端、房间推送共用）；路由改为写时复制表，读取不加锁 | 完成 |
| 3.5 | 每帧新建 `context.WithTimeout`；每次确认新建 `time.AfterFunc` | `session.go` | 读写循环各用一个长生命周期 context 加可重置计时器；确认计时器每会话复用。每次请求分配 ws 92→78、kcp 62→53 | 完成 |
| 3.6 | 每个 KCP 会话一个 10 ms 定时器协程，10 万会话即每秒 1000 万次唤醒 | `kcptransport.go` | 服务端每个 `ServeKCP` 一个调度器（每 CPU 一个协程），会话只在 KCP 有待发、待确认、待重传或窗口探测时被访问；收发与读取时唤醒并校准 KCP 时钟。客户端仍用自己的计时协程（负责保活） | 完成 |

## 第四阶段：导表性能

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 4.1 | 合并时每个单元格线性查找输出列，复杂度 O(行 × 列²)；数据行读取两遍 | `v3/compiler/merge.go`、`tab_data.go` | 合并时每张输入表预先解析输出列与重复列个数；产物逐字节一致，编译 179 ms → 118 ms。数据行读两遍未改：剩余开销主要是加载单元格的内存分配 | 完成 |
| 4.2 | 二进制写出每个值经 `binary.Write` 反射；每行新建缓冲 | `v3/gen/bindata/writer.go` | 用 `AppendUint*` 追加到缓冲区；产物逐字节一致，90 ms → 45 ms，分配 196 万 → 10 万次。每行缓冲未复用（剩余分配已很少） | 完成 |
| 4.3 | Protobuf/ProtoJSON 导出各自重复读取描述集与映射文件 | `v3/gen/pbdata` | 核实后不改：描述集约 10 KB、映射文件约 1 KB，重复解析不到 1 ms | 不需要 |
| 4.4 | Lua 导出对每个单元格经模板做两次反射调用，2 万行 × 40 列需 1.2 s（基准中最慢） | `v3/gen/luasrc` | 数据块改为每表一次 `LuaRows`，字段与过滤规则只算一次；产物逐字节一致。1.2 s → 64 ms，分配 604 万 → 64 万次 | 完成 |
| 4.5 | JSON 导出为每行建 map，再由 `MarshalIndent` 逐行排序键并整体重新缩进 | `v3/gen/jsondata` | 按 `MarshalIndent` 的格式直接流式写出（键排序、无数据行时为 null、字符串与浮点仍经 `json.Marshal`，NaN 照样报错）；与旧实现逐字节对比的边界用例测试。283 ms → 77 ms | 完成 |

## 第五阶段：后续项目

本机可用 Docker（`golang:1.26`、`mcr.microsoft.com/dotnet/sdk:8.0-alpine` 等镜像），Linux 竞态测试与部分跨端测试可在容器中运行；跨端改动仍以 CI 结果为准。

| 步骤 | 问题 | 处理 | 状态 |
| --- | --- | --- | --- |
| 5.1 | JSON 编码的实时连接每帧解码约 8.5 µs、146 次分配；`ValidateJSON` 为每个值取一次 token，HTTP 请求与数据包加载同样经过它 | `ValidateJSON` 先用 `json.Valid` 检查语法，再用轻量扫描检查深度与重复成员（转义和非法 UTF-8 的成员名按 `Token` 的方式解码后比较），出错时仍由原实现给出原来的错误信息；与原实现做差分模糊测试（580 万次输入无差异）。帧解码去掉多余的尾部检查和正则。8491 ns → 2967 ns，146 → 22 次分配 | 完成 |
| 5.2 | 会话交接存储中的快照未签名、未加密，能写存储者可伪造任意用户会话 | 新增 `Server.StoreKey` 与 `StorePreviousKeys`：快照经 XChaCha20-Poly1305 加密认证，存储键作为关联数据（不能挪给其他会话）；设置后拒绝明文快照。不设置时行为不变，文档写明存储须与服务器同等可信 | 完成 |
| 5.3 | Go / Java / proto 源码不支持字段级标签过滤，生成的结构可能包含数据里不存在的字段 | 新增 `nogenfield_go`、`nogenfield_java`、`nogenfield_proto`；Go/Java 同时去掉以该字段为键的索引，proto 保持其余字段编号。未使用时完整示例产物逐字节不变；过滤后的 Go 经 `go vet`、Java 经 `javac`、proto 经 protocompile 编译通过 | 完成 |
| 5.4 | ProtoJSON 没有分表目录导出 | 新增项目输出 `protojson_dir` 与 CLI `-pbjson_dir`（都要求映射）：每张映射表一个 ProtoJSON 文件，写入数据清单；与 `protobuf_dir` 同表文件解码结果相同。VS Code 配置补全同步 | 完成 |
| 5.5 | 发布产物只有 SHA-256，没有来源证明 | 发布工作流用 `actions/attest@v4` 为每个交付文件生成 Sigstore 签名的 SLSA 来源证明，文档给出 `gh attestation verify` 核对方法。只在推送 tag 时运行，本地仅校验了 YAML | 完成（待首次发布验证） |
| 5.6 | KCP 握手前没有地址验证：伪造源地址可占满待定名额；明文 KCP 可被用作流量放大 | 类似 QUIC Retry 的无状态 cookie 往返；需同时修改 Go、Unity C#、Godot、Unreal C++ 客户端 | 待办 |
| 5.7 | 明文 reset 可被链路上的攻击者伪造；握手期间伪造 reset 会让客户端永久放弃重连 | reset 带会话密钥派生的令牌；握手期间的 reset 改为可重试。需改各端客户端 | 待办 |
| 5.8 | 表格字段只支持基础类型与枚举 | 嵌套结构与结构数组，需要独立设计 | 待定 |
| 5.9 | Lua 长连接客户端；进程崩溃后的会话恢复 | 需要独立设计 | 待定 |

## 已确认无需处理

长连接加密每方向独立密钥、nonce 不复用、客户端固定服务器公钥、不可降级为明文、会话令牌 256 位且常量时间比较；HTTP 鉴权默认拒绝、请求体与请求头有限额、CORS 为显式白名单；WebSocket 来源检查与解压后限额；Generated 包导入与 `tabforge.json` 路径防穿越和符号链接；编辑器插件不经过 shell 启动且遵守工作区信任；后端数据加载校验 SHA-256；CI 未使用 `pull_request_target`。

## 实时长连接基准

命令：`go test -run '^$' -bench . -benchmem -benchtime=2s ./protocol/realtime/`。环境：Windows，16 线程，Go 1.26.11。`Call` 为同一客户端多协程并发请求、回环地址。

| 基准 | 优化前 | 优化后 |
| --- | --- | --- |
| FrameEncode proto | 88 ns，104 B，3 次分配 | 60 ns，128 B，1 次分配 |
| FrameEncode json | 648 ns，272 B，4 次分配 | 651 ns，272 B，4 次分配（未改） |
| FrameDecode proto | 101 ns，160 B，2 次分配 | 102 ns，160 B，2 次分配（未改） |
| FrameDecode json | 8279 ns，4515 B，146 次分配 | 8491 ns，4515 B，146 次分配（未改，见下） |
| KCPSealOpen 512 B | 970 ns，1120 B，4 次分配 | 804 ns，608 B，3 次分配 |
| KCPAccept（一次加密握手） | 91 µs，2304 B，39 次分配 | 88 µs（已移出收包协程） |
| KCPLink 256 B 消息 | 17.9 µs，2268 B，13 次分配 | 16.3 µs，1413 B，8 次分配 |
| Call ws | 21.8 µs，6853 B，97 次分配 | 19.9 µs，5589 B，78 次分配 |
| Call kcp | 30.5 µs，4233 B，88 次分配 | 24.8 µs，3222 B，53 次分配 |

`Call` 的耗时在本机波动约 ±20%，以分配次数为准。JSON 帧解码见 5.1：8491 ns → 2967 ns。空闲 KCP 会话的开销无法用单连接基准体现：改造前每个会话一个协程、每 10 ms 唤醒一次；改造后服务端每个 CPU 一个调度协程，只访问 KCP 有待办的会话。

## 导表基准

命令：`go test -run '^$' -bench Large -benchmem ./v3/tests/`。2 万行 × 40 列（6 种标量类型与 4 列重复数组）的单张表，环境同上。

| 基准 | 优化前 | 优化后 |
| --- | --- | --- |
| 编译（加载、检查、合并） | 179 ms，148 MB，274 万次分配 | 118 ms，148 MB，274 万次分配 |
| JSON 导出 | 283 ms，217 MB，238 万次分配 | 77 ms，94 MB，94 万次分配 |
| Lua 导出 | 1214 ms，316 MB，604 万次分配 | 64 ms，97 MB，64 万次分配 |
| 二进制导出 | 90 ms，47 MB，196 万次分配 | 45 ms，37 MB，10 万次分配 |
