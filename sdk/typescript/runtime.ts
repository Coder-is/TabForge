/** TabForge v1 ProtoJSON transport. No dependency on a model provider or engine. */
export interface Operation {
  path: string;
  transport: "http_json" | "http_sse";
  auth: "none" | "bearer";
  timeoutMS: number;
  events?: Readonly<Record<string, { field: string; terminal: boolean }>>;
}
export interface TransportError { code: string; message: string; retryable: boolean }
export interface CallOptions { signal?: AbortSignal; token?: string; requestId?: string }
export interface StreamEvent<T> { name: string; requestId: string; sequence: string; payload: T }
export interface Transport {
  call(operation: Operation, request: unknown, options?: CallOptions): Promise<unknown>;
  stream(operation: Operation, request: unknown, options?: CallOptions): AsyncIterable<StreamEvent<unknown>>;
}

export class ProtocolError extends Error {
  code: string;
  retryable: boolean;
  constructor(code: string, message: string, retryable = false) {
    super(message);
    this.name = "ProtocolError";
    this.code = code;
    this.retryable = retryable;
  }
}

/** The core client accepts a platform transport; only FetchTransport needs fetch. */
export class ProtocolClient<P extends { [K in keyof P]: { request: unknown; response: unknown } }> {
  private operations: { [K in keyof P]: Operation };
  private transport: Transport;
  constructor(operations: { [K in keyof P]: Operation }, transport: Transport) {
    this.operations = operations;
    this.transport = transport;
  }
  async call<K extends keyof P>(id: K, request: P[K]["request"], options?: CallOptions): Promise<P[K]["response"]> {
    const operation = this.operations[id];
    if (!operation || operation.transport !== "http_json") throw new ProtocolError("invalid_operation", "Expected a unary operation");
    return await this.transport.call(operation, request, options) as P[K]["response"];
  }
  async *stream<K extends keyof P>(id: K, request: P[K]["request"], options?: CallOptions): AsyncIterable<StreamEvent<P[K]["response"]>> {
    const operation = this.operations[id];
    if (!operation || operation.transport !== "http_sse") throw new ProtocolError("invalid_operation", "Expected a stream operation");
    for await (const event of this.transport.stream(operation, request, options)) yield event as StreamEvent<P[K]["response"]>;
  }
}

export interface SSEFrame { event: string; id: string; data: string }

/** Incremental SSE framing; callers must decode UTF-8 incrementally before feed.
 * Handles CR, LF, CRLF, BOM, comments, multiline data and arbitrary chunk cuts.
 * SSE records without data are ignored; an unterminated record is not dispatched.
 */
export class SSEParser {
  private line = "";
  private data: string[] = [];
  private name = "";
  private id = "";
  private skipLF = false;
  private first = true;
  private chars = 0;
  private maximum: number;
  constructor(maximum = 1 << 20) { this.maximum = maximum; }
  feed(chunk: string): SSEFrame[] {
    const frames: SSEFrame[] = [];
    for (let i = 0; i < chunk.length; i++) {
      const char = chunk[i];
      if (this.first) { this.first = false; if (char === "\uFEFF") continue; }
      if (this.skipLF) { this.skipLF = false; if (char === "\n") continue; }
      if (++this.chars > this.maximum) throw new ProtocolError("frame_too_large", "SSE frame exceeds the configured limit");
      if (char === "\r" || char === "\n") {
        this.consume(frames);
        this.skipLF = char === "\r";
      } else this.line += char;
    }
    return frames;
  }
  private consume(frames: SSEFrame[]): void {
    const line = this.line;
    this.line = "";
    if (line === "") {
      if (this.data.length) frames.push({ event: this.name || "message", id: this.id, data: this.data.join("\n") });
      this.data = [];
      this.name = "";
      this.chars = 0;
      return;
    }
    if (line.startsWith(":")) return;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "data") this.data.push(value);
    else if (field === "event") this.name = value;
    else if (field === "id" && !value.includes("\0")) this.id = value;
  }
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ProtocolError("invalid_frame", "Expected an object");
  return value as Record<string, unknown>;
}
function parseJSON(data: string): unknown {
  try { return JSON.parse(data); }
  catch { throw new ProtocolError("invalid_json", "Invalid JSON response"); }
}
function remoteError(value: unknown): ProtocolError {
  const error = object(value);
  if (typeof error.code !== "string" || typeof error.message !== "string" || typeof error.retryable !== "boolean") {
    throw new ProtocolError("invalid_frame", "Invalid transport error");
  }
  return new ProtocolError(error.code, error.message, error.retryable);
}
function nextSequence(sequence: string): string {
  const digits = sequence.split("");
  for (let i = digits.length - 1; i >= 0; i--) {
    if (digits[i] !== "9") { digits[i] = String(Number(digits[i]) + 1); return digits.join(""); }
    digits[i] = "0";
  }
  return "1" + digits.join("");
}

