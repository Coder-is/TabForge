# Cocos 网络适配

`CocosTransport` 面向 Creator 3.8 系列，提供 Fetch 字节流与 XHR 两种后端；普通和流接口复用生成类型、字段验证和统一错误。微信发布目标使用 `WechatTransport`。浏览器 Fetch/XHR 已实测；Creator 编辑器、JSB、Android/iOS 构建仍需运行平台验收。

```typescript
import { ProtocolClient } from '../typescript/runtime.ts';
import { CocosTransport, CancellationSource } from './transport.ts';
import { operations, protocolVersion, schemaHash, wireSchema } from './generated/types.ts';
import type { ProtocolTypes } from './generated/types.ts';
const transport = new CocosTransport(baseURL, protocolVersion, {
  schemaHash, wireSchema, backend: 'fetch'
});
const client = new ProtocolClient<ProtocolTypes>(operations, transport);
const cancel = new CancellationSource();
for await (const event of client.stream('chatStream', { prompt: '你好' }, { signal: cancel.signal })) {
  if (event.payload.delta) console.log(event.payload.delta.text);
}
// Component.onDestroy 时 cancel.cancel()。
```

Web 推荐 Fetch + ReadableStream + AbortController。JSB 可显式选择 `backend: 'xhr'`，或通过 `xhrFactory` 注入平台 XMLHttpRequest；网络 API 的增量能力需在该平台实测。只有 onprogress 在 HTTP 尚未完成时暴露累计 responseText，XHR 才提供流式能力；完成后才有全文会返回 `unsupported_transport`。XHR 会保留整个正文，默认额外限制为 4 MiB UTF-16 字符，可用 `maxXHRResponseChars` 调整。它保留回调边界处的高位代理字符，避免中文/emoji 分片丢失。XHR 由平台解码 UTF-8，无法提供字节级非法 UTF-8 检测；需要该保证时使用 Fetch 字节流。

队列/事件限制和 portable cancellation 同 [微信 SDK](../wechat/README.md)。SDK 打包目标 ES2020，运行时需异步迭代和标准集合。没有可读字节流的 Fetch 后端不会退回无限缓冲。不同 JSB 版本、平台代理及小游戏 API 不保证行为一致，因此不自动探测后选择另一后端。

验收工程 [examples/platforms/cocos](../../examples/platforms/cocos) 包含组件与打包入口；build:platforms 同时生成 JavaScript 和声明文件，Creator 无须编译仓库外的 SDK 源码。打包入口也导出 ProtocolClient、ProtocolTypes 和示例 operations，可直接用同一生成类型接入。替换业务 Proto 后应重新生成类型并调整打包入口。项目 tsconfig 加入 ES2020/DOM 类型库，Creator 自己生成 temp/tsconfig.cocos.json。构建 SDK 后，用 Creator 打开项目，创建场景、挂载 `TabForgeSmoke`，设置地址与 backend，然后查看 `TABFORGE_PLATFORM_REPORT`。真机地址应使用本机局域网 IP，不能使用 127.0.0.1。完整步骤见 [平台验收](../../doc/platform-validation.md)。

API 依据：[Creator 3.8 HTTP 文档](https://docs.cocos.com/creator/3.8/manual/zh/advanced-topics/http.html)。
