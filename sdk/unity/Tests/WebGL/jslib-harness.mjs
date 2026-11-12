// Loads TabForgeWebSocket.jslib outside Unity with a minimal Emscripten-like
// environment, and plays the C# side (WebGLWebSocketTransport) so the bridge
// can drive the TypeScript realtime client against a real server in Node.
//
// The heap allocator overwrites freed memory with garbage, so a bridge that
// keeps a view into the heap after a call returns corrupts frames and fails
// the protocol checks instead of passing by luck.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const jslibPath = fileURLToPath(new URL("../../Runtime/Plugins/WebGL/TabForgeWebSocket.jslib", import.meta.url));
const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });

function createHeap(bytes = 64 << 20) {
    const heap = { HEAPU8: new Uint8Array(bytes), live: new Map(), free: [], top: 8 };
    heap._malloc = size => {
        size = Math.max(8, (size + 7) & ~7);
        const reuse = heap.free.findIndex(block => block.size >= size);
        let ptr;
        if (reuse >= 0) {
            ptr = heap.free.splice(reuse, 1)[0].ptr;
        } else {
            ptr = heap.top;
            heap.top += size;
            if (heap.top > heap.HEAPU8.length)
                throw new Error("harness heap exhausted");
        }
        heap.live.set(ptr, size);
        return ptr;
    };
    heap._free = ptr => {
        const size = heap.live.get(ptr);
        if (size === undefined)
            throw new Error(`free of unknown pointer ${ptr}`);
        heap.live.delete(ptr);
        heap.HEAPU8.fill(0xa5, ptr, ptr + size);
        heap.free.push({ ptr, size });
    };
    heap.lengthBytesUTF8 = s => encoder.encode(s).length;
    heap.stringToUTF8 = (s, ptr, max) => {
        const bytes = encoder.encode(s).subarray(0, max - 1);
        heap.HEAPU8.set(bytes, ptr);
        heap.HEAPU8[ptr + bytes.length] = 0;
    };
    heap.UTF8ToString = ptr => {
        let end = ptr;
        while (heap.HEAPU8[end] !== 0)
            end++;
        return decoder.decode(heap.HEAPU8.subarray(ptr, end));
    };
    // Marshals a C# string argument: valid only during the call.
    heap.withString = (s, f) => {
        const ptr = heap._malloc(heap.lengthBytesUTF8(s) + 1);
        heap.stringToUTF8(s, ptr, heap.lengthBytesUTF8(s) + 1);
        try { return f(ptr); } finally { heap._free(ptr); }
    };
    return heap;
}

/** Evaluates the jslib and returns its exported functions plus the heap. */
export function loadJslib({ WebSocketImpl = globalThis.WebSocket } = {}) {
    const heap = createHeap();
    const callbacks = new Map();
    let nextCallback = 1;
    // Function pointers are table indices; makeDynCall resolves them.
    const table = f => { const index = nextCallback++; callbacks.set(index, f); return index; };
    const dynCall = index => {
        const f = callbacks.get(index);
        if (!f)
            throw new Error(`call through unregistered function pointer ${index}`);
        return f;
    };
    const source = readFileSync(jslibPath, "utf8")
        .replace(/\{\{\{\s*makeDynCall\('[a-z]+',\s*'([^']+)'\)\s*\}\}\}/g, "__dynCall($1)");
    if (source.includes("{{{"))
        throw new Error("unhandled Emscripten macro in jslib");
    // The library refers to these names as globals, as in an Emscripten build.
    // $TabForgeWS becomes the TabForgeWS global; HEAPU8 never grows here.
    const library = new Function(
        "HEAPU8", "_malloc", "_free", "UTF8ToString", "stringToUTF8", "lengthBytesUTF8", "__dynCall", "WebSocket",
        `"use strict";
        const LibraryManager = { library: {} };
        const mergeInto = (target, source) => Object.assign(target, source);
        let TabForgeWS;
        ${source}
        TabForgeWS = LibraryManager.library.$TabForgeWS;
        return LibraryManager.library;`
    )(heap.HEAPU8, heap._malloc, heap._free, heap.UTF8ToString, heap.stringToUTF8, heap.lengthBytesUTF8, dynCall, WebSocketImpl);
    return { library, heap, table };
}

/**
 * A TypeScript SocketFactory that goes through the jslib exactly like the C#
 * WebGLWebSocketTransport does: strings and payloads are marshalled into the
 * heap for one call only, and callbacks copy data out before returning.
 */
export function jslibSocketFactory(maxMessageBytes = 1 << 20, options) {
    const { library, heap, table } = loadJslib(options);
    const open = new Map();
    library.TabForgeWS_Init(
        table((id, protocolPtr) => open.get(id)?.open(heap.UTF8ToString(protocolPtr))),
        table((id, ptr, length, text) => {
            const events = open.get(id);
            if (!events)
                throw new Error(`message for unknown socket ${id}`);
            const bytes = heap.HEAPU8.slice(ptr, ptr + length);
            events.message(text ? decoder.decode(bytes) : bytes);
        }),
        table(id => {
            const events = open.get(id);
            if (!events)
                throw new Error(`close delivered twice or for unknown socket ${id}`);
            open.delete(id);
            events.close();
        }));
    const factory = (url, protocols, events) => {
        const id = heap.withString(url, u => heap.withString(protocols[0], p => library.TabForgeWS_Open(u, p, maxMessageBytes)));
        open.set(id, events);
        return {
            send(data) {
                const text = typeof data === "string";
                const bytes = text ? encoder.encode(data) : data;
                const ptr = heap._malloc(bytes.length || 1);
                heap.HEAPU8.set(bytes, ptr);
                let ok;
                try { ok = library.TabForgeWS_Send(id, ptr, bytes.length, text ? 1 : 0); } finally { heap._free(ptr); }
                if (!ok)
                    throw new Error("socket is closed");
            },
            close() { library.TabForgeWS_Close(id); }
        };
    };
    factory.openSockets = () => open.size;
    factory.heap = heap;
    return factory;
}
