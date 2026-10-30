/** Validation of the ProtoJSON wire shape, following Go protojson's input rules. */
import { JSON_ROOT, PROTOCOL_LIMITS } from "./json.ts";
import type { JSONLimits, NumberSources } from "./json.ts";

export interface FieldShape {
    kind: string;
    type?: string;
    list?: boolean;
    mapKey?: string;
    required?: boolean;
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
        const present = (name: string): boolean => hasOwn(fields, name) && (fields[name] !== null || !hasOwn(shape.fields, name) || nullable(shape.fields[name]));
        for (const group of shape.oneofs ?? []) {
            if (group.filter(present).length > 1)
                this.fail(path, "at most one oneof member");
        }
        for (const [name, field] of Object.entries(shape.fields))
            if (field.required && !present(name))
                this.fail(path + "." + name, "required field");
        for (const name of Object.keys(fields)) {
            if (skipType && name === "@type")
                continue;
            const next = path + "." + name;
            if (!hasOwn(shape.fields, name))
                this.fail(next, "declared field");
            const field = shape.fields[name], item = fields[name];
            if (item === null && !nullable(field)) {
                if (this.normalize)
                    delete fields[name];
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
                    fields[name] = rebuilt;
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
                const result = this.scalar(field, item, next, depth + 1, false, fields, name);
                if (this.normalize && result !== item)
                    fields[name] = result;
            }
        }
        return fields;
    }
    // Returns undefined for types without a special JSON form.
    private wellKnown(type: string, value: unknown, path: string, depth: number, holder: object, key: string): { value: unknown } | undefined {
        const prefix = "google.protobuf.";
        const name = type.slice(prefix.length);
        if (hasOwn(protobufWrappers, name))
            return { value: this.scalar({ kind: protobufWrappers[name] }, value, path, depth, true, holder, key) };
        if (name === "Timestamp") {
            if (typeof value !== "string" || !fullMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.(?:\d{3}|\d{6}|\d{9}))?Z$/, value) || Number(value.slice(0, 4)) === 0)
                this.fail(path, "canonical UTC timestamp");
            const date = new Date(value);
            if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== value.slice(0, 19))
                this.fail(path, "valid UTC timestamp");
            return { value };
        }
        if (name === "Duration") {
            if (typeof value !== "string" || !fullMatch(/^-?(0|[1-9]\d*)(?:\.(?:\d{3}|\d{6}|\d{9}))?s$/, value) || Math.abs(Number(value.slice(0, -1))) > 315576000001)
                this.fail(path, "duration in range");
            if (Math.abs(Number(value.slice(0, -1).split(".")[0])) > 315576000000)
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
            if (nestedType === type || !hasOwn(this.schema.messages, nestedType))
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
            const number = typeof value === "number" ? value : typeof value === "string" && JSON_NUMBER.test(value) ? Number(value) : NaN;
            // Go emits float32 max as 3.4028235e+38; accept values that round to a finite float32.
            if (!Number.isFinite(number) || (kind === "float" && !Number.isFinite(Math.fround(number))))
                this.fail(path, kind);
            return number;
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
