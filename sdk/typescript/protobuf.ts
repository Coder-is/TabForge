/**
 * Binary Protobuf <-> ProtoJSON-shaped objects, driven by the generated
 * codecSchema. Applications keep using the generated ProtoJSON types; only the
 * wire is binary. Output equals Go protojson.Marshal of the same message
 * (64-bit integers as decimal strings, enums by name, bytes as Base64, default
 * scalars omitted). No dependency on TextEncoder, BigInt64Array or DataView
 * BigInt methods, so it runs in mini-game and engine JS runtimes.
 */
export interface CodecField {
    readonly no: number;
    readonly name: string;
    readonly kind: string;
    readonly type?: string;
    readonly list?: boolean;
    readonly packed?: boolean;
    readonly mapKey?: string;
    readonly presence?: boolean;
    readonly oneof?: number;
}
export interface CodecSchema {
    readonly messages: Readonly<Record<string, readonly CodecField[]>>;
    readonly enums: Readonly<Record<string, readonly (readonly [string, number])[]>>;
}
export class CodecError extends Error {
    constructor(message: string) {
        super(message);
        this.name = "CodecError";
    }
}
const fail = (message: string): never => { throw new CodecError(message); };
const hasOwn = (value: object, key: PropertyKey): boolean => Object.prototype.hasOwnProperty.call(value, key);
const MAX_DEPTH = 100;
const VARINT = 0, I64 = 1, LEN = 2, I32 = 5;
const LONG_KINDS = new Set(["int64", "uint64", "sint64", "fixed64", "sfixed64"]);
const PACKABLE = new Set(["int32", "int64", "uint32", "uint64", "sint32", "sint64", "bool", "enum", "fixed32", "sfixed32", "float", "fixed64", "sfixed64", "double"]);
const WRAPPERS: Readonly<Record<string, string>> = {
    "google.protobuf.DoubleValue": "double", "google.protobuf.FloatValue": "float", "google.protobuf.Int64Value": "int64",
    "google.protobuf.UInt64Value": "uint64", "google.protobuf.Int32Value": "int32", "google.protobuf.UInt32Value": "uint32",
    "google.protobuf.BoolValue": "bool", "google.protobuf.StringValue": "string", "google.protobuf.BytesValue": "bytes"
};

function wireType(kind: string): number {
    switch (kind) {
        case "fixed64": case "sfixed64": case "double": return I64;
        case "fixed32": case "sfixed32": case "float": return I32;
        case "string": case "bytes": case "message": return LEN;
        default: return VARINT;
    }
}

// ---- UTF-8 and Base64 without platform APIs ----

export function encodeUTF8(text: string): Uint8Array {
    const out: number[] = [];
    for (let i = 0; i < text.length; i++) {
        let code = text.charCodeAt(i);
        if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length) {
            const low = text.charCodeAt(i + 1);
            if (low >= 0xdc00 && low <= 0xdfff) {
                code = 0x10000 + ((code - 0xd800) << 10) + (low - 0xdc00);
                i++;
            }
        }
        if (code >= 0xd800 && code <= 0xdfff)
            fail("string contains an unpaired surrogate");
        if (code < 0x80)
            out.push(code);
        else if (code < 0x800)
            out.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
        else if (code < 0x10000)
            out.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
        else
            out.push(0xf0 | (code >> 18), 0x80 | ((code >> 12) & 0x3f), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    }
    return Uint8Array.from(out);
}

