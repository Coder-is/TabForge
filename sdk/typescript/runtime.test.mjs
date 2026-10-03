import assert from "node:assert/strict";
import { test } from "node:test";
import { SSEParser, StreamValidator, FetchTransport, ProtocolClient, parseUnary } from "./runtime.ts";

const operation = { path: "/chat", transport: "http_sse", auth: "none", timeoutMS: 1000,
  events: { "text.delta": { field: "delta", terminal: false }, completed: { field: "completed", terminal: true } } };
const body = (sequence, payload) => ({ protocolVersion: "1", requestId: "r", sequence: String(sequence), payload });
const record = (sequence, name, payload) => `id: ${sequence}\nevent: ${name}\ndata: ${JSON.stringify(body(sequence, payload))}\n\n`;
const frame = (sequence, name, payload) => ({ id: String(sequence), event: name, data: JSON.stringify(body(sequence, payload)) });

test("SSE parses BOM, all newline forms, comments and multiline data across every character cut", () => {
  const input = "\uFEFF: heartbeat\r\nid: 1\revent: delta\ndata: 你好\r\ndata: 世界\r\n\r\n";
  const expected = [{ id: "1", event: "delta", data: "你好\n世界" }];
  for (let cut = 0; cut <= input.length; cut++) {
    const parser = new SSEParser();
    assert.deepEqual([...parser.feed(input.slice(0, cut)), ...parser.feed(input.slice(cut))], expected);
  }
  const parser = new SSEParser();
  assert.deepEqual(Array.from(input).flatMap(char => parser.feed(char)), expected);
});

test("UTF-8 decoded in one-byte chunks preserves Chinese and emoji", () => {
  const input = record(1, "text.delta", { delta: { text: "你好😀" } });
  const parser = new SSEParser();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const frames = [];
  for (const byte of new TextEncoder().encode(input)) frames.push(...parser.feed(decoder.decode(Uint8Array.of(byte), { stream: true })));
  frames.push(...parser.feed(decoder.decode()));
  assert.equal(JSON.parse(frames[0].data).payload.delta.text, "你好😀");
});

test("SSE requires a blank record boundary and bounds retained memory", () => {
  const parser = new SSEParser();
  assert.deepEqual(parser.feed("data: unfinished\n"), []);
  assert.deepEqual(parser.feed("\n"), [{ id: "", event: "message", data: "unfinished" }]);
  assert.throws(() => new SSEParser(8).feed("data: too long"), error => error.code === "frame_too_large");
});

test("stream validation rejects truncation, gaps, wrong versions/IDs and oneof mismatches", () => {
  const validator = new StreamValidator(operation, "1");
  assert.equal(validator.accept(frame(1, "text.delta", { delta: { text: "a" } })).payload.delta.text, "a");
  assert.throws(() => validator.finish(), error => error.code === "incomplete_stream");
  validator.accept(frame(2, "completed", { completed: {} }));
  validator.finish();
  assert.throws(() => validator.accept(frame(3, "text.delta", { delta: {} })));
  assert.throws(() => new StreamValidator(operation, "1").accept(frame(2, "text.delta", { delta: {} })), error => error.code === "invalid_sequence");
  assert.throws(() => new StreamValidator(operation, "2").accept(frame(1, "text.delta", { delta: {} })), error => error.code === "version_mismatch");
  assert.throws(() => new StreamValidator(operation, "1", "other").accept(frame(1, "text.delta", { delta: {} })));
  assert.throws(() => new StreamValidator(operation, "1").accept(frame(1, "text.delta", { completed: {} })));
  assert.throws(() => new StreamValidator(operation, "1").accept(frame(1, "text.delta", { delta: {}, completed: {} })));
  assert.throws(() => new StreamValidator(operation, "1").accept(frame(1, "constructor", { undefined: {} })), error => error.code === "invalid_event");
});

