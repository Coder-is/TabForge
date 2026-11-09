/**
 * TabForge realtime channel client (WebSocket). One connection carries
 * requests, server pushes and client notifies. The session survives network
 * drops: after reconnecting it resumes, unacknowledged frames are sent again
 * and duplicates are discarded, so requests run once and pushes arrive once,
 * in order. Works with any WebSocket-like API through SocketFactory.
 */
import { SchemaValidator } from "./schema.ts";
import type { WireSchema } from "./schema.ts";
import { PROTOCOL_LIMITS, parseJSON } from "./json.ts";
import { ProtobufCodec, decodeUTF8, encodeUTF8 } from "./protobuf.ts";
import type { CodecSchema } from "./protobuf.ts";
export type { CodecSchema } from "./protobuf.ts";

export const SUBPROTOCOL_PROTO = "tabforge.realtime.v1.proto";
export const SUBPROTOCOL_JSON = "tabforge.realtime.v1.json";

export interface ChannelMessageSpec {
    readonly id: number;
    readonly kind: "request" | "push" | "notify";
    readonly type: string;
    readonly responseType?: string;
    readonly timeoutMS?: number;
    readonly volatile?: boolean;
}
export interface ChannelSpec {
    readonly path: string;
    readonly auth: "none" | "bearer";
    readonly heartbeatMS: number;
    readonly resumeWindowMS: number;
    readonly messages: Readonly<Record<string, ChannelMessageSpec>>;
}
/** Shape of a generated ChannelTypes entry. */
export interface ChannelShape {
    requests: { [name: string]: { request: unknown; response: unknown } };
    pushes: { [name: string]: unknown };
    notifies: { [name: string]: unknown };
}

export class RealtimeError extends Error {
    code: string;
    retryable: boolean;
    constructor(code: string, message: string, retryable = false) {
        super(message);
        this.name = "RealtimeError";
        this.code = code;
        this.retryable = retryable;
    }
}

/** Minimal socket contract. Implement it for wx.connectSocket, engine sockets, etc. */
export interface RealtimeSocket {
    send(data: string | Uint8Array): void;
    close(): void;
}
export interface SocketEvents {
    /** protocol is the negotiated subprotocol ("" if the platform cannot tell). */
    open(protocol: string): void;
    message(data: string | ArrayBuffer | Uint8Array): void;
    /** Called once when the socket closes or fails. */
    close(): void;
}
export type SocketFactory = (url: string, protocols: string[], events: SocketEvents) => RealtimeSocket;

interface WebSocketLike {
    binaryType: string;
    protocol: string;
    onopen: ((event: unknown) => void) | null;
    onmessage: ((event: { data: unknown }) => void) | null;
    onclose: ((event: unknown) => void) | null;
    onerror: ((event: unknown) => void) | null;
    send(data: string | ArrayBufferLike | ArrayBufferView): void;
    close(code?: number, reason?: string): void;
}
type WebSocketConstructor = new (url: string, protocols: string[]) => WebSocketLike;

/** Socket factory for the standard WebSocket API (browsers, Node 22+, Cocos, Laya). */
export function webSocketFactory(WebSocketImpl?: WebSocketConstructor): SocketFactory {
    return (url, protocols, events) => {
        const Impl = WebSocketImpl ?? (globalThis as unknown as { WebSocket?: WebSocketConstructor }).WebSocket;
        if (!Impl)
            throw new RealtimeError("unsupported_transport", "No WebSocket implementation");
        const ws = new Impl(url, protocols);
        ws.binaryType = "arraybuffer";
        let closed = false;
        const done = () => {
            if (!closed) {
                closed = true;
                events.close();
            }
        };
        ws.onopen = () => events.open(ws.protocol ?? "");
        ws.onmessage = event => events.message(event.data as string | ArrayBuffer);
        ws.onclose = done;
        ws.onerror = done;
        return {
            send: data => ws.send(data),
            close: () => {
                try { ws.close(); } catch { /* already closed */ }
                done();
            }
        };
    };
}

export type ConnectionState = "idle" | "connecting" | "connected" | "resumed" | "disconnected" | "closed";

