// Launched by protocol/realtime tests against a Go server behind a proxy that
// cuts the TCP connection at random moments.
import assert from "node:assert/strict";
import { RealtimeClient } from "./realtime.ts";
import { channels, codecSchema, protocolVersion, schemaHash, wireSchema } from "../../examples/realtime/Generated/protocol/types.ts";

const [url, encoding, seconds, transport] = process.argv.slice(2);
// "unity-webgl" routes every frame through the Unity WebGL jslib bridge.
const socket = transport === "unity-webgl" ? (await import("../unity/Tests/WebGL/jslib-harness.mjs")).jslibSocketFactory() : undefined;
const states = [];
const client = new RealtimeClient({
    url, encoding, socket, channel: channels.game, protocolVersion, schemaHash, wireSchema, codecSchema,
    token: async () => "user-31", minBackoffMS: 5, maxBackoffMS: 50,
    onState: (state, error) => states.push(error ? `${state}:${error.code}` : state)
});
const pushes = [];
let golds = [];
setTimeout(() => { console.log("TIMEOUT", JSON.stringify({ state: client.state, states: states.slice(-8), golds: golds.length, pushes: pushes.length })); process.exit(2); }, 30000).unref();
client.on("chatMessage", m => pushes.push(Number(m.messageId)));
await client.connect();
const session = client.sessionId;

const login = await client.api.login({ nickname: "勇者😀" });
assert.deepEqual(login, { playerId: "31", nickname: "勇者😀", gold: "100" });

// Purchases under chaos: each must run exactly once.
const deadline = Date.now() + Number(seconds) * 1000;
await Promise.all(Array.from({ length: 8 }, async () => {
    while (Date.now() < deadline) {
        const reply = await client.api.buy({ itemId: 4294967295, count: 2 });
        assert.equal(reply.inventory["4294967295"], 2);
        golds.push(Number(reply.gold));
    }
}));
golds.sort((a, b) => a - b);
golds.forEach((g, i) => assert.equal(g, i + 1, "a purchase ran twice or was lost"));
const until = Date.now() + 10000;
while (pushes.length < golds.length && Date.now() < until)
    await new Promise(r => setTimeout(r, 10));
assert.equal(pushes.length, golds.length);
pushes.forEach((id, i) => assert.equal(id, i + 1, "pushes lost, duplicated or reordered"));
assert.equal(client.sessionId, session, "session was replaced instead of resumed");
const resumes = states.filter(s => s === "resumed").length;

// Business errors and validation before sending.
await assert.rejects(client.api.chat({ text: "" }), e => e.code === "empty_message" && e.message === "say something");
await assert.rejects(client.api.chat({ text: 5 }), e => e.code === "invalid_message");
await assert.rejects(client.api.chat({ text: "slow" }, { timeoutMS: 50 }), e => e.code === "timeout");

// Volatile notify -> volatile push.
// Volatile frames may be dropped while the link is down, so resend like a game loop would.
let m;
client.on("playerMoved", value => { m = value; });
while (!m) {
    client.send.move({ position: { x: 1.5, y: -2 }, tick: 9 });
    await new Promise(r => setTimeout(r, 20));
}
assert.equal(m.tick, 9);
assert.equal(m.position.x, 1.5);

// A kick closes the client permanently.
await client.api.chat({ text: "kick" });
const end = Date.now() + 5000;
while (client.state !== "closed" && Date.now() < end)
    await new Promise(r => setTimeout(r, 10));
assert.equal(states.at(-1), "closed:kicked", JSON.stringify(states.slice(-12)));
await assert.rejects(client.api.chat({ text: "x" }), e => e.code === "kicked");
if (socket) {
    // Every socket delivered its close, and the bridge released all heap memory.
    const drained = Date.now() + 3000;
    while (socket.openSockets() > 0 && Date.now() < drained)
        await new Promise(r => setTimeout(r, 10));
    assert.equal(socket.openSockets(), 0, "bridge sockets left open");
    assert.equal(socket.heap.live.size, 0, "bridge leaked heap allocations");
}
console.log(JSON.stringify({ requests: golds.length, resumes }));
