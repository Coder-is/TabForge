/** Validation of the ProtoJSON wire shape, following Go protojson's input rules. */
import { JSON_ROOT, PROTOCOL_LIMITS } from "./json.ts";
import type { JSONLimits, NumberSources } from "./json.ts";

export interface FieldShape {
    kind: string;
    type?: string;
    list?: boolean;
    mapKey?: string;
    required?: boolean;
    /** Original Proto field name when it differs from the JSON name (the field key). */
    protoName?: string;
}
export interface MessageShape {
    fields: Readonly<Record<string, FieldShape>>;
    oneofs?: readonly (readonly string[])[];
}
export interface WireSchema {
    messages: Readonly<Record<string, MessageShape>>;
    enums: Readonly<Record<string, readonly string[]>>;
}
export interface ValidatorOptions {
    /** Defaults to PROTOCOL_LIMITS; data loaders use DATA_LIMITS. */
    limits?: JSONLimits;
    /** Rewrite accepted values to canonical ProtoJSON in place (data loaders). */
    normalize?: boolean;
}
const hasOwn = (value: object, key: PropertyKey): boolean => Object.prototype.hasOwnProperty.call(value, key);
const fullMatch = (pattern: RegExp, value: string): boolean => pattern.exec(value)?.[0] === value;
const setOwn = (target: Record<string, unknown>, key: string, value: unknown): void => {
    Object.defineProperty(target, key, { value, writable: true, enumerable: true, configurable: true });
};
const integerKinds: Readonly<Record<string, readonly [boolean, 32 | 64]>> = {
    int32: [true, 32], sint32: [true, 32], sfixed32: [true, 32], uint32: [false, 32], fixed32: [false, 32],
    int64: [true, 64], sint64: [true, 64], sfixed64: [true, 64], uint64: [false, 64], fixed64: [false, 64]
};
const JSON_NUMBER = /^-?(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/;

function inRange(digits: string, negative: boolean, signed: boolean, bits: 32 | 64): boolean {
    if (negative && !signed)
        return false;
    const maximum = bits === 32 ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295") : (signed ? (negative ? "9223372036854775808" : "9223372036854775807") : "18446744073709551615");
    return digits.length < maximum.length || (digits.length === maximum.length && digits <= maximum);
}
/** Canonical decimal string; 64-bit integers are emitted this way. */
export function validIntegerString(value: unknown, signed: boolean, bits: 32 | 64 = 64): boolean {
    return typeof value === "string" && value !== "-0" && fullMatch(/-?(?:0|[1-9][0-9]*)/, value) && inRange(value.replace(/^-/, ""), value.startsWith("-"), signed, bits);
}
/**
 * Go protojson integer input: JSON number text, exact after shifting the
 * exponent (1.0, 1e2 and 100e-2 are integers; 1.5 and 1e-2 are not). Mirrors
 * Go's normalizeToIntString, including its 20-digit cap for positive exponents.
 * Returns the canonical decimal, or undefined.
 */
export function protoInteger(text: string, signed: boolean, bits: 32 | 64): string | undefined {
    const match = JSON_NUMBER.exec(text);
    if (!match)
        return undefined;
    const intp = match[1] === "0" ? "" : match[1], frac = (match[2] ?? "").replace(/0+$/, "");
    if (intp === "" && frac === "")
        return "0";
    let exponent = 0;
    if (match[3] !== undefined) {
        exponent = Number(match[3]);
        if (!(exponent >= -2147483648 && exponent <= 2147483647))
            return undefined;
    }
    let digits: string;
    if (exponent >= 0) {
        if (frac.length > exponent || intp.length + exponent > 20)
            return undefined;
        digits = intp + frac + "0".repeat(exponent - frac.length);
    }
    else {
        const index = intp.length + exponent;
        if (frac.length > 0 || index < 0 || /[^0]/.test(intp.slice(index)))
            return undefined;
        digits = intp.slice(0, index);
    }
    digits = digits.replace(/^0+(?=[0-9])/, "");
    const negative = text.startsWith("-");
    return inRange(digits, negative, signed, bits) ? (negative ? "-" : "") + digits : undefined;
}
/** Go strconv.ParseInt/ParseUint(key, 10, bits) for integer map keys. */
export function protoMapKey(key: string, kind: string): string | undefined {
    if (kind === "string")
        return key;
    if (kind === "bool")
        return key === "true" || key === "false" ? key : undefined;
    const shape = integerKinds[kind];
    const match = shape && /^([+-]?)([0-9]+)$/.exec(key);
    if (!match || (match[1] && !shape[0]))
        return undefined;
    const digits = match[2].replace(/^0+(?=[0-9])/, ""), negative = match[1] === "-" && digits !== "0";
    return inRange(digits, negative, shape[0], shape[1]) ? (negative ? "-" : "") + digits : undefined;
}
const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
/**
 * Go protojson bytes input: standard or URL alphabet (URL when "-" or "_"
 * occurs), padded exactly when the length is a multiple of 4; CR/LF ignored;
 * unused trailing bits ignored. Returns canonical padded standard Base64.
 */
export function protoBytes(value: string): string | undefined {
    const url = /[-_]/.test(value), text = value.replace(/[\r\n]/g, "");
    const pattern = value.length % 4 === 0
        ? (url ? /^(?:[A-Za-z0-9_-]{4})*(?:[A-Za-z0-9_-]{2}==|[A-Za-z0-9_-]{3}=)?$/ : /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/)
        : (url ? /^(?:[A-Za-z0-9_-]{4})*(?:[A-Za-z0-9_-]{2,3})?$/ : /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2,3})?$/);
    if (!pattern.test(text))
        return undefined;
    let body = text.replace(/=+$/, "").replace(/-/g, "+").replace(/_/g, "/");
    const rest = body.length % 4;
    if (rest !== 0)
        body = body.slice(0, -1) + BASE64[BASE64.indexOf(body[body.length - 1]) & (rest === 2 ? 0x30 : 0x3c)] + (rest === 2 ? "==" : "=");
    return body;
}
// Days from 1970-01-01 to a proleptic Gregorian date (year 0 allowed: offsets
// can move "0000-12-31T23:59:59-00:01" into range). Shifted by 400 years so
// every division is of a non-negative number.
function civilDays(year: number, month: number, day: number): number {
    const y = year + 400 - (month <= 2 ? 1 : 0), era = Math.floor(y / 400), yoe = y - era * 400;
    const doy = Math.floor((153 * ((month + 9) % 12) + 2) / 5) + day - 1;
    return era * 146097 + yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy - 719468 - 146097;
}
const daysInMonth = (year: number, month: number): number => month === 2 ? (year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28) : [31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
const TIMESTAMP = /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:([.,])([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$/;
/**
 * Go protojson Timestamp input: time.Parse(time.RFC3339Nano), then 0001-01-01
 * to 9999-12-31 in UTC and at most 9 digits after ".". Like Go this accepts any
 * offset (hours to 24, minutes to 60), a one-digit hour, and "," before a
 * fraction of any length (Go truncates it); "z", "t" and leap seconds are rejected.
 */
export function protoTimestamp(value: unknown): boolean {
    const match = typeof value === "string" ? TIMESTAMP.exec(value) : null;
    if (!match)
        return false;
    const [year, month, day, hour, minute, second] = match.slice(1, 7).map(Number);
    if ((match[7] === "." && match[8].length > 9) || month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) || hour > 23 || minute > 59 || second > 59)
        return false;
    let offset = 0;
    if (match[9] !== undefined) {
        const hours = Number(match[10]), minutes = Number(match[11]);
        if (hours > 24 || minutes > 60)
            return false;
        offset = (match[9] === "-" ? -1 : 1) * (hours * 3600 + minutes * 60);
    }
    const seconds = civilDays(year, month, day) * 86400 + hour * 3600 + minute * 60 + second - offset;
    return seconds >= -62135596800 && seconds <= 253402300799;
}
const DURATION = /^[+-]?(0|[1-9][0-9]*)?(\.[0-9]{0,9})?s$/;
/**
 * Go protojson Duration input: optional sign, integer and/or "." with up to 9
 * digits ("1.s", ".5s", "-.5s" and ".s" are accepted), then "s", within
 * ±315576000000 seconds.
 */
export function protoDuration(value: unknown): boolean {
    const match = typeof value === "string" ? DURATION.exec(value) : null;
    if (!match || (match[1] === undefined && match[2] === undefined))
        return false;
    const seconds = match[1] ?? "";
    return seconds.length < 12 || (seconds.length === 12 && seconds <= "315576000000");
}
// 2^128 - 2^104 is the largest float32; decimals from 2^128 - 2^103 up overflow it.
const FLOAT32_MAX = 3.4028234663852886e38, FLOAT32_LIMIT = 3.4028235677973366e38;
/** Whether JSON number text is below 2^128 - 2^103 in magnitude, compared exactly. */
export function belowFloat32Limit(text: string): boolean {
    const match = JSON_NUMBER.exec(text);
    if (!match)
        return false;
    // The value is 0.<digits> * 10^exponent.
    let digits = match[1] + (match[2] ?? ""), exponent = match[1].length + Number(match[3] ?? "0");
    const lead = digits.length - digits.replace(/^0+/, "").length;
    digits = digits.slice(lead).replace(/0+$/, "");
    exponent -= lead;
    const limit = "340282356779733661637539395458142568448";
    return digits === "" || exponent < limit.length || (exponent === limit.length && digits < limit);
}
const protobufWrappers: Readonly<Record<string, string>> = {
    DoubleValue: "double", FloatValue: "float", Int64Value: "int64", UInt64Value: "uint64",
    Int32Value: "int32", UInt32Value: "uint32", BoolValue: "bool", StringValue: "string", BytesValue: "bytes"
};
// JSON null means "field absent" except where null is itself a value.
const nullable = (field: FieldShape): boolean => (field.kind === "message" && field.type === "google.protobuf.Value") || (field.kind === "enum" && field.type === "google.protobuf.NullValue");

/**
 * Validates one message tree. Depth equals the JSON nesting depth of each value
 * and every JSON value counts at most once, so for parsed text the parser's
 * budget is always the binding one.
 */
export class SchemaValidator {
    private schema: WireSchema;
    private limits: JSONLimits;
    private normalize: boolean;
    private nodes = 0;
    private sources?: NumberSources;
    constructor(schema: WireSchema, options: ValidatorOptions = {}) {
        this.schema = schema;
        this.limits = options.limits ?? PROTOCOL_LIMITS;
        this.normalize = options.normalize ?? false;
    }
    /** Returns the value, rewritten in place to canonical ProtoJSON when normalizing. */
    validate(type: string, value: unknown, sources?: NumberSources): unknown {
        this.nodes = 0;
        this.sources = sources;
        try {
            const result = this.message(type, value, "$", 0, false, JSON_ROOT, "");
            return this.normalize ? result : value;
        }
        finally {
            this.sources = undefined;
        }
    }
    private fail(path: string, expected: string): never { throw new Error(`Invalid ProtoJSON at ${path}: expected ${expected}`); }
    private budget(path: string, depth: number): void {
        if (depth > this.limits.maxDepth || ++this.nodes > this.limits.maxNodes)
            this.fail(path, "bounded message depth/size");
    }
    private record(value: unknown, path: string): Record<string, unknown> {
        if (!value || typeof value !== "object" || Array.isArray(value))
            this.fail(path, "object");
        return value as Record<string, unknown>;
    }
    private numberText(value: unknown, holder: object, key: string): string | undefined {
        if (typeof value === "string")
            return value;
        if (typeof value !== "number" || !Number.isFinite(value))
            return undefined;
        return this.sources?.get(holder)?.get(key) ?? String(value);
    }
    private message(type: string, value: unknown, path: string, depth: number, counted: boolean, holder: object, key: string, skipType = false): unknown {
        if (!counted)
            this.budget(path, depth);
        if (type.startsWith("google.protobuf.")) {
            const known = this.wellKnown(type, value, path, depth, holder, key);
            if (known !== undefined)
                return known.value;
        }
        if (!hasOwn(this.schema.messages, type))
            this.fail(path, "known message type");
        const shape = this.schema.messages[type];
        const fields = this.record(value, path);
        // A member is spelled as the JSON name or the original Proto name; like Go
        // protojson, a field given twice (also once in each spelling) is rejected.
        const members: [string, string][] = [], spelled = new Map<string, string>();
        for (const member of Object.keys(fields)) {
            if (skipType && member === "@type")
                continue;
            const name = this.fieldName(shape, member);
            if (name === undefined)
                this.fail(path + "." + member, "declared field");
            if (spelled.has(name))
                this.fail(path + "." + member, "field given once");
            spelled.set(name, member);
            members.push([member, name]);
        }
        const present = (name: string): boolean => {
            const member = spelled.get(name);
            return member !== undefined && (fields[member] !== null || nullable(shape.fields[name]));
        };
        for (const group of shape.oneofs ?? []) {
            if (group.filter(present).length > 1)
                this.fail(path, "at most one oneof member");
        }
        for (const [name, field] of Object.entries(shape.fields))
            if (field.required && !present(name))
                this.fail(path + "." + name, "required field");
        for (const [member, name] of members) {
            const next = path + "." + member;
            const field = shape.fields[name], item = fields[member];
            if (item === null && !nullable(field)) {
                if (this.normalize)
                    delete fields[member];
                continue;
            }
            if (field.mapKey) {
                const entries = this.record(item, next);
                this.budget(next, depth + 1);
                const keys = new Map<string, string>(), seen = new Set<string>();
                for (const entryKey of Object.keys(entries)) {
                    const canonical = protoMapKey(entryKey, field.mapKey);
                    if (canonical === undefined)
                        this.fail(next, field.mapKey + " map key");
                    if (seen.has(canonical))
                        this.fail(next, "unique map key");
                    seen.add(canonical);
                    keys.set(entryKey, canonical);
                    const result = this.scalar(field, entries[entryKey], next + "." + entryKey, depth + 2, false, entries, entryKey);
                    if (this.normalize && result !== entries[entryKey])
                        setOwn(entries, entryKey, result);
                }
                if (this.normalize && [...keys].some(([original, canonical]) => original !== canonical)) {
                    const rebuilt: Record<string, unknown> = {};
                    for (const [original, canonical] of keys)
                        setOwn(rebuilt, canonical, entries[original]);
                    fields[member] = rebuilt;
                }
            }
            else if (field.list) {
                if (!Array.isArray(item))
                    this.fail(next, "array");
                this.budget(next, depth + 1);
                for (let i = 0; i < item.length; i++) {
                    const result = this.scalar(field, item[i], `${next}[${i}]`, depth + 2, false, item, String(i));
                    if (this.normalize)
                        item[i] = result;
                }
            }
            else {
                const result = this.scalar(field, item, next, depth + 1, false, fields, member);
                if (this.normalize && result !== item)
                    fields[member] = result;
            }
            if (this.normalize && member !== name) {
                setOwn(fields, name, fields[member]);
                delete fields[member];
            }
        }
        return fields;
    }
    private aliases = new WeakMap<MessageShape, Map<string, string>>();
    // JSON name of a member spelled as the JSON name or the original Proto name.
    private fieldName(shape: MessageShape, member: string): string | undefined {
        if (hasOwn(shape.fields, member))
            return member;
        let names = this.aliases.get(shape);
        if (!names) {
            names = new Map();
            for (const [name, field] of Object.entries(shape.fields))
                if (typeof field.protoName === "string" && !hasOwn(shape.fields, field.protoName))
                    names.set(field.protoName, name);
            this.aliases.set(shape, names);
        }
        return names.get(member);
    }
    // Returns undefined for types without a special JSON form.
    private wellKnown(type: string, value: unknown, path: string, depth: number, holder: object, key: string): { value: unknown } | undefined {
        const prefix = "google.protobuf.";
        const name = type.slice(prefix.length);
        if (hasOwn(protobufWrappers, name))
            return { value: this.scalar({ kind: protobufWrappers[name] }, value, path, depth, true, holder, key) };
        if (name === "Timestamp") {
            if (!protoTimestamp(value))
                this.fail(path, "RFC 3339 timestamp in range");
            return { value };
        }
        if (name === "Duration") {
            if (!protoDuration(value))
                this.fail(path, "duration in range");
            return { value };
        }
        if (name === "FieldMask") {
            if (typeof value !== "string" || !fullMatch(/^(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?$/, value))
                this.fail(path, "canonical field mask string");
            return { value };
        }
        if (name === "Value") {
            this.json(value, path, depth, true);
            return { value };
        }
        if (name === "Struct") {
            const fields = this.record(value, path);
            for (const field of Object.keys(fields))
                this.json(fields[field], path + "." + field, depth + 1, false);
            return { value };
        }
        if (name === "ListValue") {
            if (!Array.isArray(value))
                this.fail(path, "array");
            for (const element of value)
                this.json(element, path, depth + 1, false);
            return { value };
        }
        if (name === "Any") {
            const fields = this.record(value, path);
            // protojson writes an unset Any as {} and Empty as {"@type": ..., "value": {}}.
            if (Object.keys(fields).length === 0)
                return { value };
            if (typeof fields["@type"] !== "string")
                this.fail(path, "Any @type");
            const nestedType = (fields["@type"] as string).split("/").slice(-1)[0];
            // An Any may hold another Any; the depth budget bounds the recursion.
            if (!hasOwn(this.schema.messages, nestedType))
                this.fail(path, "known Any type");
            const members = Object.keys(fields).filter(member => member !== "@type");
            if (nestedType === prefix + "Empty" && members.length === 0)
                return { value }; // Go tolerates an omitted Empty value on input.
            if (nestedType.startsWith(prefix)) {
                if (members.length !== 1 || members[0] !== "value")
                    this.fail(path, "Any value");
                const result = this.message(nestedType, fields.value, path + ".value", depth + 1, false, fields, "value");
                if (this.normalize)
                    fields.value = result;
            }
            else
                this.message(nestedType, fields, path, depth, true, holder, key, true);
            return { value };
        }
        return undefined;
    }
    private scalar(field: FieldShape, value: unknown, path: string, depth: number, counted: boolean, holder: object, key: string): unknown {
        const kind = field.kind;
        if (kind === "message" || kind === "group")
            return this.message(field.type!, value, path, depth, counted, holder, key);
        if (!counted)
            this.budget(path, depth);
        if (kind === "enum") {
            if (field.type === "google.protobuf.NullValue" && value === null)
                return value;
            if (typeof value === "string" && this.schema.enums[field.type!]?.includes(value))
                return value;
            const number = typeof value === "number" ? protoInteger(this.numberText(value, holder, key) ?? "", true, 32) : undefined;
            if (number === undefined)
                this.fail(path, "enum name or int32");
            return Number(number);
        }
        if (hasOwn(integerKinds, kind)) {
            const [signed, bits] = integerKinds[kind];
            const text = this.numberText(value, holder, key);
            const integer = text === undefined ? undefined : protoInteger(text, signed, bits);
            if (integer === undefined)
                this.fail(path, kind);
            return bits === 64 ? integer : Number(integer);
        }
        if (kind === "double" || kind === "float") {
            if (value === "NaN" || value === "Infinity" || value === "-Infinity")
                return value;
            const text = typeof value === "string" ? (JSON_NUMBER.test(value) ? value : undefined) : this.numberText(value, holder, key);
            const number = text === undefined ? NaN : Number(text);
            if (!Number.isFinite(number))
                this.fail(path, kind);
            if (kind !== "float")
                return number;
            // Go rounds the decimal text to float32 once (3.40282356e+38 becomes
            // 3.4028235e+38, 1e-50 becomes 0). Rounding through a double differs
            // only for text that rounds to the float32 overflow threshold itself.
            if (Math.abs(number) === FLOAT32_LIMIT) {
                if (!belowFloat32Limit(text!))
                    this.fail(path, kind);
                return Math.sign(number) * FLOAT32_MAX;
            }
            const rounded = Math.fround(number);
            if (!Number.isFinite(rounded))
                this.fail(path, kind);
            return rounded;
        }
        if (kind === "bool") {
            if (typeof value !== "boolean")
                this.fail(path, "boolean");
            return value;
        }
        if (kind === "string") {
            if (typeof value !== "string")
                this.fail(path, "string");
            return value;
        }
        if (kind === "bytes") {
            const bytes = typeof value === "string" ? protoBytes(value) : undefined;
            if (bytes === undefined)
                this.fail(path, "Base64 string");
            return bytes;
        }
        return this.fail(path, "supported scalar type");
    }
    private json(value: unknown, path: string, depth: number, counted: boolean): void {
        if (!counted)
            this.budget(path, depth);
        if (value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value)))
            return;
        if (Array.isArray(value)) {
            for (const item of value)
                this.json(item, path, depth + 1, false);
            return;
        }
        const fields = this.record(value, path);
        for (const name of Object.keys(fields))
            this.json(fields[name], path + "." + name, depth + 1, false);
    }
}