export interface RealtimeOptions {
    /** Full WebSocket URL including the channel path, e.g. wss://host/ws/game. */
    url: string;
    channel: ChannelSpec;
    protocolVersion: string;
    schemaHash: string;
    wireSchema: WireSchema;
    /** Required for the binary "proto" encoding (the default when present). */
    codecSchema?: CodecSchema;
    encoding?: "proto" | "json";
    /** Bearer token for a new session; resumes do not send it again. */
    token?: () => string | Promise<string>;
    socket?: SocketFactory;
    minBackoffMS?: number;
    maxBackoffMS?: number;
    /** Bound for frames kept until the server acknowledges them. */
    maxUnacked?: number;
    maxFrameBytes?: number;
    /**
     * "connected" means a new session: reload game state. "resumed" means
     * nothing was lost. "closed" is permanent (kicked, duplicate login,
     * unauthorized, version mismatch, close()).
     */
    onState?: (state: ConnectionState, error?: RealtimeError) => void;
    /** Errors thrown by push handlers. */
    onError?: (error: unknown) => void;
}

export interface RequestOptions {
    /** Overrides the contract timeout (shorter only makes sense). */
    timeoutMS?: number;
    signal?: { readonly aborted: boolean; addEventListener(type: "abort", listener: () => void, options?: { once?: boolean }): void; removeEventListener(type: "abort", listener: () => void): void };
}

const KIND = { hello: 1, welcome: 2, request: 3, response: 4, push: 5, notify: 6, ping: 7, pong: 8, ack: 9, close: 10 } as const;
type KindName = keyof typeof KIND;
const KIND_NAMES: readonly (KindName | undefined)[] = [undefined, "hello", "welcome", "request", "response", "push", "notify", "ping", "pong", "ack", "close"];

interface WireError { code: string; message: string; retryable: boolean }
interface Frame {
    kind: KindName;
    seq: number;
    ack: number;
    message: number;
    replyTo: number;
    /** Binary Protobuf (proto) or a ProtoJSON object (json). */
    payload?: Uint8Array | Record<string, unknown>;
    error?: WireError;
    hello?: { protocolVersion: string; schemaHash?: string; token?: string; sessionId?: string; resumeToken?: string };
    welcome?: { sessionId: string; resumeToken: string; resumed?: boolean; heartbeatMS: number; resumeWindowMS?: number };
}

// ---- Frame codecs (see doc/realtime.md) ----

function putVarint(out: number[], n: number): void {
    while (n > 0x7f) {
        out.push((n % 128) | 0x80);
        n = Math.floor(n / 128);
    }
    out.push(n);
}
function putField(out: number[], no: number, n: number): void {
    if (n) {
        putVarint(out, no * 8);
        putVarint(out, n);
    }
}
function putBytes(out: number[], no: number, bytes: Uint8Array | number[]): void {
    putVarint(out, no * 8 + 2);
    putVarint(out, bytes.length);
    for (const b of bytes) out.push(b);
}
function putString(out: number[], no: number, s: string | undefined): void {
    if (s)
        putBytes(out, no, encodeUTF8(s));
}

function encodeBinaryFrame(f: Frame): Uint8Array {
    const out: number[] = [];
    putField(out, 1, KIND[f.kind]);
    putField(out, 2, f.seq);
    putField(out, 3, f.ack);
    putField(out, 4, f.message);
    putField(out, 5, f.replyTo);
    if (f.payload && (f.payload as Uint8Array).length)
        putBytes(out, 6, f.payload as Uint8Array);
    if (f.error) {
        const e: number[] = [];
        putString(e, 1, f.error.code);
        putString(e, 2, f.error.message);
        putField(e, 3, f.error.retryable ? 1 : 0);
        putBytes(out, 7, e);
    }
    if (f.hello) {
        const h: number[] = [];
        putString(h, 1, f.hello.protocolVersion);
        putString(h, 2, f.hello.schemaHash);
        putString(h, 3, f.hello.token);
        putString(h, 4, f.hello.sessionId);
        putString(h, 5, f.hello.resumeToken);
        putBytes(out, 8, h);
    }
    return Uint8Array.from(out);
}

const malformed = (why: string): never => { throw new RealtimeError("protocol_error", "Malformed frame: " + why, true); };

/** Walks fields; values are [varint as number] or [start, end] for LEN. */
function walk(buf: Uint8Array, start: number, end: number, visit: (no: number, wire: number, a: number, b: number) => void): void {
    let pos = start;
    const varint = (): number => {
        let n = 0, scale = 1;
        for (let i = 0; i < 10; i++) {
            if (pos >= end)
                malformed("truncated");
            const b = buf[pos++];
            n += (b & 0x7f) * scale;
            scale *= 128;
            if (b < 0x80) {
                if (n > Number.MAX_SAFE_INTEGER)
                    malformed("number out of range");
                return n;
            }
        }
        return malformed("varint");
    };
    while (pos < end) {
        const tag = varint(), no = Math.floor(tag / 8), wire = tag % 8;
        if (no < 1)
            malformed("field number");
        if (wire === 0)
            visit(no, wire, varint(), 0);
        else if (wire === 2) {
            const n = varint();
            if (pos + n > end)
                malformed("truncated");
            visit(no, wire, pos, pos + n);
            pos += n;
        }
        else if (wire === 1 || wire === 5) {
            pos += wire === 1 ? 8 : 4;
            if (pos > end)
                malformed("truncated");
        }
        else
            malformed("wire type");
    }
}

