# 微信小程序 / 小游戏网络适配

`WechatTransport` 使用真实 `wx.request`，普通响应读取 ArrayBuffer，SSE 使用 `enableChunked`、`onHeadersReceived` 和 `onChunkReceived`。不依赖 fetch、TextDecoder 或 AbortController；增量 UTF-8 解码器和取消信号在 SDK 内实现。适用范围是暴露这些 API 的微信小程序/小游戏，其他厂商小程序需要对应网络适配。

```typescript
import { ProtocolClient } from '../typescript/runtime.ts';
import { WechatTransport, CancellationSource } from './transport.ts';
import { operations, protocolVersion, schemaHash, wireSchema } from './generated/types.ts';
import type { ProtocolTypes } from './generated/types.ts';
const client = new ProtocolClient<ProtocolTypes>(operations,
  new WechatTransport('https://api.example.com', protocolVersion, wx, { schemaHash, wireSchema }));
const cancel = new CancellationSource();
const response = await client.call('chatComplete', { prompt: '你好' });
for await (const event of client.stream('chatStream', { prompt: '你好' }, { signal: cancel.signal })) {
  if (event.payload.delta) console.log(event.payload.delta.text);
}
// 页面 onUnload / 游戏退出时 cancel.cancel()，或退出流迭代。
```

项目可用 esbuild 打包，示例产物使用 CommonJS。SDK 无 npm 运行时依赖，但宿主必须支持 ES2020 语法、异步迭代、Promise、Symbol.asyncIterator、Set 和 Uint8Array。基础库目标至少 3.2.2：分块请求需 2.20.2，手动重定向参数需 3.2.2。旧平台未提供分块回调或只返回完整正文时，流调用明确报 `unsupported_transport`，不会把最终正文假装成实时流。

开发者工具可能不提供头回调的 statusCode。只有响应为 SSE 且版本/hash 头均匹配时，适配器按本协议服务器的约定推断 200；JSON/错误响应等待完成回调的真实状态。该兼容逻辑要求服务器只对成功响应使用 SSE。真机提供 statusCode 时使用真实值。完整 success 正文不会再次解析已经收到的分块。

普通响应/头前缓存默认最大 1 MiB；单帧最大 1 MiB JS 字符；队列默认最多 256 个事件、1 MiB UTF-8 data 字节。wx.request 不能暂停生产者，消费过慢会报 `backpressure` 并中止请求。超时覆盖全程，终止事件、取消和退出迭代会解除监听并 abort。

2026-10-03 已在开发者工具 2.01.2510260、基础库 3.11.2 中通过真实 wx.request 的 12 项验收。iPhone 模拟器不代表 iOS/Android 真机通过。正式环境需 HTTPS 和合法域名；示例关闭 URL 检查仅用于本地测试。运行步骤和待测项见 [平台验收](../../doc/platform-validation.md)。

API 依据：[微信官方 request 类型定义](https://github.com/wechat-miniprogram/api-typings/blob/master/types/wx/lib.wx.api.d.ts)。
