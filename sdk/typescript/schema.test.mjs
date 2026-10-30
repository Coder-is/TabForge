import assert from "node:assert/strict";
import { test } from "node:test";
import { SchemaValidator, validIntegerString, protoInteger, protoMapKey, protoBytes } from "./schema.ts";
import { FetchTransport } from "./runtime.ts";
import { operations, wireSchema, schemaHash, protocolVersion } from "../../examples/protocol/generated/types.ts";

test("ProtoJSON rejects overflow, wrong scalar types and unknown fields like Go protojson", () => {
  const validator = new SchemaValidator(wireSchema);
  validator.validate("tabforge.example.ChatRequest", { conversationId: "18446744073709551615", prompt: "中文😀" });
  // Go accepts 64-bit integers as JSON numbers too; JavaScript callers should still send strings above 2^53.
  validator.validate("tabforge.example.ChatRequest", { conversationId: 1 });
  for (const bad of [{ conversationId: 1.5 }, { conversationId: -1 }, { conversationId: "18446744073709551616" }, { conversationId: "01" }, { conversationId: "1\n" }, { prompt: false }, { unknown: true }]) assert.throws(() => validator.validate("tabforge.example.ChatRequest", bad));
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
  for (const bad of [{}, { id: null }, { id: 1.5 }, { id: 1, map: { "18446744073709551616": 2 } }, { id: 1, map: { "1": 2, "01": 3 } }, { id: 1, list: ["bad!"] }, { id: 1, role: "UNKNOWN" }, { id: 1, role: "1" }]) assert.throws(() => validator.validate("M", bad));
});

test("fetch performs schema checks before sending and before returning a response", async () => {
  let calls = 0;
  const fetcher = async (url, options) => {
    calls++;
    assert.equal(options.headers["X-Protocol-Schema"], schemaHash);
    return new Response(JSON.stringify({ protocolVersion, schemaHash, requestId: "r", data: { text: 123 } }), { headers: { "Content-Type": "application/json" } });
  };
  const transport = new FetchTransport("http://demo", protocolVersion, fetcher, { schemaHash, wireSchema });
  await assert.rejects(transport.call(operations.chatComplete, { conversationId: "01" }), e => e.code === "invalid_message");
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
  // Go base64 decoding ignores CR/LF.
  assert.equal(validator.validate("google.protobuf.BytesValue", "YWJj\n"), "YWJj\n");
  assert.equal(new SchemaValidator({ messages: {}, enums: {} }, { normalize: true }).validate("google.protobuf.BytesValue", "YWJj\n"), "YWJj");
  for (const [type, value] of [["Timestamp", "2024-02-29T12:34:56Z\n"], ["Duration", "1s\n"], ["FieldMask", "userId\n"], ["BytesValue", "YWJ\n"]]) assert.throws(() => validator.validate("google.protobuf." + type, value));
});

test("float and Any accept the exact shapes Go protojson emits", () => {
  const validator = new SchemaValidator({ messages: { "google.protobuf.Empty": { fields: {} }, M: { fields: { id: { kind: "int32" } } } }, enums: {} });
  // strconv.FormatFloat(math.MaxFloat32, 'g', -1, 32) is shortest float32 text, which exceeds MaxFloat32 as a double.
  for (const value of [3.4028235e38, -3.4028235e38, 1.5]) validator.validate("google.protobuf.FloatValue", value);
  for (const value of [3.5e38, -1e39]) assert.throws(() => validator.validate("google.protobuf.FloatValue", value));
  validator.validate("google.protobuf.Any", {});
  validator.validate("google.protobuf.Any", { "@type": "type.googleapis.com/google.protobuf.Empty", value: {} });
  validator.validate("google.protobuf.Any", { "@type": "type.googleapis.com/google.protobuf.Empty" });
  validator.validate("google.protobuf.Any", { "@type": "type.googleapis.com/M", id: 1 });
  assert.throws(() => validator.validate("google.protobuf.Any", { "@type": "type.googleapis.com/google.protobuf.Empty", value: { x: 1 } }));
  assert.throws(() => validator.validate("google.protobuf.Any", { value: {} }));
});

test("integer, map key and bytes rules follow Go protojson exactly", () => {
  const cases = [["1", true, 32, "1"], ["1.0", true, 32, "1"], ["1e2", true, 32, "100"], ["100e-2", true, 32, "1"], ["-0", false, 32, "0"], ["-0.0", false, 64, "0"],
    ["0.1e1", true, 32, "1"], ["1.5", true, 32, undefined], ["1e-2", true, 32, undefined], ["2147483647.0", true, 32, "2147483647"], ["2147483648", true, 32, undefined],
    ["9007199254740993", true, 64, "9007199254740993"], ["1.8446744073709551615e19", false, 64, "18446744073709551615"], ["18446744073709551616", false, 64, undefined],
    ["-1", false, 32, undefined], ["01", true, 32, undefined], [" 1", true, 32, undefined], ["1.0000000000000001", true, 64, undefined],
    ["0.000000000000000000001e21", true, 64, undefined], ["1e99999999999", true, 64, undefined], ["0e99999999999", true, 64, "0"], ["1e+0002", true, 32, "100"]];
  for (const [text, signed, bits, expected] of cases) assert.equal(protoInteger(text, signed, bits), expected, text);
  assert.equal(protoMapKey("01", "int32"), "1");
  assert.equal(protoMapKey("+1", "sint64"), "1");
  assert.equal(protoMapKey("-0", "int32"), "0");
  assert.equal(protoMapKey("+1", "uint32"), undefined);
  assert.equal(protoMapKey("1e0", "int64"), undefined);
  assert.equal(protoBytes("AAE"), "AAE=");
  assert.equal(protoBytes("AAF="), "AAE=");
  assert.equal(protoBytes("-_-_"), "+/+/");
  assert.equal(protoBytes("AA\nE="), undefined);
  assert.equal(protoBytes("AA+_"), undefined);
});

test("validator depth equals JSON depth and normalization is opt-in", () => {
  const schema = { messages: { N: { fields: { next: { kind: "message", type: "N" }, id: { kind: "int64" }, n: { kind: "int32", list: true } } } }, enums: {} };
  let deep = { id: "1" };
  for (let i = 0; i < 63; i++) deep = { next: deep };
  new SchemaValidator(schema).validate("N", deep); // id is at JSON depth 64
  assert.throws(() => new SchemaValidator(schema).validate("N", { next: deep }));
  const value = { id: 5, n: ["1e1", 2.0], next: null };
  assert.equal(new SchemaValidator(schema).validate("N", value), value);
  assert.deepEqual(value, { id: 5, n: ["1e1", 2], next: null });
  assert.deepEqual(new SchemaValidator(schema, { normalize: true }).validate("N", value), { id: "5", n: [10, 2] });
  assert.throws(() => new SchemaValidator(schema, { limits: { maxDepth: 64, maxNodes: 3 } }).validate("N", { n: [1, 2] }));
});