function decodeBinaryFrame(buf: Uint8Array): Frame {
    const f: Frame = { kind: "ping", seq: 0, ack: 0, message: 0, replyTo: 0 };
    let kind = 0;
    const text = (a: number, b: number) => decodeUTF8(buf, a, b);
    walk(buf, 0, buf.length, (no, wire, a, b) => {
        const want = no <= 5 ? 0 : no <= 9 ? 2 : -1;
        if (want >= 0 && wire !== want)
            malformed("wire type of field " + no);
        switch (no) {
            case 1: kind = a; break;
            case 2: f.seq = a; break;
            case 3: f.ack = a; break;
            case 4: f.message = a; break;
            case 5: f.replyTo = a; break;
            case 6: f.payload = buf.slice(a, b); break;
            case 7: {
                const e: WireError = { code: "", message: "", retryable: false };
                walk(buf, a, b, (n, w, x, y) => {
                    if (n === 1 || n === 2) {
                        if (w !== 2) malformed("error");
                        if (n === 1) e.code = text(x, y); else e.message = text(x, y);
                    }
                    else if (n === 3) {
                        if (w !== 0) malformed("error");
                        e.retryable = x !== 0;
                    }
                });
                f.error = e;
                break;
            }
            case 9: {
                const w = { sessionId: "", resumeToken: "", resumed: false, heartbeatMS: 0, resumeWindowMS: 0 };
                walk(buf, a, b, (n, wt, x, y) => {
                    if (n === 1 || n === 2) {
                        if (wt !== 2) malformed("welcome");
                        if (n === 1) w.sessionId = text(x, y); else w.resumeToken = text(x, y);
                    }
                    else if (n >= 3 && n <= 5) {
                        if (wt !== 0) malformed("welcome");
                        if (n === 3) w.resumed = x !== 0; else if (n === 4) w.heartbeatMS = x; else w.resumeWindowMS = x;
                    }
                });
                f.welcome = w;
                break;
            }
            case 8: f.hello = { protocolVersion: "" }; break;
        }
    });
    f.kind = KIND_NAMES[kind] ?? malformed("unknown kind " + kind);
    return f;
}

const DECIMAL = /^[1-9][0-9]{0,15}$/;
function decimalField(value: unknown): number {
    if (value === undefined)
        return 0;
    if (typeof value !== "string" || !DECIMAL.test(value) || Number(value) > Number.MAX_SAFE_INTEGER)
        malformed("sequence number");
    return Number(value);
}
const FRAME_MEMBERS = new Set(["kind", "seq", "ack", "message", "replyTo", "payload", "error", "hello", "welcome"]);

function encodeJSONFrame(f: Frame): string {
    const out: Record<string, unknown> = { kind: f.kind };
    if (f.seq) out.seq = String(f.seq);
    if (f.ack) out.ack = String(f.ack);
    if (f.message) out.message = f.message;
    if (f.replyTo) out.replyTo = String(f.replyTo);
    if (f.payload) out.payload = f.payload;
    if (f.error) out.error = f.error;
    if (f.hello) out.hello = f.hello;
    return JSON.stringify(out);
}

