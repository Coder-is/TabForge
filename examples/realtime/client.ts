// Browser / Node / Cocos client for the realtime example. Every call is typed
// from the generated contract: names, request and response shapes.
//
//   go run ./examples/realtime/server
//   node examples/realtime/client.ts
import { RealtimeClient } from "../../sdk/typescript/realtime.ts";
import { channels, codecSchema, protocolVersion, schemaHash, wireSchema } from "./Generated/protocol/types.ts";
import type { ChannelTypes } from "./Generated/protocol/types.ts";

const client = new RealtimeClient<ChannelTypes["game"]>({
    url: "ws://127.0.0.1:18090/ws/game",
    channel: channels.game,
    protocolVersion, schemaHash, wireSchema, codecSchema, // codecSchema selects binary Protobuf frames
    token: async () => "user-1001", // your login token; only sent for new sessions
    onState: (state, error) => {
        // "connected": a new session (first login or the old one expired) - reload state.
        // "resumed": the old session continued; nothing was lost.
        // "closed": permanent (kicked, duplicate_login, unauthorized...); show a dialog.
        console.log("state:", state, error?.code ?? "");
    }
});

client.on("chatMessage", message => console.log(`[${message.sentAt}] ${message.from}: ${message.text}`));
client.on("playerMoved", moved => console.log("player", moved.playerId, "at", moved.position));

await client.connect();
const me = await client.api.login({ nickname: "勇者" });
console.log("logged in as", me.playerId, "gold", me.gold);
const room = await client.api.joinRoom({ room: "lobby" });
console.log("players in room:", room.players?.length ?? 0);
await client.api.chat({ text: "hello" });
// Volatile: sent only while connected; returns false when dropped.
client.send.move({ position: { x: 1, y: 2 }, tick: 1 });
try {
    const bought = await client.api.buy({ itemId: 7, count: 3 });
    console.log("gold left", bought.gold, "inventory", bought.inventory);
} catch (error) {
    console.log("buy failed:", error); // e.g. not_enough_gold, or session_lost (outcome unknown)
}
setTimeout(() => client.close(), 500);
