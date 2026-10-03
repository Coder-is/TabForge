import { CallbackTransport } from "../typescript/callback_transport.ts";
import type { CallbackNetwork, CallbackOptions } from "../typescript/callback_transport.ts";
import { ProtocolError } from "../typescript/runtime.ts";
import { WechatTransport } from "../wechat/transport.ts";
export { CancellationSource } from "../typescript/utf8.ts";
export { WechatTransport }; // Cocos builds targeting WeChat use wx.request.

export interface CocosOptions extends CallbackOptions {
  backend?: "fetch" | "xhr";
  fetcher?: typeof fetch;
  xhrFactory?: () => XMLHttpRequest;
  /** XHR retains its entire responseText, so cap the whole stream as well. */
  maxXHRResponseChars?: number;
}
function xhrNetwork(factory: () => XMLHttpRequest, maximum: number): CallbackNetwork {
  return (request, callbacks) => {
    const xhr = factory();
    let headersSent = false, offset = 0, disposed = false, progressive = false;
    const headers = () => {
      if (headersSent || xhr.readyState < 2 || xhr.status === 0) return;
      const values: Record<string, string> = {};
      for (const line of xhr.getAllResponseHeaders().split(/\r?\n/)) {
        const colon = line.indexOf(":");
        if (colon >= 0) values[line.slice(0, colon).trim()] = line.slice(colon + 1).trim();
      }
      headersSent = true; callbacks.headers(xhr.status, values);
    };
    const receive = () => {
      if (disposed || xhr.readyState < 3) return;
      headers();
      const text = xhr.responseText;
      if (text.length > maximum) { callbacks.fail(new ProtocolError("response_too_large", "XHR retained response exceeds configured limit")); return; }
      if (text.length < offset) { callbacks.fail(new ProtocolError("invalid_frame", "XHR responseText is not cumulative")); return; }
      // XHR already decoded UTF-8. Keep a trailing high surrogate until its
      // matching low surrogate arrives before re-encoding the new text suffix.
      let end = text.length;
      if (xhr.readyState !== 4 && end > offset && text.charCodeAt(end - 1) >= 0xd800 && text.charCodeAt(end - 1) <= 0xdbff) end--;
      if (end > offset) {
        if (xhr.readyState !== 4) progressive = true;
        callbacks.chunk(encodeUTF8(text.slice(offset, end))); offset = end;
      }
    };
    xhr.open("POST", request.url, true);
    xhr.responseType = "text";
    xhr.timeout = request.timeoutMS;
    for (const [name, value] of Object.entries(request.headers)) xhr.setRequestHeader(name, value);
    xhr.onreadystatechange = () => { if (!disposed) headers(); };
    xhr.onprogress = () => { try { receive(); } catch (error) { callbacks.fail(error); } };
    xhr.onload = () => {
      if (disposed) return;
      try {
        if (request.streaming && xhr.status >= 200 && xhr.status < 300 && !progressive) { callbacks.fail(new ProtocolError("unsupported_transport", "XHR did not expose incremental responseText")); return; }
        receive(); callbacks.complete();
      } catch (error) { callbacks.fail(error); }
    };
    xhr.onerror = () => { if (!disposed) callbacks.fail(new ProtocolError("transport_error", "XHR network request failed")); };
    xhr.ontimeout = () => { if (!disposed) callbacks.fail(new ProtocolError("timeout", "XHR request deadline exceeded", true)); };
    xhr.onabort = () => { if (!disposed) callbacks.fail(new ProtocolError("cancelled", "XHR request cancelled")); };
    xhr.send(request.body);
    return { abort: () => xhr.abort(), dispose: () => { disposed = true; xhr.onreadystatechange = null; xhr.onprogress = xhr.onload = xhr.onerror = xhr.ontimeout = xhr.onabort = null; } };
  };
}
export function encodeUTF8(text: string): Uint8Array {
  const bytes: number[] = [];
  for (let i = 0; i < text.length; i++) {
    let code = text.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const low = text.charCodeAt(++i);
      if (!(low >= 0xdc00 && low <= 0xdfff)) throw new ProtocolError("invalid_utf8", "Unpaired UTF-16 surrogate");
      code = 0x10000 + ((code - 0xd800) << 10) + low - 0xdc00;
    } else if (code >= 0xdc00 && code <= 0xdfff) throw new ProtocolError("invalid_utf8", "Unpaired UTF-16 surrogate");
    if (code < 0x80) bytes.push(code);
    else if (code < 0x800) bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    else if (code < 0x10000) bytes.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    else bytes.push(0xf0 | (code >> 18), 0x80 | ((code >> 12) & 0x3f), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
  }
  return new Uint8Array(bytes);
}
function fetchNetwork(fetcher: typeof fetch): CallbackNetwork {
  return (request, callbacks) => {
    if (typeof AbortController === "undefined") throw new ProtocolError("unsupported_transport", "Fetch backend requires AbortController");
    const controller = new AbortController();
    let disposed = false;
    void (async () => {
      try {
        const response = await fetcher(request.url, { method: "POST", headers: request.headers, body: request.body, signal: controller.signal });
        if (disposed) return;
        const headers: Record<string, string> = {};
        response.headers.forEach((value, name) => { headers[name] = value; });
        callbacks.headers(response.status, headers);
        if (!response.body?.getReader) throw new ProtocolError("unsupported_transport", "Fetch backend requires a readable byte stream; use the XHR backend for native unary requests");
        const reader = response.body.getReader();
        try {
          while (!disposed) { const chunk = await reader.read(); if (chunk.done) { callbacks.complete(); return; } callbacks.chunk(chunk.value); }
        } finally { try { await reader.cancel(); } catch { /* aborted */ } reader.releaseLock(); }
      } catch (error) { if (!disposed) callbacks.fail(error); }
    })();
    return { abort: () => controller.abort(), dispose: () => { disposed = true; } };
  };
}

/** Cocos web uses fetch byte streams; native builds may inject progressive XHR.
 * Builds targeting WeChat should instantiate WechatTransport with wx instead. */
export class CocosTransport extends CallbackTransport {
  constructor(baseURL: string, version: string, options: CocosOptions) {
    if (options.backend !== undefined && options.backend !== "xhr" && options.backend !== "fetch") throw new ProtocolError("bad_request", "Unknown Cocos network backend");
    const maximum = options.maxXHRResponseChars ?? (4 << 20);
    if (!Number.isSafeInteger(maximum) || maximum < 1) throw new ProtocolError("bad_request", "Invalid XHR response limit");
    const network = options.backend === "xhr" ? xhrNetwork(options.xhrFactory ?? (() => new XMLHttpRequest()), maximum) : fetchNetwork(options.fetcher ?? globalThis.fetch);
    super(baseURL, version, network, options);
  }
}