function decodeJSONFrame(text: string): Frame {
    let value: unknown;
    try {
        value = parseJSON(text, PROTOCOL_LIMITS);
    }
    catch {
        return malformed("invalid JSON");
    }
    if (!value || typeof value !== "object" || Array.isArray(value))
        malformed("not an object");
    const o = value as Record<string, unknown>;
    for (const key of Object.keys(o))
        if (!FRAME_MEMBERS.has(key))
            malformed("unknown member " + key);
    const kind = KIND_NAMES.find(name => name === o.kind);
    if (!kind)
        malformed("unknown kind");
    const f: Frame = { kind: kind!, seq: decimalField(o.seq), ack: decimalField(o.ack), message: 0, replyTo: decimalField(o.replyTo) };
    if (o.message !== undefined) {
        if (typeof o.message !== "number" || !Number.isInteger(o.message) || o.message < 0 || o.message > 65535)
            malformed("message id");
        f.message = o.message as number;
    }
    if (o.payload !== undefined) {
        if (!o.payload || typeof o.payload !== "object" || Array.isArray(o.payload))
            malformed("payload");
        f.payload = o.payload as Record<string, unknown>;
    }
    if (o.error !== undefined) {
        const e = o.error as Record<string, unknown>;
        if (!e || typeof e !== "object" || typeof e.code !== "string" || (e.message !== undefined && typeof e.message !== "string") || (e.retryable !== undefined && typeof e.retryable !== "boolean"))
            malformed("error");
        f.error = { code: e.code as string, message: (e.message as string) ?? "", retryable: (e.retryable as boolean) ?? false };
    }
    if (o.welcome !== undefined) {
        const w = o.welcome as Record<string, unknown>;
        if (!w || typeof w !== "object" || typeof w.sessionId !== "string" || typeof w.resumeToken !== "string" || typeof w.heartbeatMS !== "number")
            malformed("welcome");
        f.welcome = { sessionId: w.sessionId as string, resumeToken: w.resumeToken as string, resumed: w.resumed === true, heartbeatMS: w.heartbeatMS as number, resumeWindowMS: (w.resumeWindowMS as number) ?? 0 };
    }
    if (o.hello !== undefined)
        f.hello = { protocolVersion: "" };
    return f;
}

/** Server-to-client envelope rules shared with the Go implementation. */
function validateServerFrame(f: Frame): void {
    const control = !f.seq && !f.message && !f.replyTo && !f.payload && !f.hello;
    switch (f.kind) {
        case "welcome":
            if (!control || !f.welcome?.sessionId || !f.welcome.resumeToken || !f.welcome.heartbeatMS || f.error) malformed("welcome");
            break;
        case "response":
            if (!f.seq || !f.message || !f.replyTo || f.hello || f.welcome || (f.error && f.payload)) malformed("response");
            if (f.error && !f.error.code) malformed("error code");
            break;
        case "push":
            if (!f.message || f.replyTo || f.error || f.hello || f.welcome) malformed("push");
            break;
        case "pong": case "ack":
            if (!control || f.error || f.welcome) malformed(f.kind);
            break;
        case "close":
            if (!control || !f.error?.code || f.welcome) malformed("close");
            break;
        default:
            malformed("unexpected " + f.kind + " from server");
    }
}

interface OutFrame {
    kind: KindName;
    seq: number;
    message: number;
    payload?: Uint8Array | Record<string, unknown>;
    written: boolean;
    call?: PendingCall;
}
interface PendingCall {
    responseType: string;
    resolve(value: unknown): void;
    reject(error: RealtimeError): void;
}
interface Connection {
    socket: RealtimeSocket;
    ready: boolean;
    queue: OutFrame[];
    volatile: number;
    idle?: ReturnType<typeof setTimeout>;
    ping?: ReturnType<typeof setInterval>;
}

type RequestAPI<C extends ChannelShape> = { [K in keyof C["requests"]]: (request: C["requests"][K]["request"], options?: RequestOptions) => Promise<C["requests"][K]["response"]> };
type NotifyAPI<C extends ChannelShape> = { [K in keyof C["notifies"]]: (message: C["notifies"][K]) => boolean };

const MAX_VOLATILE = 256;

function clone(value: unknown): unknown {
    if (value === null || typeof value !== "object")
        return value;
    if (Array.isArray(value))
        return value.map(clone);
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as object))
        Object.defineProperty(out, key, { value: clone((value as Record<string, unknown>)[key]), enumerable: true, writable: true, configurable: true });
    return out;
}

export class RealtimeClient<C extends ChannelShape> {
    /** Typed request functions: client.api.login({ ... }). */
    readonly api: RequestAPI<C>;
    /** Typed notify functions: client.send.move({ ... }); false if a volatile frame was dropped. */
    readonly send: NotifyAPI<C>;

    private options: RealtimeOptions;
    private encoding: "proto" | "json";
    private codec?: ProtobufCodec;
    private validator: SchemaValidator;
    private normalizer: SchemaValidator;
    private byID = new Map<number, [string, ChannelMessageSpec]>();
    private handlers = new Map<string, Set<(message: unknown) => void>>();

    private stateValue: ConnectionState = "idle";
    private sessionID = "";
    private resumeToken = "";
    private heartbeatMS: number;
    private nextSeq = 0;
    private outbox: OutFrame[] = [];
    private peerAck = 0;
    private received = 0;
    private ackSent = 0;
    private ackTimer?: ReturnType<typeof setTimeout>;
    private pending = new Map<number, PendingCall>();
    private conn?: Connection;
    private failures = 0;
    private retryTimer?: ReturnType<typeof setTimeout>;
    private closedError?: RealtimeError;
    private waiters: { resolve(): void; reject(e: RealtimeError): void }[] = [];

