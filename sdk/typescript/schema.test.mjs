import assert from "node:assert/strict";
import { test } from "node:test";
import { SchemaValidator, validIntegerString } from "./schema.ts";
import { FetchTransport } from "./runtime.ts";
import { operations, wireSchema, schemaHash, protocolVersion } from "../../examples/protocol/generated/types.ts";

test("ProtoJSON rejects precision loss, overflow, wrong scalar types and unknown fields", () => {
  const validator = new SchemaValidator(wireSchema);
  validator.validate("tabforge.example.ChatRequest", { conversationId: "18446744073709551615", prompt: "中文😀" });
  for (const bad of [{ conversationId: 1 }, { conversationId: "18446744073709551616" }, { conversationId: "01" }, { conversationId: "1\n" }, { prompt: false }, { unknown: true }]) assert.throws(() => validator.validate("tabforge.example.ChatRequest", bad));
  assert.equal(validIntegerString("-9223372036854775808", true), true);
  assert.equal(validIntegerString("-9223372036854775809", true), false);
  assert.throws(() => validator.validate("tabforge.example.ChatEvent", { delta: {}, completed: {} }));
  assert.throws(() => validator.validate("tabforge.example.ChatEvent", { delta: { text: 123 } }));
});

test("map keys, enums, bytes, repeated and presence are validated", () => {
  const schema = { messages: { M: { fields: {
    map: { kind: "int32", mapKey: "uint64" }, list: { kind: "bytes", list: true }, role: { kind: "enum", type: "Role" }, id: { kind: "int32", required: true }
  } } }, enums: { Role: ["NONE", "USER"] } };
  const validator = new SchemaValidator(schema);
  validator.validate("M", { id: 1, map: { "18446744073709551615": 2 }, list: ["YWJj"], role: "USER" });
  for (const bad of [{}, { id: 1.5 }, { id: 1, map: { "18446744073709551616": 2 } }, { id: 1, list: ["bad!"] }, { id: 1, role: "UNKNOWN" }]) assert.throws(() => validator.validate("M", bad));
});

test("fetch performs schema checks before sending and before returning a response", async () => {
  let calls = 0;
  const fetcher = async (url, options) => {
    calls++;
    assert.equal(options.headers["X-Protocol-Schema"], schemaHash);
    return new Response(JSON.stringify({ protocolVersion, schemaHash, requestId: "r", data: { text: 123 } }), { headers: { "Content-Type": "application/json" } });
  };
  const transport = new FetchTransport("http://demo", protocolVersion, fetcher, { schemaHash, wireSchema });
  await assert.rejects(transport.call(operations.chatComplete, { conversationId: 123 }), e => e.code === "invalid_message");
  assert.equal(calls, 0);
  await assert.rejects(transport.call(operations.chatComplete, {}), e => e.code === "invalid_message");
  assert.equal(calls, 1);
});

test("unary responses are bounded before JSON parsing", async () => {
  const transport = new FetchTransport("http://demo", "1", async () => new Response("x".repeat(100), { headers: { "Content-Type": "application/json" } }), { maxResponseBytes: 16 });
  await assert.rejects(transport.call(operations.chatComplete, {}), e => e.code === "response_too_large");
});

test("well-known types preserve canonical values and reject invalid dates/durations", () => {
  const validator = new SchemaValidator({ messages: {}, enums: {} });
  validator.validate("google.protobuf.Timestamp", "2024-02-29T12:34:56.123456789Z");
  validator.validate("google.protobuf.Duration", "-0.123s");
  validator.validate("google.protobuf.FieldMask", "profile.userId,name");
  assert.throws(() => validator.validate("google.protobuf.Timestamp", "2025-02-29T12:34:56Z"));
  assert.throws(() => validator.validate("google.protobuf.Duration", "315576000001s"));
  assert.throws(() => validator.validate("google.protobuf.FieldMask", "user_id"));
  for (const [type, value] of [["Timestamp", "2024-02-29T12:34:56Z\n"], ["Duration", "1s\n"], ["FieldMask", "userId\n"], ["BytesValue", "YWJj\n"]]) assert.throws(() => validator.validate("google.protobuf." + type, value));
});