export function decodeUTF8(bytes: Uint8Array, start = 0, end = bytes.length): string {
    let text = "";
    for (let i = start; i < end;) {
        const first = bytes[i];
        const count = first <= 0x7f ? 1 : first >= 0xc2 && first <= 0xdf ? 2 : first >= 0xe0 && first <= 0xef ? 3 : first >= 0xf0 && first <= 0xf4 ? 4 : 0;
        if (!count || i + count > end)
            fail("invalid UTF-8");
        let code = first & (count === 1 ? 0x7f : (1 << (7 - count)) - 1);
        for (let j = 1; j < count; j++) {
            if ((bytes[i + j] & 0xc0) !== 0x80)
                fail("invalid UTF-8");
            code = (code << 6) | (bytes[i + j] & 0x3f);
        }
        if ((count === 2 && code < 0x80) || (count === 3 && code < 0x800) || (count === 4 && code < 0x10000) || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff))
            fail("invalid UTF-8");
        text += code <= 0xffff ? String.fromCharCode(code) : String.fromCharCode(0xd800 + ((code - 0x10000) >> 10), 0xdc00 + ((code - 0x10000) & 0x3ff));
        i += count;
    }
    return text;
}

const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
export function encodeBase64(bytes: Uint8Array): string {
    let out = "";
    for (let i = 0; i < bytes.length; i += 3) {
        const n = (bytes[i] << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0);
        out += BASE64[n >> 18] + BASE64[(n >> 12) & 63] + (i + 1 < bytes.length ? BASE64[(n >> 6) & 63] : "=") + (i + 2 < bytes.length ? BASE64[n & 63] : "=");
    }
    return out;
}
/** Decodes canonical (validator-normalized) standard Base64. */
export function decodeBase64(text: string): Uint8Array {
    const clean = text.replace(/=+$/, "");
    const out = new Uint8Array(Math.floor(clean.length * 3 / 4));
    let bits = 0, value = 0, n = 0;
    for (let i = 0; i < clean.length; i++) {
        const index = BASE64.indexOf(clean[i]);
        if (index < 0)
            fail("invalid Base64");
        value = (value << 6) | index;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            out[n++] = (value >> bits) & 0xff;
        }
    }
    return out.subarray(0, n);
}

// ---- Timestamp and Duration (Go protojson formats) ----

