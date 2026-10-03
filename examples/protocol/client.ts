import { ProtocolClient, FetchTransport } from "../../sdk/typescript/runtime.ts";
import { operations, protocolVersion, schemaHash, wireSchema } from "./generated/types.ts";
import type { ProtocolTypes } from "./generated/types.ts";

export const client = new ProtocolClient<ProtocolTypes>(
  operations,
  new FetchTransport("http://127.0.0.1:18082", protocolVersion, globalThis.fetch, { schemaHash, wireSchema })
);

export async function runExample(): Promise<void> {
  const request = { prompt: "你好", conversationId: "18446744073709551615" };
  const response = await client.call("chatComplete", request);
  console.log(response.text ?? "");
  for await (const event of client.stream("chatStream", request)) {
    if (event.payload.delta) console.log(event.payload.delta.text ?? "");
    if (event.payload.failed) throw new Error(event.payload.failed.message ?? "Chat failed");
  }
}
