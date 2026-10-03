// Real local HTTP. wx is simulated here; DevTools validation uses wechat/run.mjs.
import assert from "node:assert/strict";
import http from "node:http";
import { CocosTransport, WechatTransport } from "../../sdk/cocos/transport.ts";
import { protocolVersion, schemaHash, wireSchema } from "../protocol/generated/types.ts";
import { runSmoke } from "./smoke.ts";
const api = { request(options) {
  let headerListener, chunkListener;
  const request = http.request(options.url, { method: options.method, headers: options.header }, response => {
    headerListener?.({ statusCode: response.statusCode, header: response.headers });
    const body = [];
    response.on("data", buffer => {
      const data = buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + buffer.byteLength);
      if (options.enableChunked) chunkListener?.({ data }); else body.push(buffer);
    });
    response.on("end", () => { const b = Buffer.concat(body); options.success({ statusCode: response.statusCode, header: response.headers, data: b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) }); });
    response.on("error", error => options.fail({ errMsg: error.message }));
  });
  request.on("error", error => options.fail({ errMsg: error.message }));
  request.end(options.data);
  return { abort() { request.destroy(); }, onHeadersReceived(fn) { headerListener = fn; }, offHeadersReceived() { headerListener = undefined; }, onChunkReceived(fn) { chunkListener = fn; }, offChunkReceived() { chunkListener = undefined; } };
} };
for (const [name, transport] of [
  ["Cocos fetch (Node)", new CocosTransport(process.argv[2], protocolVersion, { schemaHash, wireSchema })],
  ["WeChat callback API simulation (Node)", new WechatTransport(process.argv[2], protocolVersion, api, { schemaHash, wireSchema })]
]) {
  const results = await runSmoke(transport);
  console.log(name, JSON.stringify(results));
  assert.equal(results.length, 12);
  assert.ok(results.every(result => result.passed), "Acceptance failure: " + JSON.stringify(results.filter(result => !result.passed)));
}
