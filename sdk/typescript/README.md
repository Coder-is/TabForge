# TypeScript 协议运行时

runtime.ts 无 npm 运行时依赖。使用项目生成的 ProtocolTypes、operations 和 protocolVersion 创建客户端，完整示例见 [client.ts](../../examples/protocol/client.ts)：

```typescript
const client = new ProtocolClient<ProtocolTypes>(
  operations,
  new FetchTransport(baseURL, protocolVersion)
);
const response = await client.call("chatComplete", { prompt: "你好" });
for await (const event of client.stream("chatStream", { prompt: "你好" })) {
  if (event.payload.delta) render(event.payload.delta.text ?? "");
}
```

AbortSignal 可取消请求，退出流迭代会中止请求。failed 等业务事件由应用处理；传输错误抛出 ProtocolError。默认字段可能被 ProtoJSON 省略。

FetchTransport 要求 fetch、ReadableStream、TextDecoder 和 AbortController。小程序或引擎使用其他网络 API 时实现 Transport，可复用 SSEParser 与 StreamValidator：

1. 将网络 bytes **增量解码**为 UTF-8 字符串，不能逐分片独立解码。
2. 字符串交给 SSEParser.feed，对返回的完整帧调用 StreamValidator.accept。
3. 返回 accept 产生的事件，终止事件后关闭请求；EOF 时调用 finish 检查是否完整结束。
4. 遵守协议版本、鉴权、超时、取消和请求 ID 约定。

TS 类型不替代响应字段的运行时验证。当前验证包络和流约定，字段类型由 Proto 和应用后端保障；生成的 JSON 类型也不直接等于某个二进制库的 message 类型。

开发检查使用 Node 24+：

```bash
npm ci --prefix sdk/typescript --ignore-scripts
npm --prefix sdk/typescript run check
npm --prefix sdk/typescript test
```