    constructor(options: RealtimeOptions) {
        this.options = options;
        this.encoding = options.encoding ?? (options.codecSchema ? "proto" : "json");
        if (this.encoding === "proto") {
            if (!options.codecSchema)
                throw new RealtimeError("invalid_options", "The proto encoding needs codecSchema");
            this.codec = new ProtobufCodec(options.codecSchema);
        }
        this.validator = new SchemaValidator(options.wireSchema);
        this.normalizer = new SchemaValidator(options.wireSchema, { normalize: true });
        this.heartbeatMS = options.channel.heartbeatMS;
        const api: Record<string, unknown> = {}, send: Record<string, unknown> = {};
        for (const [name, spec] of Object.entries(options.channel.messages)) {
            this.byID.set(spec.id, [name, spec]);
            if (spec.kind === "request")
                api[name] = (request: unknown, opts?: RequestOptions) => this.request(name as keyof C["requests"] & string, request as never, opts);
            else if (spec.kind === "notify")
                send[name] = (message: unknown) => this.notify(name as keyof C["notifies"] & string, message as never);
        }
        this.api = api as RequestAPI<C>;
        this.send = send as NotifyAPI<C>;
    }

    get state(): ConnectionState { return this.stateValue; }
    get sessionId(): string { return this.sessionID; }

    /** Starts the client; resolves once the first session is established. */
    connect(): Promise<void> {
        if (this.closedError)
            return Promise.reject(this.closedError);
        if (this.conn?.ready)
            return Promise.resolve();
        const wait = new Promise<void>((resolve, reject) => this.waiters.push({ resolve, reject }));
        if (this.stateValue === "idle")
            this.open();
        return wait;
    }

    /** Ends the session on the server and stops reconnecting. */
    close(): void {
        this.shutdown(new RealtimeError("closed", "Closed by client"), true);
    }

    /** Subscribes to a push; returns an unsubscribe function. Unknown pushes are ignored. */
    on<K extends keyof C["pushes"] & string>(name: K, handler: (message: C["pushes"][K]) => void): () => void {
        const spec = this.options.channel.messages[name];
        if (!spec || spec.kind !== "push")
            throw new RealtimeError("invalid_message", `No push message ${name}`);
        let set = this.handlers.get(name);
        if (!set)
            this.handlers.set(name, set = new Set());
        set.add(handler as (message: unknown) => void);
        return () => { set!.delete(handler as (message: unknown) => void); };
    }

    request<K extends keyof C["requests"] & string>(name: K, request: C["requests"][K]["request"], options: RequestOptions = {}): Promise<C["requests"][K]["response"]> {
        return new Promise((resolve, reject) => {
            const spec = this.options.channel.messages[name];
            if (!spec || spec.kind !== "request")
                throw new RealtimeError("invalid_message", `No request message ${name}`);
            if (options.signal?.aborted)
                throw new RealtimeError("cancelled", "Request cancelled");
            const payload = this.encodePayload(spec.type, request);
            let timer: ReturnType<typeof setTimeout> | undefined;
            let seq = 0;
            const abort = () => finish(new RealtimeError("cancelled", "Request cancelled"));
            const finish = (error?: RealtimeError, value?: unknown) => {
                if (timer === undefined)
                    return;
                clearTimeout(timer);
                timer = undefined;
                options.signal?.removeEventListener("abort", abort);
                if (this.pending.get(seq) === call)
                    this.pending.delete(seq);
                if (error) reject(error); else resolve(value as C["requests"][K]["response"]);
            };
            const call: PendingCall = { responseType: spec.responseType!, resolve: value => finish(undefined, value), reject: error => finish(error) };
            seq = this.enqueue({ kind: "request", seq: 0, message: spec.id, payload, written: false, call });
            const timeout = Math.min(options.timeoutMS ?? Infinity, spec.timeoutMS ?? 30000);
            timer = setTimeout(() => finish(new RealtimeError("timeout", "Request deadline exceeded")), timeout);
            options.signal?.addEventListener("abort", abort, { once: true });
        });
    }

