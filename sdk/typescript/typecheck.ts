// These assertions verify the *consumer* contract, including invalid usage.
import { client } from "../../examples/protocol/client.ts";
import type { T_tabforge__example__ChatEvent } from "../../examples/protocol/generated/types.ts";

if (false) {
  client.call("chatComplete", { conversationId: "18446744073709551615" });
  // @ts-expect-error uint64 wire values must not be JS numbers.
  client.call("chatComplete", { conversationId: 18446744073709551615 });
  // @ts-expect-error Unknown operation is rejected by the generated types.
  client.call("missing", {});
  // @ts-expect-error Request members must exist in the Proto message.
  client.call("chatComplete", { unrelated: true });
  // @ts-expect-error oneof members cannot be sent simultaneously.
  const conflicting: T_tabforge__example__ChatEvent = { delta: { text: "a" }, completed: { response: { text: "a" } } };
  void conflicting;
}
