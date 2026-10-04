# 统一协议示例

同一套 Proto 类型演示普通 Chat 响应与 SSE 事件，不连接真实大模型。从仓库根目录生成接入包并启动演示：

```bash
go run . -protocol=examples/protocol/contract.json -protocol_out=examples/protocol/generated
go run ./examples/protocol
```

演示监听 `127.0.0.1:18082`。普通响应为“你好，TabForge”，流依次发送两条文本增量、用量、完成，共四个事件。跨域网页需要由应用或网关配置 CORS，演示本身没有 CORS 中间件。

## 运行客户端

TypeScript 客户端见 [client.ts](client.ts)。另开终端，从仓库根目录使用 Node 24+ 运行：

```bash
node --input-type=module -e "import('./examples/protocol/client.ts').then(m => m.runExample())"
```

浏览器使用构建器打包客户端，不直接加载 `.ts`。完整前后端步骤见 [接入指南](../../doc/protocol-integration.md)。其他端接入：[微信](../../sdk/wechat/README.md)、[Cocos](../../sdk/cocos/README.md)、[Unity](../../sdk/unity/README.md)、[Unreal](../../sdk/unreal/README.md)、[Godot](../../sdk/godot/README.md)。

## 用 curl 查看原始数据

先获取此协议的 hash，供下列请求使用；以下命令使用 Bash：

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

普通返回的消息位于 `data`，SSE 的消息位于 `payload`；64 位整数以字符串传输。流的 `id` / `sequence` 从 1 连续递增，`completed` 是结束事件。

## 修改协议与分发文件

修改 [chat.proto](proto/chat.proto) 后执行 `bash examples/protocol/Make.sh`，需要 `protoc`。路由、鉴权和事件映射编辑 [contract.json](contract.json)。未修改 Proto 时，可直接使用已提交的 [schema.pb](schema.pb) 运行生成器。

| 生成产物 | 用途 |
| --- | --- |
| [contract.json](generated/contract.json) | 分发清单，供 Go 服务加载 |
| [schema.pb](generated/schema.pb) | 包含依赖的 Protobuf 描述文件 |
| [types.ts](generated/types.ts) | TS 类型、接口表、版本/hash 与字段规则 |
| [wire_schema.json](generated/wire_schema.json) | 单独的 JSON 字段验证规则 |
| [protocol.gd](generated/protocol.gd) | Godot CONTRACT 元数据 |
| [runtime.json](generated/runtime.json) | Unity/Unreal 运行时元数据 |
| [PROTOCOL.md](generated/PROTOCOL.md) | 接口、字段与事件说明 |

生成器覆盖同名文件；直接修改生成文件会在下次生成时丢失。分发时保留整个生成包，前后端使用同一协议身份。需要 Protobuf 二进制语言消息类时另用 `protoc` 的语言插件。

## 接入后端与平台验收

实际 Go 后端用 httptransport.New(contract) 创建服务，用 HandleUnary / HandleStream 注册业务函数。输入是 Proto 动态消息，输出可以使用匹配相同 Proto 的生成消息或动态消息。bearer 接口需安装 Authorize 回调；处理函数和模型调用必须遵守 context。

各平台验收使用 [另一套服务与工程](../platforms/README.md)，默认 `18083`，包含故意返回坏数据的接口，正常 SSE 为两个事件。它与这里的业务演示期望值不同，不能替换地址后直接当成同一验收。测试路由只用于本地验收。

完整架构见 [统一协议文档](../../doc/unified-protocol.md)，升级与服务配置见 [生产部署说明](../../doc/production.md)。
