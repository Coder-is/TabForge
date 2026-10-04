import { ProtocolError, SSEParser, StreamValidator, parseJSON, parseUnary, prepareRequest, validateMessage } from "./runtime.ts";
import type { CallOptions, FetchOptions, Operation, StreamEvent, Transport, WireSchema } from "./runtime.ts";
import { UTF8Decoder } from "./utf8.ts";
export interface NetworkRequest {
    url: string;
    headers: Record<string, string>;
    body: string;
    timeoutMS: number;
    streaming: boolean;
}
export interface NetworkCallbacks {
    headers(status: number, headers: Record<string, unknown>): void;
    chunk(bytes: Uint8Array): void;
    complete(): void;
    fail(error: unknown): void;
}
export interface NetworkTask {
    abort(): void;
    dispose?(): void;
}
export type CallbackNetwork = (request: NetworkRequest, callbacks: NetworkCallbacks) => NetworkTask;
export interface CallbackOptions extends FetchOptions {
    schemaHash: string;
    wireSchema: WireSchema;
    maxQueuedBytes?: number;
    maxQueuedEvents?: number;
}
const limit = (value: number | undefined, fallback: number): number => {
    const result = value ?? fallback;
    if (!Number.isSafeInteger(result) || result < 1)
        throw new ProtocolError("bad_request", "Limits must be positive integers");
    return result;
};
function byteLength(text: string): number {
    let size = 0;
    for (let i = 0; i < text.length; i++) {
        const code = text.charCodeAt(i);
        if (code <= 0x7f)
            size++;
        else if (code <= 0x7ff)
            size += 2;
        else if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length && text.charCodeAt(i + 1) >= 0xdc00 && text.charCodeAt(i + 1) <= 0xdfff) {
            size += 4;
            i++;
        }
        else
            size += 3;
    }
    return size;
}
function error(value: unknown): ProtocolError {
    return value instanceof ProtocolError ? value : new ProtocolError("transport_error", value instanceof Error ? value.message : "Network request failed");
}
/** Push network APIs cannot pause their producer. A slow consumer gets a bounded
 * queue and an explicit backpressure error, rather than unbounded memory use. */
