# 统一协议示例

同一套 Proto 类型演示普通 Chat 响应与 SSE 事件，不连接真实大模型。从仓库根目录生成接入包并启动演示：

```bash
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated
go run ./examples/protocol
```

TypeScript 客户端见 [client.ts](client.ts)。演示监听 127.0.0.1:18082；跨域网页需要由应用或网关配置 CORS，演示本身没有 CORS 中间件。

Godot 接入见 [GDScript SDK](../../sdk/godot/README.md)。先获取此协议的 hash，供下列请求使用：

```bash
PROTOCOL_SCHEMA_HASH=$(go run . -protocol=examples/protocol/contract.json | sed -n 's/^Schema hash: //p')
```

普通响应：

```bash
curl http://127.0.0.1:18082/v1/chat/complete \
  -H 'Content-Type: application/json' -H 'X-Protocol-Version: 1.0.0' \
  -H "X-Protocol-Schema: $PROTOCOL_SCHEMA_HASH" \
  --data '{"prompt":"你好","conversationId":"18446744073709551615"}'
```

流式响应：

```bash
curl -N http://127.0.0.1:18082/v1/chat/stream \
  -H 'Content-Type: application/json' -H 'X-Protocol-Version: 1.0.0' \
  -H "X-Protocol-Schema: $PROTOCOL_SCHEMA_HASH" \
  --data '{"prompt":"你好"}'
```

修改 [chat.proto](proto/chat.proto) 后执行 `bash examples/protocol/Make.sh`。路由、鉴权和事件映射编辑 [contract.json](contract.json)。生成的字段和传输文档见 [PROTOCOL.md](generated/PROTOCOL.md)。

实际 Go 后端用 httptransport.New(contract) 创建服务，用 HandleUnary / HandleStream 注册业务函数。输入是 Proto 动态消息，输出可以使用匹配相同 Proto 的生成消息或动态消息。bearer 接口需安装 Authorize 回调；处理函数和模型调用必须遵守 context。

完整架构与平台路线见 [统一协议文档](../../doc/unified-protocol.md)。
