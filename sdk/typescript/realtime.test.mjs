import assert from "node:assert/strict";
import { test } from "node:test";
import { RealtimeClient } from "./realtime.ts";
import { ProtobufCodec, encodeUTF8, decodeUTF8, encodeBase64, decodeBase64 } from "./protobuf.ts";
import { channels, codecSchema, protocolVersion, schemaHash, wireSchema } from "../../examples/realtime/Generated/protocol/types.ts";

const tick = () => new Promise(resolve => setTimeout(resolve, 0));
const holds = condition => { try { return condition(); } catch { return false; } };
const until = async (condition, what) => {
    for (let i = 0; i < 500 && !holds(condition); i++) await new Promise(r => setTimeout(r, 2));
    assert.ok(holds(condition), "timed out waiting for " + what);
};

/** A scripted server speaking the JSON subprotocol. */
function fakeNetwork() {
    const net = { sockets: [], opened: 0 };
    net.factory = (url, protocols, events) => {
        const socket = { url, protocols, events, sent: [], closed: false,
            send(data) { this.sent.push(JSON.parse(data)); },
            close() { if (!this.closed) { this.closed = true; } } };
        net.sockets.push(socket);
        net.opened++;
        setTimeout(() => events.open(protocols[0]), 0);
        return socket;
    };
    net.last = () => net.sockets.at(-1);
    net.reply = (frame) => net.last().events.message(JSON.stringify(frame));
    net.welcome = (extra = {}) => net.reply({ kind: "welcome", welcome: { sessionId: "s1", resumeToken: "t1", heartbeatMS: 5000, ...extra } });
    net.cut = () => { const s = net.last(); s.closed = true; s.events.close(); };
    return net;
}

function client(net, extra = {}) {
    return new RealtimeClient({ url: "ws://x/ws/game", encoding: "json", channel: channels.game, protocolVersion, schemaHash, wireSchema,
        token: () => "user-1", socket: net.factory, minBackoffMS: 1, maxBackoffMS: 2, ...extra });
}

test("hello, welcome, typed request and response", async () => {
    const net = fakeNetwork();
    const c = client(net);
    const connected = c.connect();
    await until(() => net.last().sent.length === 1, "hello");
    const hello = net.last().sent[0];
    assert.deepEqual(hello, { kind: "hello", hello: { protocolVersion, schemaHash, token: "user-1" } });
    assert.deepEqual(net.last().protocols, ["tabforge.realtime.v1.json"]);
    net.welcome();
    await connected;
    assert.equal(c.state, "connected");
    const reply = c.api.login({ nickname: "a" });
    const sent = net.last().sent.at(-1);
    assert.deepEqual(sent, { kind: "request", seq: "1", message: 1, payload: { nickname: "a" } });
    net.reply({ kind: "response", seq: "1", ack: "1", message: 1, replyTo: "1", payload: { playerId: "7", gold: "5" } });
    assert.deepEqual(await reply, { playerId: "7", gold: "5" });
    c.close();
    assert.equal(net.last().sent.at(-1).kind, "close");
});

test("requests are validated and normalized before sending", async () => {
    const net = fakeNetwork();
    const c = client(net);
    const connected = c.connect();
    await until(() => net.last().sent.length, "hello");
    net.welcome();
    await connected;
    await assert.rejects(c.api.chat({ text: 1 }), e => e.code === "invalid_message");
    await assert.rejects(c.api.chat({ unknown: "x" }), e => e.code === "invalid_message");
    const request = { itemId: 1.0e0, count: "2" };
    c.api.buy(request).catch(() => {});
    assert.deepEqual(net.last().sent.at(-1).payload, { itemId: 1, count: 2 });
    assert.deepEqual(request, { itemId: 1, count: "2" }, "caller's object is not modified");
    c.close();
});

test("pushes: duplicates dropped, unknown ids ignored, handler errors isolated", async () => {
    const net = fakeNetwork();
    const errors = [];
    const c = client(net, { onError: e => errors.push(e) });
    const got = [];
    c.on("chatMessage", m => { got.push(m.messageId); if (m.messageId === "2") throw new Error("handler bug"); });
    const connected = c.connect();
    await until(() => net.last().sent.length, "hello");
    net.welcome();
    await connected;
    const push = (seq, id) => net.reply({ kind: "push", seq: String(seq), message: 201, payload: { messageId: String(id) } });
    push(1, 1);
    push(1, 1); // retransmission
    net.reply({ kind: "push", seq: "2", message: 999, payload: {} }); // pushed by a newer server
    push(3, 2);
    push(4, 3);
    assert.deepEqual(got, ["1", "2", "3"]);
    assert.equal(errors.length, 1);
    await until(() => net.last().sent.some(f => f.kind === "ack" && f.ack === "4"), "ack");
    c.close();
});