/** Shared validation for browser and custom mini-program/engine transports. */
export class StreamValidator {
  private operation: Operation;
  private version: string;
  private requestID?: string;
  private sequence = "0";
  private ended = false;
  constructor(operation: Operation, version: string, requestID?: string) {
    this.operation = operation;
    this.version = version;
    this.requestID = requestID;
  }
  get terminal(): boolean { return this.ended; }
  accept(frame: SSEFrame): StreamEvent<unknown> {
    if (this.ended) throw new ProtocolError("invalid_frame", "Event after a terminal event");
    const body = object(parseJSON(frame.data));
    validateEnvelope(body, this.version, this.requestID);
    this.requestID = body.requestId as string;
    const expected = nextSequence(this.sequence);
    if (body.sequence !== expected || frame.id !== expected) throw new ProtocolError("invalid_sequence", "SSE id and sequence must increment from 1");
    this.sequence = expected;
    if (frame.event === "protocol.error") {
      if ("payload" in body) throw new ProtocolError("invalid_frame", "Error frame cannot contain payload");
      this.ended = true;
      throw remoteError(body.error);
    }
    const events = this.operation.events;
    const event = events && Object.prototype.hasOwnProperty.call(events, frame.event) ? events[frame.event] : undefined;
    if (!event || "error" in body) throw new ProtocolError("invalid_event", "Unknown or conflicting stream event");
    const payload = object(body.payload);
    if (Object.keys(payload).length !== 1 || !Object.prototype.hasOwnProperty.call(payload, event.field)) throw new ProtocolError("invalid_event", "Event does not match the response oneof");
    this.ended = event.terminal;
    return { name: frame.event, requestId: this.requestID, sequence: expected, payload };
  }
  finish(): void {
    if (!this.ended) throw new ProtocolError("incomplete_stream", "Connection ended before a terminal event");
  }
}

function validateEnvelope(body: Record<string, unknown>, version: string, requestID?: string): void {
  if (body.protocolVersion !== version) throw new ProtocolError("version_mismatch", "Response protocol version differs");
  if (typeof body.requestId !== "string" || !/^[A-Za-z0-9_.-]{1,128}$/.test(body.requestId) || (requestID && requestID !== body.requestId)) {
    throw new ProtocolError("invalid_frame", "Invalid or inconsistent request ID");
  }
}

export function parseUnary(value: unknown, version: string, requestID?: string): unknown {
  const body = object(value);
  validateEnvelope(body, version, requestID);
  if ("error" in body) {
    if ("data" in body) throw new ProtocolError("invalid_frame", "Response contains data and error");
    throw remoteError(body.error);
  }
  if (!("data" in body)) throw new ProtocolError("invalid_frame", "Response has no data");
  return body.data;
}

/** Browser/Node fetch adapter. Supply a custom Transport for other network APIs.
 * Timeout covers the whole stream. Breaking iteration aborts the network call.
 * Does not retry or reconnect, because replay/idempotency is application-specific.
 */