test("reserved stream error and unary error preserve failure information", () => {
  const error = { code: "timeout", message: "deadline", retryable: true };
  const validator = new StreamValidator(operation, "1");
  assert.throws(() => validator.accept({ id: "1", event: "protocol.error", data: JSON.stringify({ protocolVersion: "1", requestId: "r", sequence: "1", error }) }), e => e.code === "timeout" && e.retryable);
  assert.throws(() => parseUnary({ protocolVersion: "1", requestId: "r", error }, "1"), e => e.code === "timeout");
  assert.throws(() => parseUnary({ protocolVersion: "1", requestId: "r", error, data: {} }, "1"));
  assert.equal(parseUnary({ protocolVersion: "1", requestId: "r", data: { id: "18446744073709551615" } }, "1").id, "18446744073709551615");
});

test("fetch adapter handles fragmented streams and stops at a terminal event", async () => {
  const bytes = new TextEncoder().encode(record(1, "text.delta", { delta: { text: "中文😀" } }) + record(2, "completed", { completed: {} }));
  let offset = 0, cancelled = false, signal;
  const transport = new FetchTransport("http://demo", "1", async (url, options) => {
    signal = options.signal;
    assert.equal(options.method, "POST");
    assert.equal(options.headers["X-Protocol-Version"], "1");
    return new Response(new ReadableStream({
      pull(controller) { if (offset < bytes.length) controller.enqueue(bytes.slice(offset, ++offset)); },
      cancel() { cancelled = true; }
    }), { headers: { "Content-Type": "text/event-stream" } });
  });
  const events = [];
  for await (const event of transport.stream(operation, {})) events.push(event);
  assert.equal(events[0].payload.delta.text, "中文😀");
  assert.equal(events[1].name, "completed");
  assert.equal(cancelled, true);
  assert.equal(signal.aborted, true);
});

test("fetch adapter reports truncated streams and aborts on early iterator return", async () => {
  let cancelled = false;
  const make = () => new FetchTransport("http://demo", "1", async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode(record(1, "text.delta", { delta: {} }))); controller.close(); },
    cancel() { cancelled = true; }
  }), { headers: { "Content-Type": "text/event-stream" } }));
  await assert.rejects(async () => { for await (const event of make().stream(operation, {})) void event; }, e => e.code === "incomplete_stream");
  const transport = new FetchTransport("http://demo", "1", async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode(record(1, "text.delta", { delta: {} }))); },
    cancel() { cancelled = true; }
  }), { headers: { "Content-Type": "text/event-stream" } }));
  for await (const event of transport.stream(operation, {})) { void event; break; }
  assert.equal(cancelled, true);
});

test("fetch adapter reports timeout and explicit cancellation", async () => {
  const fetcher = async (url, options) => new Promise((resolve, reject) => {
    const abort = () => reject(new Error("aborted"));
    options.signal.addEventListener("abort", abort, { once: true });
    if (options.signal.aborted) abort();
  });
  const transport = new FetchTransport("http://demo", "1", fetcher);
  await assert.rejects(transport.call({ ...operation, transport: "http_json", timeoutMS: 5 }, {}), e => e.code === "timeout");
  const controller = new AbortController(); controller.abort();
  await assert.rejects(transport.call({ ...operation, transport: "http_json" }, {}, { signal: controller.signal }), e => e.code === "cancelled");
});

test("client rejects unary/stream API misuse before transport", async () => {
  const client = new ProtocolClient({ chat: operation }, {});
  await assert.rejects(client.call("chat", {}), e => e.code === "invalid_operation");
});

test("unary fetch failures use ProtocolError and reject non-JSON responses", async () => {
  const unary = { ...operation, transport: "http_json" };
  const failed = new FetchTransport("http://demo", "1", async () => { throw new TypeError("offline"); });
  await assert.rejects(failed.call(unary, {}), error => error.code === "transport_error" && error.message === "offline");
  const html = new FetchTransport("http://demo", "1", async () => new Response("<html>proxy failure</html>", { headers: { "Content-Type": "text/html" } }));
  await assert.rejects(html.call(unary, {}), error => error.code === "unsupported_media_type");
});
