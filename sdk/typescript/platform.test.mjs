import assert from "node:assert/strict";
import test from "node:test";
import { CallbackTransport } from "./callback_transport.ts";
import { UTF8Decoder, CancellationSource } from "./utf8.ts";
import { WechatTransport } from "../wechat/transport.ts";
import { CocosTransport, encodeUTF8 } from "../cocos/transport.ts";
import { operations, protocolVersion, schemaHash, wireSchema } from "../../examples/protocol/generated/types.ts";

const settings = { schemaHash, wireSchema };
const reply = data => JSON.stringify({ protocolVersion, schemaHash, requestId: "platform", data });
const event = (sequence, name, payload) => `id: ${sequence}\nevent: ${name}\ndata: ${JSON.stringify({ protocolVersion, schemaHash, requestId: "platform", sequence: String(sequence), payload })}\n\n`;
const chunks = event(1, "text.delta", { delta: { text: "你好😀" } }) + event(2, "completed", { completed: {} });
const options = { requestId: "platform" };
const arrayBuffer = bytes => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);

test("portable decoder preserves Unicode at every byte split and rejects malformed UTF-8", () => {
  const bytes = encodeUTF8("BOM \ufeff你好😀");
  for (let split = 0; split <= bytes.length; split++) {
    const decoder = new UTF8Decoder();
    assert.equal(decoder.decode(bytes.subarray(0, split)) + decoder.decode(bytes.subarray(split), true), "BOM \ufeff你好😀");
  }
  for (const bytes of [[0xc0, 0x80], [0xed, 0xa0, 0x80], [0xf4, 0x90, 0x80, 0x80], [0xe4], [0x80]]) assert.throws(() => new UTF8Decoder().decode(new Uint8Array(bytes), true), { code: "invalid_utf8" });
});

test("callback stream accepts single-byte fragments, validates payloads and disposes at terminal", async () => {
  let aborted = 0, disposed = 0;
  const transport = new CallbackTransport("http://example", protocolVersion, (request, callbacks) => {
    assert.equal(request.headers["X-Protocol-Schema"], schemaHash);
    queueMicrotask(() => {
      callbacks.headers(200, { "Content-Type": "text/event-stream" });
      for (const byte of encodeUTF8(chunks)) callbacks.chunk(new Uint8Array([byte]));
      callbacks.fail(new Error("late failure"));
    });
    return { abort: () => aborted++, dispose: () => disposed++ };
  }, settings);
  const events = [];
  for await (const frame of transport.stream(operations.chatStream, { prompt: "你好" }, options)) events.push(frame);
  assert.equal(events[0].payload.delta.text, "你好😀");
  assert.equal(events.at(-1).name, "completed");
  assert.equal(aborted, 1); assert.equal(disposed, 1);
});

test("callback transports reject overflow, gaps, truncation and queue backpressure", async () => {
  const cases = [
    { body: event(1, "text.delta", { delta: { text: 7 } }), code: "invalid_message" },
    { body: event(2, "text.delta", { delta: { text: "x" } }), code: "invalid_sequence" },
    { body: event(1, "text.delta", { delta: { text: "x" } }), code: "incomplete_stream" },
    { body: chunks, code: "backpressure", extra: { maxQueuedEvents: 1 } },
    { body: chunks, code: "backpressure", extra: { maxQueuedBytes: 1 } },
    { body: chunks, code: "frame_too_large", extra: { maxFrameChars: 20 } }
  ];
  for (const c of cases) {
    const transport = new CallbackTransport("http://example", protocolVersion, (_request, callbacks) => {
      queueMicrotask(() => { callbacks.headers(200, { "content-type": "text/event-stream" }); callbacks.chunk(encodeUTF8(c.body)); callbacks.complete(); });
      return { abort() {} };
    }, { ...settings, ...c.extra });
    await assert.rejects(async () => { for await (const _frame of transport.stream(operations.chatStream, {}, options)) {} }, { code: c.code });
  }
});

test("portable cancellation, whole-request timeout and iterator break abort the network", async () => {
  let aborted = 0;
  const transport = new CallbackTransport("http://example", protocolVersion, (_request, callbacks) => {
    queueMicrotask(() => { callbacks.headers(200, { "content-type": "text/event-stream" }); callbacks.chunk(encodeUTF8(event(1, "text.delta", { delta: { text: "x" } }))); });
    return { abort: () => aborted++ };
  }, settings);
  for await (const _frame of transport.stream(operations.chatStream, {}, options)) break;
  assert.equal(aborted, 1);
  const source = new CancellationSource();
  source.signal.addEventListener("abort", () => { throw new Error("application abort listener"); });
  const pending = (async () => { for await (const _frame of transport.stream(operations.chatStream, {}, { ...options, signal: source.signal })) source.cancel(); })();
  await assert.rejects(pending, { code: "cancelled" });
  await assert.rejects(async () => { for await (const _frame of transport.stream({ ...operations.chatStream, timeoutMS: 5 }, {}, options)) {} }, { code: "timeout" });
  source.cancel();
  await assert.rejects(transport.call(operations.chatComplete, {}, { ...options, signal: source.signal }), { code: "cancelled" });
  assert.equal(aborted, 3);
});

