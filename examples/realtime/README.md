# 实时长连接示例

一个最小的游戏服务：登录、进房、移动广播（volatile）、聊天推送和购买（断线也只扣一次）。协议设计见 [实时长连接](../../doc/realtime.md)。

| 文件 | 内容 |
| --- | --- |
| `Protocols/game.proto` | 消息与 RPC |
| `Protocols/contract.json` | 通道 `game`：4 个 request、1 个 notify、3 个 push |
| `tabforge.json` | 项目配置：编译 Proto、生成 Go 类型和协议包 |
| `Generated/` | 生成结果（`protocol/types.ts`、`runtime.json`、`PROTOCOL.md`，`schema/go` 下的 Go 类型） |
| `server/main.go` | Go 服务端，使用类型化处理函数和房间 `Group` |
| `client.ts` | TypeScript 客户端，所有调用由生成的 `ChannelTypes` 约束类型 |

```bash
# 修改 Proto 或清单后重新生成（仓库根目录执行）
go run . -project=examples/realtime

# 启动服务端：ws://127.0.0.1:18090/ws/game
go run ./examples/realtime/server

# 另一个终端运行客户端（Node 22.6+ 可直接运行 .ts）
node examples/realtime/client.ts
```

演示令牌格式为 `user-<数字>`，正式项目在 `server.Authenticate` 中替换为自己的令牌校验。服务端开启了 `SingleSessionPerUser`，同一用户再次登录会以 `duplicate_login` 关闭旧连接。