test("resume retransmits unacknowledged frames; expiry fails only uncertain requests", async () => {
    const net = fakeNetwork();
    const states = [];
    const c = client(net, { onState: s => states.push(s) });
    const connected = c.connect();
    await until(() => net.last().sent.length, "hello");
    net.welcome();
    await connected;
    const first = c.api.chat({ text: "one" });
    net.cut();
    // Queued while offline.
    await until(() => net.opened === 2 && net.last().sent.length === 1, "reconnect");
    const hello = net.last().sent[0];
    assert.equal(hello.hello.sessionId, "s1");
    assert.equal(hello.hello.token, undefined, "resumes do not resend the token");
    net.welcome({ resumed: true });
    await tick();
    assert.deepEqual(net.last().sent.at(-1).seq, "1", "unacknowledged request was sent again");
    net.reply({ kind: "response", seq: "1", ack: "1", message: 3, replyTo: "1", payload: { messageId: "9" } });
    assert.equal((await first).messageId, "9");

    const uncertain = c.api.chat({ text: "two" });
    net.cut();
    await until(() => net.opened === 3, "third connection");
    const queued = c.api.chat({ text: "three" }); // never written to any connection
    await until(() => net.last().sent.length === 1, "hello 3");
    net.reply({ kind: "close", error: { code: "session_expired", message: "gone", retryable: true } });
    await assert.rejects(uncertain, e => e.code === "session_lost");
    await until(() => net.opened === 4 && net.last().sent.length === 1, "new session hello");
    assert.equal(net.last().sent[0].hello.token, "user-1");
    net.welcome({ sessionId: "s2", resumeToken: "t2" });
    await tick();
    const carried = net.last().sent.at(-1);
    assert.equal(carried.seq, "1", "queued request renumbered for the new session");
    assert.deepEqual(carried.payload, { text: "three" });
    net.reply({ kind: "response", seq: "1", ack: "1", message: 3, replyTo: "1", payload: {} });
    assert.deepEqual(await queued, {});
    assert.ok(states.includes("resumed") && states.filter(s => s === "connected").length === 2);
    c.close();
});

test("protocol errors reconnect; permanent closes stop the client", async () => {
    const net = fakeNetwork();
    const c = client(net);
    const connected = c.connect();
    await until(() => net.last().sent.length, "hello");
    net.welcome();
    await connected;
    const pending = c.api.chat({ text: "x" });
    net.last().events.message("{\"kind\":\"push\",\"seq\":\"5\",\"message\":201,\"payload\":{}}"); // gap
    await until(() => net.opened === 2, "reconnect after gap");
    await until(() => net.last().sent.length, "hello");
    net.welcome({ resumed: true });
    net.last().events.message("{\"kind\":\"push\",\"kind\":\"push\"}"); // duplicate member
    await until(() => net.opened === 3, "reconnect after garbage");
    await until(() => net.last().sent.length, "hello");
    net.welcome({ resumed: true });
    net.reply({ kind: "close", error: { code: "duplicate_login", message: "elsewhere", retryable: false } });
    await assert.rejects(pending, e => e.code === "duplicate_login");
    assert.equal(c.state, "closed");
    await assert.rejects(c.api.chat({ text: "y" }), e => e.code === "duplicate_login");
    await new Promise(r => setTimeout(r, 20));
    assert.equal(net.opened, 3, "no reconnect after a permanent close");
});

test("timeouts, cancellation, backpressure and volatile drops", async () => {
    const net = fakeNetwork();
    const c = client(net, { maxUnacked: 3 });
    const connected = c.connect();
    await until(() => net.last().sent.length, "hello");
    net.welcome();
    await connected;
    await assert.rejects(c.api.chat({ text: "a" }, { timeoutMS: 5 }), e => e.code === "timeout");
    const controller = new AbortController();
    const cancelled = c.api.chat({ text: "b" }, { signal: controller.signal });
    controller.abort();
    await assert.rejects(cancelled, e => e.code === "cancelled");
    c.api.chat({ text: "c" }).catch(() => {});
    await assert.rejects(c.api.chat({ text: "d" }), e => e.code === "overloaded");
    // A late answer to the timed-out request is ignored.
    net.reply({ kind: "response", seq: "1", ack: "3", message: 3, replyTo: "1", payload: {} });
    assert.equal(c.send.move({ tick: 1 }), true);
    assert.deepEqual(net.last().sent.at(-1), { kind: "notify", ack: "1", message: 100, payload: { tick: 1 } });
    net.cut();
    assert.equal(c.send.move({ tick: 2 }), false, "volatile frames are dropped while offline");
    c.close();
});

