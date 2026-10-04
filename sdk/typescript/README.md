# TypeScript 协议运行时

runtime.ts 无 npm 运行时依赖。使用生成的 ProtocolTypes、operations、protocolVersion、schemaHash 和 wireSchema 创建客户端。以下路径以 [client.ts](../../examples/protocol/client.ts) 所在目录为基准，业务项目应调整路径与地址：

```typescript
import { ProtocolClient, FetchTransport } from "../../sdk/typescript/runtime.ts";
import { operations, protocolVersion, schemaHash, wireSchema } from "./generated/types.ts";
import type { ProtocolTypes } from "./generated/types.ts";
const baseURL = "http://127.0.0.1:18082";
const client = new ProtocolClient<ProtocolTypes>(
  operations,
  new FetchTransport(baseURL, protocolVersion, globalThis.fetch, { schemaHash, wireSchema })
);
const response = await client.call("chatComplete", { prompt: "你好" });
for await (const event of client.stream("chatStream", { prompt: "你好" })) {
  if (event.payload.delta) console.log(event.payload.delta.text ?? "");
}
```

AbortSignal 可取消请求，退出流迭代会中止请求。failed 等业务事件由应用处理；传输错误抛出 ProtocolError。默认字段可能被 ProtoJSON 省略。

当前 package.json 标记 private，以源码或业务打包产物分发。浏览器不能直接执行 `.ts`，应使用构建器；示例可在 Node 24+ 运行，见 [协议示例](../../examples/protocol/README.md)。前后端文件分发和普通/流式包络见 [接入指南](../../doc/protocol-integration.md)。

FetchTransport 要求 ES2022、fetch、ReadableStream、TextDecoder 和 AbortController。schemaHash 与 wireSchema 从生成的 types.ts 导入，分别校验协议身份和请求/响应字段。普通响应默认限制为 1 MiB 字节，SSE 默认限制单帧为 1 MiB UTF-16 字符，可通过 FetchOptions 配置。微信与 Cocos 可直接使用 [WechatTransport](../wechat/README.md) / [CocosTransport](../cocos/README.md)；微信与 Cocos XHR 后端不要求宿主提供 TextDecoder，Cocos Fetch 后端仍需标准 fetch 环境。其他平台可实现 CallbackNetwork 并使用 CallbackTransport，或者自行实现 Transport，复用 SSEParser、StreamValidator、SchemaValidator：

1. 将网络 bytes **增量解码**为 UTF-8 字符串，不能逐分片独立解码。
2. 字符串交给 SSEParser.feed，对返回的完整帧调用 StreamValidator.accept。
3. 返回 accept 产生的事件，终止事件后关闭请求；EOF 时调用 finish 检查是否完整结束。
4. 遵守协议版本、鉴权、超时、取消和请求 ID 约定。

启用 wireSchema 后，客户端验证未知字段、数值范围、枚举、map、数组、required、oneof 与 well-known types；失败抛出 invalid_message。自定义 Transport 需要应用相同验证规则。生成的 JSON 类型不直接等于二进制库的 message 类型。部署与旧客户端升级见 [生产说明](../../doc/production.md)。

开发检查使用 Node 24+：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
```
