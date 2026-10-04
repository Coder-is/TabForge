import { CallbackTransport } from "../typescript/callback_transport.ts";
import type { CallbackNetwork, CallbackOptions, NetworkTask } from "../typescript/callback_transport.ts";
import { ProtocolError } from "../typescript/runtime.ts";
export { CancellationSource } from "../typescript/utf8.ts";
export interface WxResponse {
    statusCode: number;
    header: Record<string, unknown>;
    data: ArrayBuffer;
}
export interface WxHeaders {
    statusCode?: number;
    header: Record<string, unknown>;
}
export interface WxRequestTask {
    abort(): void;
    onHeadersReceived?(listener: (response: WxHeaders) => void): void;
    offHeadersReceived?(listener: (response: WxHeaders) => void): void;
    onChunkReceived?(listener: (response: {
        data: ArrayBuffer;
    }) => void): void;
    offChunkReceived?(listener: (response: {
        data: ArrayBuffer;
    }) => void): void;
}
export interface WxAPI {
    request(options: {
        url: string;
        method: "POST";
        header: Record<string, string>;
        data: string;
        responseType: "arraybuffer";
        dataType: "text";
        enableChunked: boolean;
        timeout: number;
        redirect: "manual";
        success(response: WxResponse): void;
        fail(error: {
            errMsg?: string;
        }): void;
    }): WxRequestTask;
}
function header(headers: Record<string, unknown>, name: string): string {
    const key = Object.keys(headers).find(key => key.toLowerCase() === name);
    return key && typeof headers[key] === "string" ? headers[key] as string : "";
}
export function wechatNetwork(api: WxAPI): CallbackNetwork {
    return (request, callbacks): NetworkTask => {
        let sentHeaders = false, chunkReceived = false, disposed = false;
        const headers = (response: WxHeaders) => {
            if (disposed || sentHeaders)
                return;
            let status = response.statusCode;
            // Tencent's typings document that DevTools may omit statusCode here.
            // This protocol server only uses SSE for successful responses. Infer 200
            // only when its identity headers match; JSON/error responses wait for the
            // completion callback's actual HTTP status. Device callbacks use statusCode.
            if (status === undefined && request.streaming && header(response.header, "content-type").split(";")[0].trim().toLowerCase() === "text/event-stream" && header(response.header, "x-protocol-schema") === request.headers["X-Protocol-Schema"] && header(response.header, "x-protocol-version") === request.headers["X-Protocol-Version"])
                status = 200;
            if (status !== undefined) {
                sentHeaders = true;
                callbacks.headers(status, response.header);
            }
        };
        const chunk = (response: {
            data: ArrayBuffer;
        }) => {
            if (disposed)
                return;
            if (!(response.data instanceof ArrayBuffer)) {
                callbacks.fail(new ProtocolError("invalid_frame", "wx.request chunk is not an ArrayBuffer"));
                return;
            }
            chunkReceived = true;
            callbacks.chunk(new Uint8Array(response.data));
        };
        const task = api.request({
            url: request.url, method: "POST", header: request.headers, data: request.body,
            responseType: "arraybuffer", dataType: "text", enableChunked: request.streaming,
            timeout: request.timeoutMS, redirect: "manual",
            success: response => {
                if (disposed)
                    return;
                headers(response);
                if (!chunkReceived) {
                    if (request.streaming && response.statusCode >= 200 && response.statusCode < 300) {
                        callbacks.fail(new ProtocolError("unsupported_transport", "wx.request did not deliver streaming chunks"));
                        return;
                    }
                    chunk({ data: response.data });
                }
                callbacks.complete();
            },
            fail: failure => {
                if (!disposed)
                    callbacks.fail(new ProtocolError((failure.errMsg ?? "").includes("timeout") ? "timeout" : "transport_error", failure.errMsg ?? "wx.request failed", (failure.errMsg ?? "").includes("timeout")));
            }
        });
        if (request.streaming && (!task.onChunkReceived || !task.onHeadersReceived)) {
            callbacks.fail(new ProtocolError("unsupported_transport", "RequestTask chunk/header callbacks are unavailable"));
        }
        else {
            task.onHeadersReceived?.(headers);
            if (request.streaming)
                task.onChunkReceived?.(chunk);
        }
        return {
            abort: () => task.abort(),
            dispose: () => {
                disposed = true;
                task.offHeadersReceived?.(headers);
                if (request.streaming)
                    task.offChunkReceived?.(chunk);
            }
        };
    };
}
/** Inject the real wx object, or the same request API exposed by a mini-game. */
export class WechatTransport extends CallbackTransport {
    constructor(baseURL: string, version: string, api: WxAPI, options: CallbackOptions) { super(baseURL, version, wechatNetwork(api), options); }
}
