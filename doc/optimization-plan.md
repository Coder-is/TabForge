# 开发优化计划（2026-11）

日期：2026-11-15，基于分支 `feat/unity-kcp`（`f3dfab1`）的代码审查。本页汇总安全、性能与功能三方面的待办，按顺序逐项实现；每完成一步更新本页“状态”列并单独提交。

审查方式：通读 `protocol/realtime`、`protocol/httptransport`、`v3/`、`project/`、`clientbundle/`、`internal/publish`、编辑器插件、后端数据包与 CI；运行 `govulncheck`、`npm audit`。下面每条都标出了源码位置，结论经对照代码核实。

本机环境为 Windows + Go + Node，没有 .NET、C++ 编译器、Godot 与 javac。需要修改 Unity/Godot/Unreal 客户端线协议的项目（见第五阶段）在本轮只给出设计，不在缺乏验证环境时改动。

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
| 2.5 | 过时的 `// TODO KV表测试` 与注释掉的错误用例 | `v3/tests/data_test.go` | 删除 | 待办 |

## 第三阶段：实时长连接性能

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 3.1 | 没有任何基准测试 | `protocol/realtime` | 增加帧编解码、KCP 加解密、KCP 环回收发基准 | 待办 |
| 3.2 | 每个 UDP 数据报用 `fmt.Sprintf` 拼查找键并持有全局互斥锁；X25519 握手（约 100 µs）在锁内执行；连接数上限全局共享，无单地址限制，握手超时 10 s | `kcptransport.go` `find`、`serveKCPConn` | 结构体键 + 读写锁；握手移出锁；限制单个 IP 的未完成握手数；缩短握手期限 | 待办 |
| 3.3 | 每条消息单独 `Flush`，连续推送变成 N 个数据报、N 次加密与系统调用；发送路径复制 4 次、接收路径复制最多 5 次 | `session.go` `writeLoop`、`kcptransport.go`、`kcpcrypto.go`、`frame.go` | 写循环排空队列后统一刷新；预分配编码缓冲；加密复用缓冲区 | 待办 |
| 3.4 | 每条入站消息加服务器全局读锁；按 ID 查消息为线性扫描并复制结构体 | `session.go` `process`、`protocol/channel.go` | 预建索引；路由在开始服务后冻结 | 待办 |
| 3.5 | 每帧新建 `context.WithTimeout`；每次确认新建 `time.AfterFunc` | `session.go` | 复用计时器 | 待办 |
| 3.6 | 每个 KCP 会话一个 10 ms 定时器协程，10 万会话即每秒 1000 万次唤醒 | `kcptransport.go` | 统一调度器，空闲会话跳过 | 待办 |

## 第四阶段：导表性能

| 步骤 | 问题 | 位置 | 处理 | 状态 |
| --- | --- | --- | --- | --- |
| 4.1 | 合并时每个单元格线性查找输出列，复杂度 O(行 × 列²)；数据行读取两遍 | `v3/compiler/merge.go`、`tab_data.go` | 每张表预建列映射 | 待办 |
| 4.2 | 二进制写出每个值经 `binary.Write` 反射；每行新建缓冲 | `v3/gen/bindata/writer.go` | 直接追加字节，复用行缓冲 | 待办 |
| 4.3 | Protobuf/ProtoJSON 导出各自重复读取描述集与映射文件 | `v3/gen/pbdata` | 在一次导出内缓存 | 待办 |

## 第五阶段：需要跨端验证或较大设计的项目（本轮只做设计）

- **KCP 地址验证**：伪造源地址的握手包可占满会话表并消耗 X25519 计算；明文 KCP 可被用作约 8 倍流量放大。根治需要类似 QUIC Retry 的无状态 cookie 往返，必须同时修改 Go、Unity C#、Godot、Unreal C++ 客户端并在各自环境中联调。第三阶段 3.2 先在服务端缓解（单地址上限、短握手期限、握手移出锁）。生产环境应启用 `KCPKey`。
- **KCP 断开包认证**：明文 reset 可被链路上的攻击者伪造；握手期间伪造 reset 会让客户端永久放弃重连。需改各端客户端。
- **会话交接存储完整性**：`Store` 中的快照未签名、未加密，能写存储者可伪造任意用户会话。建议对快照做 AEAD，并在文档中要求存储为内网私有。
- **嵌套结构与结构数组字段**：目前表格字段只支持基础类型与枚举，是与同类工具相比最大的功能缺口，需要独立设计。
- **Go / Java / proto 源码的字段级标签过滤**；**ProtoJSON 目录导出**。
- **Lua 长连接客户端**；**进程崩溃后的会话恢复**。
- **发布签名**：为发布产物增加构建来源证明或签名。

## 已确认无需处理

长连接加密每方向独立密钥、nonce 不复用、客户端固定服务器公钥、不可降级为明文、会话令牌 256 位且常量时间比较；HTTP 鉴权默认拒绝、请求体与请求头有限额、CORS 为显式白名单；WebSocket 来源检查与解压后限额；Generated 包导入与 `tabforge.json` 路径防穿越和符号链接；编辑器插件不经过 shell 启动且遵守工作区信任；后端数据加载校验 SHA-256；CI 未使用 `pull_request_target`。
