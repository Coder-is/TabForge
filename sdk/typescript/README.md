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

可直接复制源码，也可在本目录执行 `npm pack` 创建包含 JS 和声明文件的本地 npm 包，再在业务项目执行 `npm install /path/to/tabforge-protocol-runtime-0.4.0.tgz`。包入口为 `@tabforge/protocol-runtime`，子入口为 `/data`、`/node`、`/schema`、`/callback`。当前未发布到 npm registry。浏览器源码示例使用构建器；Node 示例使用 Node 24+，见 [协议示例](../../examples/protocol/README.md)。版本与完整交付包见 [第四部分](../../doc/release-workflow.md)。

纯结构与配置加载使用 `DataSchema<MessageTypes>`，无需网络接口。项目导出会把 `data.ts`、`schema.ts` 和 `json.ts` 与全部类型一起放入 `Generated/schema`，可直接复制使用。64 位数保留字符串，校验 optional、oneof、map 与嵌套消息，并拒绝重复 JSON 键，见 [完整示例](../../examples/complete/README.md)与[项目工作流](../../doc/project-workflow.md)。`decode` 返回类型化 ProtoJSON 对象，不补齐被省略的默认字段；当前不提供二进制 Protobuf 编解码。Node 后端可用 `/node` 的 `DataBundle`、`DataStore` 直接加载完整 Generated 包并保留快照，见 [第三版接入](../../doc/backend-workflow.md)。

FetchTransport 要求 ES2022、fetch、ReadableStream、TextDecoder 和 AbortController。schemaHash 与 wireSchema 从生成的 types.ts 导入，分别校验协议身份和请求/响应字段。普通响应默认限制为 1 MiB 字节，SSE 默认限制单帧为 1 MiB UTF-16 字符，可通过 FetchOptions 配置。微信与 Cocos 可直接使用 [WechatTransport](../wechat/README.md) / [CocosTransport](../cocos/README.md)；微信与 Cocos XHR 后端不要求宿主提供 TextDecoder，Cocos Fetch 后端仍需标准 fetch 环境。其他平台可实现 CallbackNetwork 并使用 CallbackTransport，或者自行实现 Transport，复用 SSEParser、StreamValidator、SchemaValidator：

1. 将网络 bytes **增量解码**为 UTF-8 字符串，不能逐分片独立解码。
2. 字符串交给 SSEParser.feed，对返回的完整帧调用 StreamValidator.accept。
3. 返回 accept 产生的事件，终止事件后关闭请求；EOF 时调用 finish 检查是否完整结束。
4. 遵守协议版本、鉴权、超时、取消和请求 ID 约定。

启用 wireSchema 后，客户端验证未知字段、数值范围、枚举、map、数组、required、oneof 与 well-known types；失败抛出 invalid_message。自定义 Transport 需要应用相同验证规则。生成的 JSON 类型不直接等于二进制库的 message 类型。部署与旧客户端升级见 [生产说明](../../doc/production.md)。

Fetch 和回调传输在建立网络请求前使用相同校验：bearer 接口缺 token 报 `unauthorized`；token 含换行、非空 requestId 不符合 `[A-Za-z0-9_.-]{1,128}`，或接口 timeoutMS 不是正的安全整数时，报 `bad_request`。请求不能序列化为 JSON（如循环引用、BigInt 或顶层 undefined）报 `invalid_message`；普通/流式调用与接口不匹配报 `invalid_operation`。requestId 和 token 可通过调用选项传入，timeoutMS 来自生成的接口元数据。

源码职责：`runtime.ts` 包含客户端、Fetch、SSE 与包络；`callback_transport.ts` 处理回调网络、队列和取消；`schema.ts` 校验字段及 Protobuf 内建类型；`utf8.ts` 提供增量 UTF-8 解码。修改共用规则时同时覆盖 Fetch 和回调后端，定位与联调命令见[开发维护指南](../../doc/development.md)。

开发检查使用 Node 24+：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
```