    /** Sends a notify. Reliable notifies queue while offline; volatile ones return false when dropped. */
    notify<K extends keyof C["notifies"] & string>(name: K, message: C["notifies"][K]): boolean {
        const spec = this.options.channel.messages[name];
        if (!spec || spec.kind !== "notify")
            throw new RealtimeError("invalid_message", `No notify message ${name}`);
        if (this.closedError)
            throw this.closedError;
        const payload = this.encodePayload(spec.type, message);
        if (!spec.volatile) {
            this.enqueue({ kind: "notify", seq: 0, message: spec.id, payload, written: false });
            return true;
        }
        const conn = this.conn;
        if (!conn?.ready || conn.volatile >= MAX_VOLATILE)
            return false;
        this.write(conn, { kind: "notify", seq: 0, message: spec.id, payload, written: false });
        return true;
    }

    private encodePayload(type: string, value: unknown): Uint8Array | Record<string, unknown> {
        let canonical: unknown;
        try {
            canonical = this.normalizer.validate(type, clone(value));
        }
        catch (error) {
            throw new RealtimeError("invalid_message", error instanceof Error ? error.message : "Invalid message");
        }
        if (this.encoding === "json")
            return canonical as Record<string, unknown>;
        return this.codec!.encode(type, canonical);
    }

    private decodePayload(type: string, payload: Frame["payload"]): unknown {
        if (this.encoding === "proto")
            return this.codec!.decode(type, (payload as Uint8Array) ?? new Uint8Array());
        const value = payload ?? {};
        this.validator.validate(type, value);
        return value;
    }

    private setState(state: ConnectionState, error?: RealtimeError): void {
        this.stateValue = state;
        this.options.onState?.(state, error);
    }

    private enqueue(f: OutFrame): number {
        if (this.closedError)
            throw this.closedError;
        if (this.outbox.length >= (this.options.maxUnacked ?? 1024))
            throw new RealtimeError("overloaded", "Too many unacknowledged messages", true);
        f.seq = ++this.nextSeq;
        this.outbox.push(f);
        if (f.call)
            this.pending.set(f.seq, f.call);
        if (this.conn?.ready)
            this.write(this.conn, f);
        return f.seq;
    }

    private write(conn: Connection, f: OutFrame): void {
        if (f.seq && f.seq <= this.peerAck)
            return;
        const frame: Frame = { kind: f.kind, seq: f.seq, ack: this.received, message: f.message, replyTo: 0, payload: f.payload };
        this.ackSent = this.received;
        f.written = true;
        this.sendFrame(conn, frame);
    }

    private sendFrame(conn: Connection, frame: Frame): void {
        try {
            conn.socket.send(this.encoding === "proto" ? encodeBinaryFrame(frame) : encodeJSONFrame(frame));
        }
        catch {
            this.drop(conn);
        }
    }

    private async open(): Promise<void> {
        if (this.closedError)
            return;
        this.setState("connecting");
        let token: string | undefined;
        if (!this.sessionID && this.options.token) {
            try {
                token = await this.options.token();
            }
            catch {
                this.scheduleRetry();
                return;
            }
            if (this.closedError)
                return;
        }
        const protocol = this.encoding === "proto" ? SUBPROTOCOL_PROTO : SUBPROTOCOL_JSON;
        const factory = this.options.socket ?? webSocketFactory();
        let conn: Connection | undefined;
        const events: SocketEvents = {
            open: negotiated => {
                if (!conn || this.conn !== conn)
                    return;
                if (negotiated && negotiated !== protocol) {
                    this.shutdown(new RealtimeError("protocol_error", "Server did not accept " + protocol), false);
                    return;
                }
                this.sendFrame(conn, { kind: "hello", seq: 0, ack: this.received, message: 0, replyTo: 0, hello: { protocolVersion: this.options.protocolVersion, schemaHash: this.options.schemaHash, token: token || undefined, sessionId: this.sessionID || undefined, resumeToken: this.resumeToken || undefined } });
            },
            message: data => { if (conn && this.conn === conn) this.receive(conn, data); },
            close: () => { if (conn) this.drop(conn); }
        };
        try {
            conn = { socket: undefined as unknown as RealtimeSocket, ready: false, queue: [], volatile: 0 };
            this.conn = conn;
            conn.socket = factory(this.options.url, [protocol], events);
            this.watch(conn, this.heartbeatMS * 2 + 1000);
        }
        catch {
            if (conn && this.conn === conn)
                this.conn = undefined;
            this.scheduleRetry();
        }
    }

    /** Idle watchdog: any frame from the server proves the link is alive. */
    private watch(conn: Connection, ms: number): void {
        if (conn.idle !== undefined)
            clearTimeout(conn.idle);
        conn.idle = setTimeout(() => this.drop(conn), ms);
    }