export class FetchTransport implements Transport {
  private baseURL: string;
  private version: string;
  private fetcher: typeof fetch;
  constructor(baseURL: string, version: string, fetcher: typeof fetch = globalThis.fetch) {
    this.baseURL = baseURL.replace(/\/$/, "");
    this.version = version;
    this.fetcher = fetcher;
  }
  private setup(operation: Operation, options: CallOptions = {}) {
    if (operation.auth === "bearer" && !options.token) throw new ProtocolError("unauthorized", "Bearer token is required");
    if (options.requestId && !/^[A-Za-z0-9_.-]{1,128}$/.test(options.requestId)) throw new ProtocolError("bad_request", "Invalid request ID");
    const controller = new AbortController();
    let timedOut = false;
    const abort = () => controller.abort();
    options.signal?.addEventListener("abort", abort, { once: true });
    if (options.signal?.aborted) abort();
    const timer = setTimeout(() => { timedOut = true; abort(); }, operation.timeoutMS);
    const headers: Record<string, string> = { "Content-Type": "application/json", "X-Protocol-Version": this.version, "Accept": operation.transport === "http_sse" ? "text/event-stream" : "application/json" };
    if (options.token) headers.Authorization = "Bearer " + options.token;
    if (options.requestId) headers["X-Request-ID"] = options.requestId;
    return {
      controller, headers,
      error: (error: unknown) => {
        if (timedOut) return new ProtocolError("timeout", "Request deadline exceeded", true);
        if (options.signal?.aborted) return new ProtocolError("cancelled", "Request cancelled");
        if (error instanceof ProtocolError) return error;
        return new ProtocolError("transport_error", error instanceof Error ? error.message : "Transport failed");
      },
      cleanup: () => { clearTimeout(timer); options.signal?.removeEventListener("abort", abort); controller.abort(); }
    };
  }
  async call(operation: Operation, request: unknown, options: CallOptions = {}): Promise<unknown> {
    const scope = this.setup(operation, options);
    try {
      const response = await this.fetcher(this.baseURL + operation.path, { method: "POST", headers: scope.headers, body: JSON.stringify(request), signal: scope.controller.signal });
      if (response.headers.get("content-type")?.split(";")[0].trim() !== "application/json") {
        throw new ProtocolError("unsupported_media_type", "Expected a JSON response body");
      }
      const body = await response.json();
      const data = parseUnary(body, this.version, options.requestId);
      if (!response.ok) throw new ProtocolError("http_error", "Unexpected HTTP status " + response.status);
      return data;
    } catch (error) { throw scope.error(error); }
    finally { scope.cleanup(); }
  }
  async *stream(operation: Operation, request: unknown, options: CallOptions = {}): AsyncIterable<StreamEvent<unknown>> {
    const scope = this.setup(operation, options);
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    try {
      const response = await this.fetcher(this.baseURL + operation.path, { method: "POST", headers: scope.headers, body: JSON.stringify(request), signal: scope.controller.signal });
      if (!response.ok) {
        parseUnary(await response.json(), this.version, options.requestId);
        throw new ProtocolError("http_error", "Unexpected HTTP status " + response.status);
      }
      if (response.headers.get("content-type")?.split(";")[0].trim() !== "text/event-stream" || !response.body) {
        throw new ProtocolError("unsupported_media_type", "Expected an SSE response body");
      }
      reader = response.body.getReader();
      const decoder = new TextDecoder("utf-8", { fatal: true });
      const parser = new SSEParser();
      const validator = new StreamValidator(operation, this.version, options.requestId);
      while (true) {
        const chunk = await reader.read();
        const text = chunk.done ? decoder.decode() : decoder.decode(chunk.value, { stream: true });
        for (const frame of parser.feed(text)) {
          const event = validator.accept(frame);
          yield event;
          if (validator.terminal) return;
        }
        if (chunk.done) { validator.finish(); return; }
      }
    } catch (error) { throw scope.error(error); }
    finally {
      scope.cleanup();
      // A real fetch reader may reject cancel() after controller.abort().
      // Cleanup must not replace a successful terminal event or the real error.
      if (reader) { try { await reader.cancel(); } catch { /* already aborted */ } finally { reader.releaseLock(); } }
    }
  }
}
