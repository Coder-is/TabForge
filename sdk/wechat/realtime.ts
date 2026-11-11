/** WeChat mini-program / mini-game socket for the realtime channel client. */
import type { SocketFactory } from "../typescript/realtime.ts";

export interface WxSocketTask {
    send(options: { data: string | ArrayBuffer; fail?(error: { errMsg?: string }): void }): void;
    close(options?: { code?: number; reason?: string }): void;
    onOpen(listener: (result: { header?: Record<string, unknown> }) => void): void;
    onMessage(listener: (result: { data: string | ArrayBuffer }) => void): void;
    onClose(listener: (result: unknown) => void): void;
    onError(listener: (result: unknown) => void): void;
}
export interface WxSocketAPI {
    connectSocket(options: { url: string; protocols: string[]; timeout?: number; perMessageDeflate?: boolean; fail?(error: { errMsg?: string }): void }): WxSocketTask;
}

export interface WechatSocketOptions {
    /** Handshake timeout in milliseconds (default 10000). */
    connectTimeoutMS?: number;
    /** Offer permessage-deflate; used only if the server enables Compression. */
    perMessageDeflate?: boolean;
}

/**
 * Uses wx.connectSocket. Pass it as the `socket` option of RealtimeClient.
 * Each client owns its SocketTask, so several channels can be open at once
 * (the base library limits concurrent sockets; check your target version).
 * The second argument may also be the handshake timeout in milliseconds.
 */
export function wechatSocketFactory(wx: WxSocketAPI, options: number | WechatSocketOptions = {}): SocketFactory {
    const { connectTimeoutMS = 10000, perMessageDeflate = false } = typeof options === "number" ? { connectTimeoutMS: options } : options;
    return (url, protocols, events) => {
        let closed = false;
        const done = () => {
            if (!closed) {
                closed = true;
                events.close();
            }
        };
        const task = wx.connectSocket({ url, protocols, timeout: connectTimeoutMS, perMessageDeflate, fail: done });
        task.onOpen(result => {
            const header = result?.header ?? {};
            const key = Object.keys(header).find(name => name.toLowerCase() === "sec-websocket-protocol");
            events.open(key ? String(header[key]) : "");
        });
        task.onMessage(result => events.message(result.data));
        task.onClose(done);
        task.onError(done);
        return {
            send: data => {
                // wx sends ArrayBuffer, not views: copy the exact byte range.
                const payload = typeof data === "string" ? data : data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer;
                task.send({ data: payload, fail: done });
            },
            close: () => {
                try { task.close({ code: 1000 }); } catch { /* already closed */ }
                done();
            }
        };
    };
}