test("binary codec helpers", () => {
    for (const text of ["", "abc", "勇者😀", "\u0000￿"])
        assert.equal(decodeUTF8(encodeUTF8(text)), text);
    assert.throws(() => encodeUTF8("\ud800"));
    assert.throws(() => decodeUTF8(Uint8Array.of(0xc0, 0x80)));
    for (const bytes of [[], [0], [1, 2], [255, 254, 253], [1, 2, 3, 4]])
        assert.deepEqual([...decodeBase64(encodeBase64(Uint8Array.from(bytes)))], bytes);
    const codec = new ProtobufCodec(codecSchema);
    const message = { messageId: "18446744073709551615", from: "1", text: "hi", sentAt: "2023-11-14T22:13:20.000000005Z" };
    assert.deepEqual(codec.decode("tabforge.game.ChatMessage", codec.encode("tabforge.game.ChatMessage", message)), message);
    // Offsets are converted to UTC like Go.
    assert.equal(codec.decode("tabforge.game.ChatMessage", codec.encode("tabforge.game.ChatMessage", { sentAt: "2024-01-01T08:00:00+08:00" })).sentAt, "2024-01-01T00:00:00Z");
    const notice = { kind: "NOTICE_EVENT", eventName: "x" };
    assert.deepEqual(codec.decode("tabforge.game.ServerNotice", codec.encode("tabforge.game.ServerNotice", notice)), notice);
    assert.deepEqual(codec.decode("tabforge.game.Move", Uint8Array.of(0x10, 0x01, 0x10, 0x02)), { tick: 2 }, "last scalar wins");
    assert.deepEqual(codec.decode("tabforge.game.Move", Uint8Array.of(0x78, 0x01, 0x10, 0x03)), { tick: 3 }, "unknown fields skipped");
    assert.throws(() => codec.decode("tabforge.game.Move", Uint8Array.of(0x12, 0x05, 0x01)), "truncated");
    assert.throws(() => codec.decode("tabforge.game.Move", Uint8Array.of(0x12, 0x01)), "wrong wire type");
});

test("wechat socket adapter drives the client with wx.connectSocket", async () => {
    const { wechatSocketFactory } = await import("../wechat/realtime.ts");
    const tasks = [];
    const wx = { connectSocket(options) {
        const task = { options, sent: [], listeners: {},
            send({ data }) { this.sent.push(data); }, close() { this.listeners.close?.({}); },
            onOpen(f) { this.listeners.open = f; }, onMessage(f) { this.listeners.message = f; },
            onClose(f) { this.listeners.close = f; }, onError(f) { this.listeners.error = f; } };
        tasks.push(task);
        return task;
    } };
    const c = new RealtimeClient({ url: "wss://x/ws/game", encoding: "proto", channel: channels.game, protocolVersion, schemaHash, wireSchema, codecSchema,
        token: () => "user-1", socket: wechatSocketFactory(wx), minBackoffMS: 1, maxBackoffMS: 2 });
    const connected = c.connect();
    await until(() => tasks.length === 1, "connectSocket");
    assert.deepEqual(tasks[0].options.protocols, ["tabforge.realtime.v1.proto"]);
    tasks[0].listeners.open({ header: { "Sec-WebSocket-Protocol": "tabforge.realtime.v1.proto" } });
    assert.ok(tasks[0].sent[0] instanceof ArrayBuffer, "binary frames are sent as ArrayBuffer");
    // welcome: kind=2, welcome{sessionId "s", resumeToken "t", heartbeatMS 5000}
    const welcome = Uint8Array.of(0x08, 0x02, 0x4a, 0x09, 0x0a, 0x01, 0x73, 0x12, 0x01, 0x74, 0x20, 0x88, 0x27);
    tasks[0].listeners.message({ data: welcome.buffer });
    await connected;
    assert.equal(c.sessionId, "s");
    tasks[0].listeners.error({ errMsg: "network" });
    await until(() => tasks.length === 2, "reconnect");
    c.close();
});