function civilDays(year: number, month: number, day: number): number {
    const y = year + 400 - (month <= 2 ? 1 : 0), era = Math.floor(y / 400), yoe = y - era * 400;
    const doy = Math.floor((153 * ((month + 9) % 12) + 2) / 5) + day - 1;
    return era * 146097 + yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy - 719468 - 146097;
}
function civilFromDays(days: number): [number, number, number] {
    const z = days + 719468, era = Math.floor(z / 146097), doe = z - era * 146097;
    const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
    const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100)), mp = Math.floor((5 * doy + 2) / 153);
    const day = doy - Math.floor((153 * mp + 2) / 5) + 1, month = mp < 10 ? mp + 3 : mp - 9;
    return [yoe + era * 400 + (month <= 2 ? 1 : 0), month, day];
}
const pad = (value: number, width: number): string => String(value).padStart(width, "0");
function fraction(nanos: number): string {
    if (nanos === 0)
        return "";
    const digits = pad(nanos, 9);
    return "." + (nanos % 1000000 === 0 ? digits.slice(0, 3) : nanos % 1000 === 0 ? digits.slice(0, 6) : digits);
}
const TIMESTAMP = /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:[.,]([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$/;
function parseTimestamp(text: string): { seconds: string; nanos: number } {
    const m = TIMESTAMP.exec(text) ?? fail("invalid timestamp");
    let seconds = civilDays(Number(m[1]), Number(m[2]), Number(m[3])) * 86400 + Number(m[4]) * 3600 + Number(m[5]) * 60 + Number(m[6]);
    if (m[8])
        seconds -= (m[8] === "-" ? -1 : 1) * (Number(m[9]) * 3600 + Number(m[10]) * 60);
    const nanos = m[7] ? Number((m[7] + "00000000").slice(0, 9)) : 0;
    return { seconds: String(seconds), nanos };
}
function formatTimestamp(seconds: bigint, nanos: number): string {
    if (seconds < -62135596800n || seconds > 253402300799n || nanos < 0 || nanos > 999999999)
        fail("timestamp out of range");
    const s = Number(seconds), days = Math.floor(s / 86400), rest = s - days * 86400;
    const [year, month, day] = civilFromDays(days);
    return `${pad(year, 4)}-${pad(month, 2)}-${pad(day, 2)}T${pad(Math.floor(rest / 3600), 2)}:${pad(Math.floor(rest / 60) % 60, 2)}:${pad(rest % 60, 2)}${fraction(nanos)}Z`;
}
function parseDuration(text: string): { seconds: string; nanos: number } {
    const m = /^(-)?([0-9]+)(?:\.([0-9]{1,9}))?s$/.exec(text) ?? fail("invalid duration");
    const nanos = m[3] ? Number((m[3] + "00000000").slice(0, 9)) : 0;
    const negative = m[1] === "-";
    return { seconds: (negative && m[2] !== "0" ? "-" : "") + BigInt(m[2]).toString(), nanos: negative ? -nanos : nanos };
}
function formatDuration(seconds: bigint, nanos: number): string {
    if (seconds < -315576000000n || seconds > 315576000000n || nanos < -999999999 || nanos > 999999999 || (seconds > 0n && nanos < 0) || (seconds < 0n && nanos > 0))
        fail("duration out of range");
    const negative = seconds < 0n || nanos < 0;
    return (negative ? "-" : "") + (seconds < 0n ? -seconds : seconds).toString() + fraction(Math.abs(nanos)) + "s";
}

// ---- Writer ----

class Writer {
    buf = new Uint8Array(64);
    pos = 0;
    private view = new DataView(this.buf.buffer);
    private ensure(n: number): void {
        if (this.pos + n <= this.buf.length)
            return;
        let size = this.buf.length * 2;
        while (size < this.pos + n)
            size *= 2;
        const next = new Uint8Array(size);
        next.set(this.buf.subarray(0, this.pos));
        this.buf = next;
        this.view = new DataView(next.buffer);
    }
    byte(b: number): void { this.ensure(1); this.buf[this.pos++] = b; }
    /** Unsigned varint of a non-negative safe integer. */
    uvarint(n: number): void {
        this.ensure(10);
        while (n > 0x7f) {
            this.buf[this.pos++] = (n % 128) | 0x80;
            n = Math.floor(n / 128);
        }
        this.buf[this.pos++] = n;
    }
    /** Varint of a 64-bit two's complement value. */
    varint64(n: bigint): void {
        let v = BigInt.asUintN(64, n);
        this.ensure(10);
        while (v > 0x7fn) {
            this.buf[this.pos++] = Number(v & 0x7fn) | 0x80;
            v >>= 7n;
        }
        this.buf[this.pos++] = Number(v);
    }
    tag(no: number, wire: number): void { this.uvarint(no * 8 + wire); }
    fixed32(n: number): void { this.ensure(4); this.view.setUint32(this.pos, n >>> 0, true); this.pos += 4; }
    float(n: number): void { this.ensure(4); this.view.setFloat32(this.pos, n, true); this.pos += 4; }
    double(n: number): void { this.ensure(8); this.view.setFloat64(this.pos, n, true); this.pos += 8; }
    fixed64(n: bigint): void {
        const v = BigInt.asUintN(64, n);
        this.fixed32(Number(v & 0xffffffffn));
        this.fixed32(Number(v >> 32n));
    }
    bytes(b: Uint8Array): void { this.uvarint(b.length); this.ensure(b.length); this.buf.set(b, this.pos); this.pos += b.length; }
    /** Writes a length prefix for content produced by body(). */
    nested(body: () => void): void {
        const start = this.pos;
        this.ensure(5);
        this.pos += 5; // Reserve a maximal 32-bit length prefix.
        body();
        const length = this.pos - start - 5;
        const prefix: number[] = [];
        let n = length;
        while (n > 0x7f) {
            prefix.push((n & 0x7f) | 0x80);
            n >>>= 7;
        }
        prefix.push(n);
        this.buf.copyWithin(start + prefix.length, start + 5, this.pos);
        this.buf.set(prefix, start);
        this.pos = start + prefix.length + length;
    }
    finish(): Uint8Array { return this.buf.slice(0, this.pos); }
}

// ---- Reader ----

class Reader {
    pos: number;
    readonly buf: Uint8Array;
    readonly end: number;
    private view: DataView;
    constructor(buf: Uint8Array, start: number, end: number) {
        this.buf = buf;
        this.end = end;
        this.pos = start;
        this.view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
    }
    /** Returns [low 32 bits, high 32 bits] of a varint. */
    varint(): [number, number] {
        let lo = 0, hi = 0;
        for (let i = 0; i < 10; i++) {
            if (this.pos >= this.end)
                fail("truncated varint");
            const b = this.buf[this.pos++];
            if (i < 4)
                lo |= (b & 0x7f) << (7 * i);
            else if (i === 4) {
                lo |= (b & 0x0f) << 28;
                hi |= (b & 0x7f) >> 4;
            }
            else
                hi |= (b & 0x7f) << (7 * i - 32);
            if (b < 0x80) {
                if (i === 9 && b > 1)
                    fail("varint overflows 64 bits");
                return [lo >>> 0, hi >>> 0];
            }
        }
        return fail("varint too long");
    }
    uvarint32(): number {
        const [lo, hi] = this.varint();
        if (hi !== 0 || lo > 0x7fffffff)
            fail("length out of range");
        return lo;
    }
    skip(n: number): void {
        if (n < 0 || this.pos + n > this.end)
            fail("truncated field");
        this.pos += n;
    }
    fixed32(): number { this.skip(4); return this.view.getUint32(this.pos - 4, true); }
    float(): number { this.skip(4); return this.view.getFloat32(this.pos - 4, true); }
    double(): number { this.skip(8); return this.view.getFloat64(this.pos - 8, true); }
    fixed64(): [number, number] { const lo = this.fixed32(); return [lo, this.fixed32()]; }
    length(): [number, number] { const n = this.uvarint32(); const start = this.pos; this.skip(n); return [start, start + n]; }
    skipField(wire: number, depth: number): void {
        switch (wire) {
            case VARINT: this.varint(); return;
            case I64: this.skip(8); return;
            case LEN: this.length(); return;
            case I32: this.skip(4); return;
            case 3: { // Group: skip until the matching end tag.
                if (depth > MAX_DEPTH)
                    fail("message too deep");
                for (;;) {
                    const [tag] = this.varint();
                    if ((tag & 7) === 4)
                        return;
                    this.skipField(tag & 7, depth + 1);
                }
            }
            default: fail("invalid wire type");
        }
    }
}

const long = (lo: number, hi: number): bigint => (BigInt(hi) << 32n) | BigInt(lo);

/** Encodes and decodes messages of one generated codecSchema. */
export class ProtobufCodec {
    private fieldsByNumber = new Map<string, Map<number, CodecField>>();
    private enumNumbers = new Map<string, Map<string, number>>();
    private enumNames = new Map<string, Map<number, string>>();
    private schema: CodecSchema;
    constructor(schema: CodecSchema) {
        this.schema = schema;
        for (const [name, values] of Object.entries(schema.enums)) {
            const numbers = new Map<string, number>(), names = new Map<number, string>();
            for (const [value, number] of values) {
                numbers.set(value, number);
                if (!names.has(number))
                    names.set(number, value);
            }
            this.enumNumbers.set(name, numbers);
            this.enumNames.set(name, names);
        }
    }
    private fields(type: string): readonly CodecField[] {
        if (!hasOwn(this.schema.messages, type))
            fail("unknown message type " + type);
        return this.schema.messages[type];
    }
    private byNumber(type: string): Map<number, CodecField> {
        let map = this.fieldsByNumber.get(type);
        if (!map) {
            map = new Map(this.fields(type).map(f => [f.no, f]));
            this.fieldsByNumber.set(type, map);
        }
        return map;
    }

    /** Encodes a canonical ProtoJSON object (as produced by SchemaValidator with normalize). */
    encode(type: string, value: unknown): Uint8Array {
        const w = new Writer();
        this.writeMessage(w, type, value, 0);
        return w.finish();
    }

    private writeMessage(w: Writer, type: string, value: unknown, depth: number): void {
        if (depth > MAX_DEPTH)
            fail("message too deep");
        value = this.fromSpecialJSON(type, value);
        if (!value || typeof value !== "object" || Array.isArray(value))
            fail(type + ": expected an object");
        const object = value as Record<string, unknown>;
        for (const field of this.fields(type)) {
            if (!hasOwn(object, field.name))
                continue;
            const item = object[field.name];
            if (item === undefined || item === null)
                continue;
            if (field.mapKey) {
                const entries = item as Record<string, unknown>;
                for (const key of Object.keys(entries)) {
                    w.tag(field.no, LEN);
                    w.nested(() => {
                        this.writeScalar(w, 1, field.mapKey!, undefined, this.mapKey(field.mapKey!, key), depth + 1);
                        this.writeScalar(w, 2, field.kind, field.type, entries[key], depth + 1);
                    });
                }
            }
            else if (field.list) {
                const items = item as unknown[];
                if (!items.length)
                    continue;
                if (field.packed && PACKABLE.has(field.kind)) {
                    w.tag(field.no, LEN);
                    w.nested(() => { for (const element of items) this.writeValue(w, field.kind, field.type, element, depth + 1); });
                }
                else
                    for (const element of items)
                        this.writeScalar(w, field.no, field.kind, field.type, element, depth + 1);
            }
            else if (field.presence || field.kind === "message" || !this.isDefault(field, item))
                this.writeScalar(w, field.no, field.kind, field.type, item, depth + 1);
        }
    }

    private mapKey(kind: string, key: string): unknown {
        if (kind === "bool")
            return key === "true";
        if (kind === "string" || LONG_KINDS.has(kind))
            return key;
        return Number(key);
    }

    private isDefault(field: CodecField, value: unknown): boolean {
        switch (field.kind) {
            case "string": case "bytes": return value === "";
            case "bool": return value === false;
            case "enum": return value === 0 || this.enumNumbers.get(field.type!)?.get(value as string) === 0;
            default: return value === 0 || value === "0";
        }
    }

    private writeScalar(w: Writer, no: number, kind: string, type: string | undefined, value: unknown, depth: number): void {
        w.tag(no, wireType(kind));
        this.writeValue(w, kind, type, value, depth);
    }

    private writeValue(w: Writer, kind: string, type: string | undefined, value: unknown, depth: number): void {
        switch (kind) {
            case "message": w.nested(() => this.writeMessage(w, type!, value, depth)); return;
            case "string": w.bytes(encodeUTF8(value as string)); return;
            case "bytes": w.bytes(decodeBase64(value as string)); return;
            case "bool": w.byte(value ? 1 : 0); return;
            case "enum": {
                const number = typeof value === "number" ? value : this.enumNumbers.get(type!)?.get(value as string);
                if (number === undefined)
                    fail("unknown enum value " + String(value));
                w.varint64(BigInt(number as number));
                return;
            }
            case "int32": w.varint64(BigInt(value as number)); return;
            case "uint32": w.uvarint(value as number); return;
            case "sint32": { const n = value as number; w.uvarint(((n << 1) ^ (n >> 31)) >>> 0); return; }
            case "int64": case "uint64": w.varint64(BigInt(value as string)); return;
            case "sint64": { const n = BigInt(value as string); w.varint64((n << 1n) ^ (n >> 63n)); return; }
            case "fixed32": case "sfixed32": w.fixed32(value as number); return;
            case "fixed64": case "sfixed64": w.fixed64(BigInt(value as string)); return;
            case "float": w.float(special(value)); return;
            case "double": w.double(special(value)); return;
        }
        fail("unsupported kind " + kind);
    }

    // Well-known types: JSON form -> plain message object.
    private fromSpecialJSON(type: string, value: unknown): unknown {
        if (!type.startsWith("google.protobuf."))
            return value;
        if (type === "google.protobuf.Timestamp")
            return parseTimestamp(value as string);
        if (type === "google.protobuf.Duration")
            return parseDuration(value as string);
        if (hasOwn(WRAPPERS, type))
            return { value };
        return value;
    }

    /** Decodes binary Protobuf into the object Go protojson.Marshal would produce. */
    decode(type: string, bytes: Uint8Array): Record<string, unknown> {
        return this.readMessage(type, new Reader(bytes, 0, bytes.length), 0, {}) as Record<string, unknown>;
    }

    private readMessage(type: string, r: Reader, depth: number, into: Record<string, unknown>): unknown {
        if (depth > MAX_DEPTH)
            fail("message too deep");
        const fields = this.byNumber(type);
        while (r.pos < r.end) {
            const [tag, tagHigh] = r.varint();
            const no = tag >>> 3, wire = tag & 7;
            if (tagHigh !== 0 || no === 0)
                fail("invalid field number");
            const field = fields.get(no);
            if (!field) {
                r.skipField(wire, depth);
                continue;
            }
            if (field.mapKey) {
                if (wire !== LEN)
                    fail(type + "." + field.name + ": wrong wire type");
                const [start, end] = r.length();
                const entry = new Reader(r.buf, start, end);
                let key: unknown, value: unknown;
                while (entry.pos < entry.end) {
                    const [entryTag] = entry.varint();
                    const entryNo = entryTag >>> 3, entryWire = entryTag & 7;
                    if (entryNo === 1)
                        key = this.readValue(entry, entryWire, field.mapKey, undefined, depth + 1);
                    else if (entryNo === 2)
                        value = this.readValue(entry, entryWire, field.kind, field.type, depth + 1);
                    else
                        entry.skipField(entryWire, depth);
                }
                const map = (hasOwn(into, field.name) ? into[field.name] : (into[field.name] = {})) as Record<string, unknown>;
                const keyText = key === undefined ? (field.mapKey === "bool" ? "false" : field.mapKey === "string" ? "" : "0") : String(key);
                Object.defineProperty(map, keyText, { value: value === undefined ? this.defaultValue(field) : value, enumerable: true, writable: true, configurable: true });
                continue;
            }
            if (field.list) {
                const list = (hasOwn(into, field.name) ? into[field.name] : (into[field.name] = [])) as unknown[];
                if (wire === LEN && PACKABLE.has(field.kind)) {
                    const [start, end] = r.length();
                    const packed = new Reader(r.buf, start, end);
                    while (packed.pos < packed.end)
                        list.push(this.readValue(packed, wireType(field.kind), field.kind, field.type, depth + 1));
                }
                else
                    list.push(this.readValue(r, wire, field.kind, field.type, depth + 1));
                continue;
            }
            if (field.oneof)
                for (const other of fields.values())
                    if (other.oneof === field.oneof && other !== field)
                        delete into[other.name];
            if (field.kind === "message" && wire === LEN && hasOwn(into, field.name) && !this.isSpecial(field.type!)) {
                const [start, end] = r.length(); // A repeated singular message merges.
                this.readMessage(field.type!, new Reader(r.buf, start, end), depth + 1, into[field.name] as Record<string, unknown>);
                continue;
            }
            into[field.name] = this.readValue(r, wire, field.kind, field.type, depth + 1);
        }
        for (const field of fields.values())
            if (hasOwn(into, field.name) && !field.list && !field.mapKey && !field.presence && field.kind !== "message" && this.isDefault(field, into[field.name]))
                delete into[field.name];
        return into;
    }

    private isSpecial(type: string): boolean {
        return type === "google.protobuf.Timestamp" || type === "google.protobuf.Duration" || hasOwn(WRAPPERS, type) || type === "google.protobuf.Empty";
    }

    private defaultValue(field: CodecField): unknown {
        switch (field.kind) {
            case "message": return this.toSpecialJSON(field.type!, {});
            case "string": case "bytes": return "";
            case "bool": return false;
            case "enum": return this.enumNames.get(field.type!)?.get(0) ?? 0;
            default: return LONG_KINDS.has(field.kind) ? "0" : 0;
        }
    }

    private readValue(r: Reader, wire: number, kind: string, type: string | undefined, depth: number): unknown {
        if (wire !== wireType(kind))
            fail(`wrong wire type ${wire} for ${kind}`);
        switch (kind) {
            case "message": {
                const [start, end] = r.length();
                return this.toSpecialJSON(type!, this.readMessage(type!, new Reader(r.buf, start, end), depth, {}) as Record<string, unknown>);
            }
            case "string": { const [start, end] = r.length(); return decodeUTF8(r.buf, start, end); }
            case "bytes": { const [start, end] = r.length(); return encodeBase64(r.buf.subarray(start, end)); }
            case "bool": { const [lo, hi] = r.varint(); return lo !== 0 || hi !== 0; }
            case "enum": {
                const [lo] = r.varint();
                return this.enumNames.get(type!)?.get(lo | 0) ?? (lo | 0);
            }
            case "int32": return r.varint()[0] | 0;
            case "uint32": return r.varint()[0];
            case "sint32": { const lo = r.varint()[0]; return (lo >>> 1) ^ -(lo & 1); }
            case "int64": { const [lo, hi] = r.varint(); return BigInt.asIntN(64, long(lo, hi)).toString(); }
            case "uint64": { const [lo, hi] = r.varint(); return long(lo, hi).toString(); }
            case "sint64": { const [lo, hi] = r.varint(); const n = long(lo, hi); return ((n >> 1n) ^ -(n & 1n)).toString(); }
            case "fixed32": return r.fixed32();
            case "sfixed32": return r.fixed32() | 0;
            case "fixed64": { const [lo, hi] = r.fixed64(); return long(lo, hi).toString(); }
            case "sfixed64": { const [lo, hi] = r.fixed64(); return BigInt.asIntN(64, long(lo, hi)).toString(); }
            case "float": return finite(r.float());
            case "double": return finite(r.double());
        }
        return fail("unsupported kind " + kind);
    }

    // Well-known types: plain message object -> JSON form.
    private toSpecialJSON(type: string, value: Record<string, unknown>): unknown {
        if (!type.startsWith("google.protobuf."))
            return value;
        if (type === "google.protobuf.Timestamp")
            return formatTimestamp(BigInt((value.seconds as string) ?? "0"), (value.nanos as number) ?? 0);
        if (type === "google.protobuf.Duration")
            return formatDuration(BigInt((value.seconds as string) ?? "0"), (value.nanos as number) ?? 0);
        if (hasOwn(WRAPPERS, type))
            return hasOwn(value, "value") ? value.value : this.defaultValue({ no: 1, name: "value", kind: WRAPPERS[type] });
        return value;
    }
}

function special(value: unknown): number {
    if (value === "NaN")
        return NaN;
    if (value === "Infinity")
        return Infinity;
    if (value === "-Infinity")
        return -Infinity;
    return value as number;
}
function finite(n: number): number | string {
    if (Number.isNaN(n))
        return "NaN";
    if (n === Infinity)
        return "Infinity";
    if (n === -Infinity)
        return "-Infinity";
    return n;
}