class Session {
    private operation: Operation;
    private version: string;
    private settings: CallbackOptions;
    private options: CallOptions;
    private task?: NetworkTask;
    private stopped = false;
    private disposed = false;
    private timer?: ReturnType<typeof setTimeout>;
    private wake?: () => void;
    private failure?: ProtocolError;
    private queue: {
        event: StreamEvent<unknown>;
        bytes: number;
    }[] = [];
    private queuedBytes = 0;
    private decoder = new UTF8Decoder();
    private parser: SSEParser;
    private validator: StreamValidator;
    private status = 0;
    private pending: Uint8Array[] = [];
    private pendingBytes = 0;
    private responseBytes = 0;
    private text = "";
    private value: unknown;
    private maximum: number;
    private queueMaximum: number;
    private eventMaximum: number;
    private abort = () => this.reject(new ProtocolError("cancelled", "Request cancelled"));
    constructor(operation: Operation, version: string, settings: CallbackOptions, options: CallOptions) {
        this.operation = operation;
        this.version = version;
        this.settings = settings;
        this.options = options;
        this.maximum = limit(settings.maxResponseBytes, 1 << 20);
        this.queueMaximum = limit(settings.maxQueuedBytes, 1 << 20);
        this.eventMaximum = limit(settings.maxQueuedEvents, 256);
        this.parser = new SSEParser(limit(settings.maxFrameChars, 1 << 20));
        this.validator = new StreamValidator(operation, version, options.requestId, settings);
    }
    start(network: CallbackNetwork, baseURL: string, request: unknown): void {
        const { headers, body } = prepareRequest(this.operation, request, this.version, this.settings, this.options);
        this.options.signal?.addEventListener("abort", this.abort, { once: true });
        if (this.options.signal?.aborted) {
            this.abort();
            return;
        }
        this.timer = setTimeout(() => this.reject(new ProtocolError("timeout", "Request deadline exceeded", true)), this.operation.timeoutMS);
        const guard = (fn: () => void) => {
            if (this.stopped)
                return;
            try {
                fn();
            }
            catch (failure) {
                this.reject(error(failure));
            }
        };
        try {
            this.task = network({ url: baseURL.replace(/\/$/, "") + this.operation.path, headers, body, timeoutMS: this.operation.timeoutMS, streaming: this.operation.transport === "http_sse" }, {
                headers: (status, responseHeaders) => guard(() => this.headers(status, responseHeaders)),
                chunk: bytes => guard(() => this.chunk(bytes)),
                complete: () => guard(() => this.complete()),
                fail: failure => guard(() => this.reject(error(failure)))
            });
            if (this.stopped)
                this.dispose(); // Also handles synchronous network callbacks.
        }
        catch (failure) {
            this.reject(error(failure));
        }
    }
    private headers(status: number, headers: Record<string, unknown>): void {
        if (this.status)
            throw new ProtocolError("invalid_frame", "Duplicate response headers");
        if (!Number.isInteger(status) || status < 100 || status > 599)
            throw new ProtocolError("invalid_frame", "Invalid HTTP status");
        this.status = status;
        const name = Object.keys(headers).find(key => key.toLowerCase() === "content-type");
        const type = name && typeof headers[name] === "string" ? (headers[name] as string).split(";")[0].trim().toLowerCase() : "";
        const expected = this.streaming ? "text/event-stream" : "application/json";
        if (type !== expected)
            throw new ProtocolError("unsupported_media_type", "Unexpected response content type");
        const pending = this.pending;
        this.pending = [];
        this.pendingBytes = 0;
        for (const bytes of pending) {
            if (this.stopped)
                break;
            this.chunk(bytes);
        }
    }
    private get streaming(): boolean { return this.operation.transport === "http_sse" && this.status >= 200 && this.status < 300; }
    private chunk(bytes: Uint8Array): void {
        if (!(bytes instanceof Uint8Array))
            throw new ProtocolError("invalid_frame", "Network chunk must be bytes");
        if (!this.status) {
            this.pendingBytes += bytes.byteLength;
            if (this.pendingBytes > this.maximum)
                throw new ProtocolError("response_too_large", "Body arrived before response headers");
            this.pending.push(bytes.slice());
            return;
        }
        if (!this.streaming) {
            this.responseBytes += bytes.byteLength;
            if (this.responseBytes > this.maximum)
                throw new ProtocolError("response_too_large", "Response exceeds configured limit");
            this.text += this.decoder.decode(bytes);
            return;
        }
        // Bound the intermediate decoded chunk even if a platform coalesces a large
        // number of individually valid SSE frames into one callback.
        for (let offset = 0; offset < bytes.length && !this.stopped; offset += 4096) {
            for (const frame of this.parser.feed(this.decoder.decode(bytes.subarray(offset, offset + 4096)))) {
                const event = this.validator.accept(frame);
                const size = byteLength(frame.data);
                if (this.queue.length >= this.eventMaximum || this.queuedBytes + size > this.queueMaximum)
                    throw new ProtocolError("backpressure", "Event consumer exceeded the bounded queue");
                this.queue.push({ event, bytes: size });
                this.queuedBytes += size;
                this.notify();
                if (this.validator.terminal) {
                    this.close();
                    break;
                }
            }
        }
    }
    private complete(): void {
        if (!this.status)
            throw new ProtocolError("invalid_frame", "Response has no headers");
        if (this.streaming) {
            this.decoder.decode(undefined, true);
            this.validator.finish();
        }
        else {
            this.text += this.decoder.decode(undefined, true);
            const body = parseJSON(this.text);
            this.value = parseUnary(body, this.version, this.options.requestId, this.settings.schemaHash);
            if (this.status < 200 || this.status >= 300)
                throw new ProtocolError("http_error", "Unexpected HTTP status " + this.status);
            validateMessage(this.settings, this.operation.responseType, this.value);
        }
        this.close();
    }
    private notify(): void { const wake = this.wake; this.wake = undefined; wake?.(); }
    private dispose(): void {
        if (!this.task || this.disposed)
            return;
        this.disposed = true;
        try {
            this.task.dispose?.();
        }
        catch { /* cleanup cannot replace the result */ }
        try {
            this.task.abort();
        }
        catch { /* already completed */ }
    }
    close(): void {
        this.stopped = true;
        if (this.timer !== undefined)
            clearTimeout(this.timer);
        this.options.signal?.removeEventListener("abort", this.abort);
        this.pending = [];
        this.dispose();
        this.notify();
    }
    private reject(failure: ProtocolError): void {
        if (this.stopped)
            return;
        this.failure = failure;
        this.queue = [];
        this.queuedBytes = 0;
        this.close();
    }
    async unary(): Promise<unknown> {
        while (!this.stopped)
            await new Promise<void>(resolve => { this.wake = resolve; });
        if (this.failure)
            throw this.failure;
        return this.value;
    }
    async next(): Promise<IteratorResult<StreamEvent<unknown>>> {
        while (true) {
            if (this.failure)
                throw this.failure;
            const item = this.queue.shift();
            if (item) {
                this.queuedBytes -= item.bytes;
                return { done: false, value: item.event };
            }
            if (this.stopped)
                return { done: true, value: undefined };
            await new Promise<void>(resolve => { this.wake = resolve; });
        }
    }
}
export class CallbackTransport implements Transport {
    private baseURL: string;
    private version: string;
    private network: CallbackNetwork;
    private settings: CallbackOptions;
    constructor(baseURL: string, version: string, network: CallbackNetwork, settings: CallbackOptions) {
        this.baseURL = baseURL;
        this.version = version;
        this.network = network;
        this.settings = settings;
        const match = /^https?:\/\/[^/?#@\s]+(?:\/[^?#\s]*)?$/.exec(baseURL);
        if (!match || match[0] !== baseURL || !settings.schemaHash || !settings.wireSchema)
            throw new ProtocolError("bad_request", "HTTP(S) base URL and generated contract identity/schema are required");
    }
    async call(operation: Operation, request: unknown, options: CallOptions = {}): Promise<unknown> {
        if (operation.transport !== "http_json")
            throw new ProtocolError("invalid_operation", "Expected unary transport");
        const session = new Session(operation, this.version, this.settings, options);
        try {
            session.start(this.network, this.baseURL, request);
            return await session.unary();
        }
        finally {
            session.close();
        }
    }
    async *stream(operation: Operation, request: unknown, options: CallOptions = {}): AsyncIterable<StreamEvent<unknown>> {
        if (operation.transport !== "http_sse")
            throw new ProtocolError("invalid_operation", "Expected stream transport");
        const session = new Session(operation, this.version, this.settings, options);
        try {
            session.start(this.network, this.baseURL, request);
            while (true) {
                const event = await session.next();
                if (event.done)
                    return;
                yield event.value;
            }
        }
        finally {
            session.close();
        }
    }
}
