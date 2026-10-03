// Launched by the Go transport integration test against a real local HTTP server.
import assert from "node:assert/strict";
import { ProtocolClient, FetchTransport } from "./runtime.ts";
import { operations, protocolVersion, schemaHash, wireSchema } from "../../examples/protocol/generated/types.ts";
const client = new ProtocolClient(operations, new FetchTransport(process.argv[2], protocolVersion, globalThis.fetch, { schemaHash, wireSchema }));
const request = { prompt: "你好😀", conversationId: "18446744073709551615" };
const reply = await client.call("chatComplete", request);
assert.equal(reply.text, "你好😀");
assert.equal(reply.usage.outputTokens, "18446744073709551615");
const events = [];
for await (const event of client.stream("chatStream", request)) events.push(event);
assert.equal(events[0].payload.delta.text, "你好😀");
assert.equal(events.at(-1).name, "completed");
assert.equal(events.at(-1).sequence, "2");
console.log("Go ↔ TypeScript unary + SSE integration passed");
