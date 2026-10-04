import { ProtocolError } from "./runtime.ts";
/** Strict incremental decoder for JS runtimes without TextDecoder. */
export class UTF8Decoder {
    private pending: number[] = [];
    decode(bytes: Uint8Array = new Uint8Array(), final = false): string {
        const data = this.pending.concat(Array.from(bytes));
        this.pending = [];
        let text = "";
        for (let i = 0; i < data.length;) {
            const first = data[i];
            const count = first <= 0x7f ? 1 : first >= 0xc2 && first <= 0xdf ? 2 : first >= 0xe0 && first <= 0xef ? 3 : first >= 0xf0 && first <= 0xf4 ? 4 : 0;
            if (!count)
                throw new ProtocolError("invalid_utf8", "Invalid UTF-8 lead byte");
            if (i + count > data.length) {
                for (let j = i + 1; j < data.length; j++)
                    if ((data[j] & 0xc0) !== 0x80)
                        throw new ProtocolError("invalid_utf8", "Invalid UTF-8 continuation");
                this.pending = data.slice(i);
                break;
            }
            let code = first & (count === 1 ? 0x7f : (1 << (7 - count)) - 1);
            for (let j = 1; j < count; j++) {
                if ((data[i + j] & 0xc0) !== 0x80)
                    throw new ProtocolError("invalid_utf8", "Invalid UTF-8 continuation");
                code = (code << 6) | (data[i + j] & 0x3f);
            }
            if ((count === 2 && code < 0x80) || (count === 3 && code < 0x800) || (count === 4 && code < 0x10000) || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff))
                throw new ProtocolError("invalid_utf8", "Invalid UTF-8 scalar");
            text += code <= 0xffff ? String.fromCharCode(code) : String.fromCharCode(0xd800 + ((code - 0x10000) >> 10), 0xdc00 + ((code - 0x10000) & 0x3ff));
            i += count;
        }
        if (final && this.pending.length)
            throw new ProtocolError("invalid_utf8", "Truncated UTF-8 scalar");
        return text;
    }
}
export class CancellationSource {
    private listeners = new Set<() => void>();
    private cancelled = false;
    readonly signal = {
        get aborted(): boolean { return false; },
        addEventListener: (_type: "abort", listener: () => void) => { this.listeners.add(listener); },
        removeEventListener: (_type: "abort", listener: () => void) => { this.listeners.delete(listener); }
    };
    constructor() { Object.defineProperty(this.signal, "aborted", { get: () => this.cancelled }); }
    cancel(): void {
        if (this.cancelled)
            return;
        this.cancelled = true;
        const listeners = Array.from(this.listeners);
        this.listeners.clear();
        for (const listener of listeners) {
            try {
                listener();
            }
            catch { /* One application listener must not prevent network cleanup. */ }
        }
    }
}
