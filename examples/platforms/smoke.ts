import { ProtocolClient, ProtocolError } from "../../sdk/typescript/runtime.ts";
import type { Transport } from "../../sdk/typescript/runtime.ts";
import { CancellationSource } from "../../sdk/typescript/utf8.ts";
import { operations } from "../protocol/generated/types.ts";
import type { ProtocolTypes } from "../protocol/generated/types.ts";

export interface SmokeResult { name: string; passed: boolean; code?: string; message?: string }
export async function runSmoke(transport: Transport): Promise<SmokeResult[]> {
  const results: SmokeResult[] = [];
  const client = new ProtocolClient<ProtocolTypes>(operations, transport);
  const request = { prompt: "你好😀", conversationId: "18446744073709551615" };
  const options = { requestId: "platform-smoke" };
  const check = (value: boolean, message: string) => { if (!value) throw new Error(message); };
  const run = async (name: string, action: () => Promise<void>) => {
    try { await action(); results.push({ name, passed: true }); }
    catch (error) { results.push({ name, passed: false, code: error instanceof ProtocolError ? error.code : "assertion", message: error instanceof Error ? error.message : String(error) }); }
  };
  const rejects = async (action: () => Promise<unknown>, code: string) => {
    try { await action(); } catch (error) { check(error instanceof ProtocolError && error.code === code, `Expected ${code}, received ${(error as ProtocolError).code}`); return; }
    throw new Error("Expected " + code);
  };
  await run("unary Unicode + uint64", async () => { const data = await client.call("chatComplete", request, options); check(data.text === "你好😀" && data.usage?.outputTokens === "18446744073709551615", "Unary data differs"); });
  await run("SSE Unicode + terminal sequence", async () => { const events = []; for await (const event of client.stream("chatStream", request, options)) events.push(event); check(events.length === 2 && events[0].payload.delta?.text === "你好😀" && events[1].name === "completed" && events[1].sequence === "2", "SSE data differs"); });
  await run("request type validation", async () => { await rejects(() => transport.call(operations.chatComplete, { conversationId: 1 }, options), "invalid_message"); });
  await run("incremental event before EOF + cancellation", async () => {
    const source = new CancellationSource(); let received = false;
    await rejects(async () => { for await (const event of transport.stream({ ...operations.chatStream, timeoutMS: 5000 }, { ...request, prompt: "cancel" }, { ...options, signal: source.signal })) { check(event.name === "text.delta", "Expected first delta"); received = true; source.cancel(); } }, "cancelled");
    check(received, "No incremental event before cancellation");
  });
  await run("iterator break aborts streaming", async () => { for await (const _event of transport.stream({ ...operations.chatStream, timeoutMS: 5000 }, { ...request, prompt: "cancel" }, options)) break; });
  await run("whole-stream timeout", async () => { await rejects(async () => { for await (const _event of transport.stream({ ...operations.chatStream, timeoutMS: 150 }, { ...request, prompt: "timeout" }, options)) {} }, "timeout"); });
  await run("server incomplete-stream error", async () => { await rejects(async () => { for await (const _event of client.stream("chatStream", { ...request, prompt: "truncated" }, options)) {} }, "incomplete_stream"); });
  for (const [path, code] of [["gap", "invalid_sequence"], ["schema", "schema_mismatch"], ["type", "invalid_message"], ["eof", "incomplete_stream"], ["large", "frame_too_large"]]) {
    await run("fault " + path, async () => { await rejects(async () => { for await (const _event of transport.stream({ ...operations.chatStream, path: "/_platform/fault/" + path }, request, options)) {} }, code); });
  }
  return results;
}