function fakeWx(body, streaming = true, omitStatus = false) {
  let headerListener, chunkListener, aborted = 0;
  const api = { request(request) {
    assert.equal(request.responseType, "arraybuffer");
    assert.equal(request.dataType, "text");
    const bytes = encodeUTF8(body);
    queueMicrotask(() => {
      const header = { "Content-Type": streaming ? "text/event-stream" : "application/json", "X-Protocol-Schema": schemaHash, "X-Protocol-Version": protocolVersion };
      headerListener?.({ header, ...(omitStatus ? {} : { statusCode: 200 }) });
      if (streaming) for (const byte of bytes) chunkListener?.({ data: arrayBuffer(new Uint8Array([byte])) });
      request.success({ statusCode: 200, header, data: arrayBuffer(bytes) });
    });
    return {
      abort: () => aborted++,
      onHeadersReceived: listener => headerListener = listener,
      offHeadersReceived: listener => { if (headerListener === listener) headerListener = undefined; },
      onChunkReceived: listener => chunkListener = listener,
      offChunkReceived: listener => { if (chunkListener === listener) chunkListener = undefined; }
    };
  } };
  return { api, get aborted() { return aborted; } };
}
test("wx.request uses ArrayBuffer, avoids duplicate final body and handles DevTools headers", async () => {
  for (const omitStatus of [false, true]) {
    const wx = fakeWx(chunks, true, omitStatus);
    const transport = new WechatTransport("http://example", protocolVersion, wx.api, settings);
    const events = [];
    for await (const frame of transport.stream(operations.chatStream, {}, options)) events.push(frame);
    assert.equal(events.length, 2); assert.equal(wx.aborted, 1);
  }
  const wx = fakeWx(reply({ text: "你好", usage: { outputTokens: "18446744073709551615" } }), false, true);
  const transport = new WechatTransport("http://example", protocolVersion, wx.api, settings);
  assert.equal((await transport.call(operations.chatComplete, {}, options)).usage.outputTokens, "18446744073709551615");
});

test("callback unary bounds bytes and validates schema before sending", async () => {
  let sent = 0;
  const transport = new CallbackTransport("http://example", protocolVersion, (_request, callbacks) => {
    sent++;
    callbacks.headers(200, { "content-type": "application/json" });
    callbacks.chunk(encodeUTF8(reply({ text: "hello" }))); callbacks.complete();
    return { abort() {} };
  }, { ...settings, maxResponseBytes: 30 });
  await assert.rejects(transport.call(operations.chatComplete, { conversationId: 1 }, options), { code: "invalid_message" });
  assert.equal(sent, 0);
  await assert.rejects(transport.call(operations.chatComplete, {}, options), { code: "response_too_large" });
});

test("Cocos progressive XHR slices cumulative text, retains split surrogates and closes early", async () => {
  let aborted = false;
  const xhr = {
    readyState: 0, status: 200, responseText: "", open() {}, setRequestHeader() {}, getAllResponseHeaders: () => "Content-Type: text/event-stream\r\n",
    abort() { aborted = true; },
    send() { queueMicrotask(() => {
      this.readyState = 2; this.onreadystatechange();
      this.readyState = 3;
      const split = chunks.indexOf("😀") + 1;
      this.responseText = chunks.slice(0, split); this.onprogress?.();
      this.responseText = chunks; this.onprogress?.();
      this.readyState = 4; this.onload?.();
    }); }
  };
  const transport = new CocosTransport("http://example", protocolVersion, { ...settings, backend: "xhr", xhrFactory: () => xhr });
  const events = [];
  for await (const frame of transport.stream(operations.chatStream, {}, options)) events.push(frame);
  assert.equal(events[0].payload.delta.text, "你好😀"); assert.equal(events.length, 2); assert.equal(aborted, true);
});

test("platform adapters reject buffered-only streaming and unsupported wx callbacks", async () => {
  let aborted = 0;
  const transport = new WechatTransport("http://example", protocolVersion, { request() { return { abort() { aborted++; } }; } }, settings);
  await assert.rejects(async () => { for await (const _event of transport.stream(operations.chatStream, {}, options)) {} }, { code: "unsupported_transport" });
  assert.equal(aborted, 1);
  const xhr = {
    readyState: 0, status: 200, responseText: chunks, open() {}, setRequestHeader() {}, getAllResponseHeaders: () => "Content-Type: text/event-stream\r\n", abort() { aborted++; },
    send() { queueMicrotask(() => { this.readyState = 4; this.onload?.(); }); }
  };
  const cocos = new CocosTransport("http://example", protocolVersion, { ...settings, backend: "xhr", xhrFactory: () => xhr });
  await assert.rejects(async () => { for await (const _event of cocos.stream(operations.chatStream, {}, options)) {} }, { code: "unsupported_transport" });
  assert.equal(aborted, 2);
});

test("callback transports reject invalid origins/IDs and cap bytes received before headers", async () => {
  for (const url of ["http://user:password@example", "http://example?query", "http://example/#fragment", "http://example\n"]) assert.throws(() => new CallbackTransport(url, protocolVersion, () => ({ abort() {} }), settings), { code: "bad_request" });
  let sent = 0;
  const transport = new CallbackTransport("http://example", protocolVersion, (_request, callbacks) => {
    sent++; callbacks.chunk(new Uint8Array(32)); return { abort() {} };
  }, { ...settings, maxResponseBytes: 16 });
  await assert.rejects(transport.call(operations.chatComplete, {}, { requestId: "id\n" }), { code: "bad_request" });
  assert.equal(sent, 0);
  await assert.rejects(transport.call(operations.chatComplete, {}, options), { code: "response_too_large" });
});