    private drop(conn: Connection, error?: RealtimeError): void {
        if (this.conn !== conn)
            return;
        this.conn = undefined;
        if (conn.idle !== undefined) clearTimeout(conn.idle);
        if (conn.ping !== undefined) clearInterval(conn.ping);
        try { conn.socket.close(); } catch { /* closed */ }
        if (this.closedError)
            return;
        if (error && !error.retryable) {
            this.shutdown(error, false);
            return;
        }
        if (conn.ready || this.stateValue !== "disconnected")
            this.setState("disconnected", error);
        this.scheduleRetry();
    }

    private scheduleRetry(immediate = false): void {
        if (this.closedError)
            return;
        const min = this.options.minBackoffMS ?? 200, max = this.options.maxBackoffMS ?? 10000;
        let delay = 0;
        if (!immediate) {
            delay = Math.min(max, min * Math.pow(2, Math.min(this.failures, 16)));
            delay = delay / 2 + Math.random() * delay / 2;
            this.failures++;
        }
        if (this.stateValue !== "disconnected")
            this.setState("disconnected");
        this.retryTimer = setTimeout(() => { this.retryTimer = undefined; this.open(); }, delay);
    }

    private receive(conn: Connection, data: string | ArrayBuffer | Uint8Array): void {
        let f: Frame;
        try {
            if (this.encoding === "proto") {
                if (typeof data === "string")
                    malformed("text frame on binary subprotocol");
                f = decodeBinaryFrame(data instanceof Uint8Array ? data : new Uint8Array(data as ArrayBuffer));
            }
            else {
                if (typeof data !== "string")
                    malformed("binary frame on JSON subprotocol");
                f = decodeJSONFrame(data as string);
            }
            validateServerFrame(f);
        }
        catch (error) {
            this.drop(conn, error instanceof RealtimeError ? error : new RealtimeError("protocol_error", "Invalid frame", true));
            return;
        }
        this.watch(conn, this.heartbeatMS * 2 + 1000);
        if (f.kind === "close") {
            this.serverClosed(conn, f.error!);
            return;
        }
        if (!conn.ready) {
            if (f.kind !== "welcome") {
                this.drop(conn, new RealtimeError("protocol_error", "Expected welcome", true));
                return;
            }
            this.welcome(conn, f);
            return;
        }
        if (f.kind === "welcome") {
            this.drop(conn, new RealtimeError("protocol_error", "Unexpected welcome", true));
            return;
        }
        if (!this.applyAck(conn, f.ack))
            return;
        if (f.kind === "pong" || f.kind === "ack")
            return;
        if (f.kind === "push" && !f.seq) {
            this.deliverPush(f);
            return;
        }
        if (f.seq <= this.received) {
            this.ackSent = 0; // A retransmission: acknowledge again.
            this.scheduleAck(conn);
            return;
        }
        if (f.seq !== this.received + 1) {
            this.drop(conn, new RealtimeError("protocol_error", "Sequence gap from server", true));
            return;
        }
        this.received = f.seq;
        this.scheduleAck(conn);
        if (f.kind === "push") {
            this.deliverPush(f);
            return;
        }
        const call = this.pending.get(f.replyTo);
        if (!call)
            return; // Timed out or cancelled; the late answer is discarded.
        this.pending.delete(f.replyTo);
        if (f.error) {
            call.reject(new RealtimeError(f.error.code, f.error.message, f.error.retryable));
            return;
        }
        try {
            call.resolve(this.decodePayload(call.responseType, f.payload));
        }
        catch (error) {
            call.reject(new RealtimeError("protocol_error", "Invalid response: " + (error instanceof Error ? error.message : String(error))));
        }
    }

    private welcome(conn: Connection, f: Frame): void {
        const w = f.welcome!;
        const resuming = this.sessionID !== "";
        if (w.resumed && w.sessionId !== this.sessionID) {
            this.drop(conn, new RealtimeError("protocol_error", "Server resumed a different session"));
            return;
        }
        if (!w.resumed) {
            if (resuming)
                this.resetSession();
            this.received = 0;
            this.peerAck = 0;
        }
        this.sessionID = w.sessionId;
        this.resumeToken = w.resumeToken;
        this.heartbeatMS = w.heartbeatMS;
        conn.ready = true;
        this.failures = 0;
        if (!this.applyAck(conn, f.ack))
            return;
        this.ackSent = 0;
        for (const frame of this.outbox.slice())
            if (this.conn === conn)
                this.write(conn, frame);
        if (this.conn !== conn)
            return;
        conn.ping = setInterval(() => {
            if (this.conn === conn)
                this.sendFrame(conn, { kind: "ping", seq: 0, ack: this.received, message: 0, replyTo: 0 });
        }, this.heartbeatMS);
        this.watch(conn, this.heartbeatMS * 2 + 1000);
        this.setState(w.resumed ? "resumed" : "connected");
        const waiters = this.waiters;
        this.waiters = [];
        for (const waiter of waiters)
            waiter.resolve();
    }

    private applyAck(conn: Connection, ack: number): boolean {
        if (ack > this.nextSeq) {
            this.drop(conn, new RealtimeError("protocol_error", "Server acknowledged an unsent frame", true));
            return false;
        }
        if (ack > this.peerAck) {
            this.peerAck = ack;
            let n = 0;
            while (n < this.outbox.length && this.outbox[n].seq <= ack)
                n++;
            if (n)
                this.outbox.splice(0, n);
        }
        return true;
    }

    private scheduleAck(conn: Connection): void {
        if (this.ackTimer !== undefined)
            return;
        this.ackTimer = setTimeout(() => {
            this.ackTimer = undefined;
            if (this.conn === conn && conn.ready && this.received > this.ackSent) {
                this.ackSent = this.received;
                this.sendFrame(conn, { kind: "ack", seq: 0, ack: this.received, message: 0, replyTo: 0 });
            }
        }, 50);
    }

    private deliverPush(f: Frame): void {
        const entry = this.byID.get(f.message);
        if (!entry || entry[1].kind !== "push")
            return; // Pushes added by a newer server are ignored.
        const handlers = this.handlers.get(entry[0]);
        if (!handlers?.size)
            return;
        let message: unknown;
        try {
            message = this.decodePayload(entry[1].type, f.payload);
        }
        catch (error) {
            this.options.onError?.(error);
            return;
        }
        for (const handler of [...handlers]) {
            try {
                handler(message);
            }
            catch (error) {
                this.options.onError?.(error);
            }
        }
    }

    private serverClosed(conn: Connection, error: WireError): void {
        const e = new RealtimeError(error.code, error.message, error.retryable);
        if (e.code === "session_expired" && this.sessionID) {
            // The server forgot the session: start a new one right away.
            this.resetSession();
            this.conn = undefined;
            if (conn.idle !== undefined) clearTimeout(conn.idle);
            if (conn.ping !== undefined) clearInterval(conn.ping);
            try { conn.socket.close(); } catch { /* closed */ }
            this.scheduleRetry(true);
            return;
        }
        this.drop(conn, e);
    }

    /**
     * Forget the session. Frames never written move to the next session;
     * requests that may have reached the server fail with session_lost.
     */
    private resetSession(): void {
        const kept: OutFrame[] = [], lost: PendingCall[] = [];
        for (const f of this.outbox) {
            if (!f.written)
                kept.push(f);
            else if (f.call && this.pending.get(f.seq) === f.call)
                lost.push(f.call);
        }
        const keptCalls = new Set(kept.map(f => f.call));
        for (const call of this.pending.values())
            if (!keptCalls.has(call) && !lost.includes(call))
                lost.push(call);
        this.pending.clear();
        kept.forEach((f, i) => {
            f.seq = i + 1;
            if (f.call)
                this.pending.set(f.seq, f.call);
        });
        this.outbox = kept;
        this.nextSeq = kept.length;
        this.sessionID = this.resumeToken = "";
        this.received = this.peerAck = this.ackSent = 0;
        for (const call of lost)
            call.reject(new RealtimeError("session_lost", "Connection lost; the request may or may not have run"));
    }

    private shutdown(error: RealtimeError, notifyServer: boolean): void {
        if (this.closedError)
            return;
        this.closedError = error;
        if (this.retryTimer !== undefined)
            clearTimeout(this.retryTimer);
        if (this.ackTimer !== undefined)
            clearTimeout(this.ackTimer);
        const conn = this.conn;
        this.conn = undefined;
        if (conn) {
            if (notifyServer && conn.ready)
                this.sendFrame(conn, { kind: "close", seq: 0, ack: this.received, message: 0, replyTo: 0, error: { code: error.code, message: error.message, retryable: false } });
            if (conn.idle !== undefined) clearTimeout(conn.idle);
            if (conn.ping !== undefined) clearInterval(conn.ping);
            try { conn.socket.close(); } catch { /* closed */ }
        }
        const calls = [...this.pending.values()];
        this.pending.clear();
        this.outbox = [];
        for (const call of calls)
            call.reject(error);
        for (const waiter of this.waiters.splice(0))
            waiter.reject(error);
        this.setState("closed", error);
    }
}
