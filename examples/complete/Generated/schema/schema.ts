/** Validation of the emitted ProtoJSON wire shape, including decimal int64s. */
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
const hasOwn = (value: object, key: PropertyKey): boolean => Object.prototype.hasOwnProperty.call(value, key);
const fullMatch = (pattern: RegExp, value: string): boolean => pattern.exec(value)?.[0] === value;
const signed64 = new Set(["int64", "sint64", "sfixed64"]);
const unsigned64 = new Set(["uint64", "fixed64"]);
const signed32 = new Set(["int32", "sint32", "sfixed32"]);
const unsigned32 = new Set(["uint32", "fixed32"]);
export function validIntegerString(value: unknown, signed: boolean, bits = 64): boolean {
    if (typeof value !== "string" || value === "-0")
        return false;
    const match = /^-?(0|[1-9][0-9]*)$/.exec(value);
    if (!match || match[0] !== value)
        return false;
    const negative = value.startsWith("-");
    if (negative && !signed)
        return false;
    const digits = negative ? value.slice(1) : value;
    const maximum = bits === 32 ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295") : (signed ? (negative ? "9223372036854775808" : "9223372036854775807") : "18446744073709551615");
    return digits.length < maximum.length || (digits.length === maximum.length && digits <= maximum);
}
const protobufWrappers: Readonly<Record<string, string>> = {
    DoubleValue: "double", FloatValue: "float", Int64Value: "int64", UInt64Value: "uint64",
    Int32Value: "int32", UInt32Value: "uint32", BoolValue: "bool", StringValue: "string", BytesValue: "bytes"
};
export class SchemaValidator {
    private schema: WireSchema;
    private nodes = 0;
    constructor(schema: WireSchema) { this.schema = schema; }
    validate(type: string, value: unknown): void { this.nodes = 0; this.message(type, value, "$", 0); }
    private fail(path: string, expected: string): never { throw new Error(`Invalid ProtoJSON at ${path}: expected ${expected}`); }
    private budget(path: string, depth: number): void {
        if (depth > 64 || ++this.nodes > 100000)
            this.fail(path, "bounded message depth/size");
    }
    private record(value: unknown, path: string): Record<string, unknown> {
        if (!value || typeof value !== "object" || Array.isArray(value))
            this.fail(path, "object");
        return value as Record<string, unknown>;
    }
    private message(type: string, value: unknown, path: string, depth: number): void {
        this.budget(path, depth);
        if (type.startsWith("google.protobuf.") && this.wellKnown(type, value, path, depth))
            return;
        if (!hasOwn(this.schema.messages, type))
            this.fail(path, "known message type");
        const shape = this.schema.messages[type];
        const fields = this.record(value, path);
        for (const group of shape.oneofs ?? []) {
            if (group.filter(name => hasOwn(fields, name)).length > 1)
                this.fail(path, "at most one oneof member");
        }
        for (const [name, field] of Object.entries(shape.fields))
            if (field.required && !hasOwn(fields, name))
                this.fail(path + "." + name, "required field");
        for (const name of Object.keys(fields)) {
            if (!hasOwn(shape.fields, name))
                this.fail(path + "." + name, "declared field");
            const field = shape.fields[name], item = fields[name], next = path + "." + name;
            if (field.mapKey) {
                const entries = this.record(item, next);
                for (const key of Object.keys(entries)) {
                    if (field.mapKey !== "string") {
                        if (field.mapKey === "bool") {
                            if (key !== "true" && key !== "false")
                                this.fail(next, "boolean map key");
                        }
                        else if (!validIntegerString(key, field.mapKey.startsWith("s") || field.mapKey.startsWith("int"), field.mapKey.endsWith("32") ? 32 : 64))
                            this.fail(next, "integer map key");
                    }
                    this.scalar(field, entries[key], next + "." + key, depth + 1);
                }
            }
            else if (field.list) {
                if (!Array.isArray(item))
                    this.fail(next, "array");
                for (let i = 0; i < item.length; i++)
                    this.scalar(field, item[i], `${next}[${i}]`, depth + 1);
            }
            else
                this.scalar(field, item, next, depth + 1);
        }
    }
    private wellKnown(type: string, value: unknown, path: string, depth: number): boolean {
        const prefix = "google.protobuf.";
        const name = type.slice(prefix.length);
        if (hasOwn(protobufWrappers, name)) {
            this.scalar({ kind: protobufWrappers[name] }, value, path, depth + 1);
            return true;
        }
        if (name === "Timestamp") {
            if (typeof value !== "string" || !fullMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.(?:\d{3}|\d{6}|\d{9}))?Z$/, value) || Number(value.slice(0, 4)) === 0)
                this.fail(path, "canonical UTC timestamp");
            const date = new Date(value);
            if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 19) !== value.slice(0, 19))
                this.fail(path, "valid UTC timestamp");
            return true;
        }
        if (name === "Duration") {
            if (typeof value !== "string" || !fullMatch(/^-?(0|[1-9]\d*)(?:\.(?:\d{3}|\d{6}|\d{9}))?s$/, value) || Math.abs(Number(value.slice(0, -1))) > 315576000001)
                this.fail(path, "duration in range");
            if (Math.abs(Number(value.slice(0, -1).split(".")[0])) > 315576000000)
                this.fail(path, "duration in range");
            return true;
        }
        if (name === "FieldMask") {
            if (typeof value !== "string" || !fullMatch(/^(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?$/, value))
                this.fail(path, "canonical field mask string");
            return true;
        }
        if (name === "Value") {
            this.json(value, path, depth + 1);
            return true;
        }
        if (name === "Struct") {
            const fields = this.record(value, path);
            for (const key of Object.keys(fields))
                this.json(fields[key], path + "." + key, depth + 1);
            return true;
        }
        if (name === "ListValue") {
            if (!Array.isArray(value))
                this.fail(path, "array");
            for (const element of value)
                this.json(element, path, depth + 1);
            return true;
        }
        if (name === "Any") {
            const fields = this.record(value, path);
            if (typeof fields["@type"] !== "string")
                this.fail(path, "Any @type");
            const nestedType = (fields["@type"] as string).split("/").slice(-1)[0];
            if (nestedType === type || !hasOwn(this.schema.messages, nestedType))
                this.fail(path, "known Any type");
            const nested = Object.fromEntries(Object.entries(fields).filter(([key]) => key !== "@type"));
            if (nestedType.startsWith(prefix) && nestedType !== prefix + "Empty") {
                if (Object.keys(nested).length !== 1 || !("value" in nested))
                    this.fail(path, "Any value");
                this.message(nestedType, nested.value, path + ".value", depth + 1);
            }
            else
                this.message(nestedType, nested, path, depth + 1);
            return true;
        }
        return false;
    }
    private scalar(field: FieldShape, value: unknown, path: string, depth: number): void {
        this.budget(path, depth);
        const kind = field.kind;
        if (kind === "message" || kind === "group") {
            this.message(field.type!, value, path, depth + 1);
            return;
        }
        if (kind === "enum") {
            if (field.type === "google.protobuf.NullValue" && value === null)
                return;
            if (typeof value === "number" && Number.isInteger(value) && value >= -2147483648 && value <= 2147483647)
                return;
            if (typeof value === "string" && this.schema.enums[field.type!]?.includes(value))
                return;
            this.fail(path, "enum name or int32");
        }
        if (signed64.has(kind) || unsigned64.has(kind)) {
            if (!validIntegerString(value, signed64.has(kind)))
                this.fail(path, kind + " decimal string");
            return;
        }
        if (signed32.has(kind) || unsigned32.has(kind)) {
            if (typeof value !== "number" || !Number.isInteger(value) || value < (signed32.has(kind) ? -2147483648 : 0) || value > (signed32.has(kind) ? 2147483647 : 4294967295))
                this.fail(path, kind);
            return;
        }
        if (kind === "double" || kind === "float") {
            if ((typeof value === "number" && Number.isFinite(value) && (kind !== "float" || Math.abs(value) <= 3.4028234663852886e38)) || ["NaN", "Infinity", "-Infinity"].includes(value as string))
                return;
            this.fail(path, kind);
        }
        if (kind === "bool") {
            if (typeof value !== "boolean")
                this.fail(path, "boolean");
            return;
        }
        if (kind === "string" || kind === "bytes") {
            if (typeof value !== "string")
                this.fail(path, "string");
            if (kind === "bytes" && !fullMatch(/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/, value))
                this.fail(path, "padded Base64");
            return;
        }
        this.fail(path, "supported scalar type");
    }
    private json(value: unknown, path: string, depth: number): void {
        this.budget(path, depth);
        if (value === null || typeof value === "string" || typeof value === "boolean" || (typeof value === "number" && Number.isFinite(value)))
            return;
        if (Array.isArray(value)) {
            for (const item of value)
                this.json(item, path, depth + 1);
            return;
        }
        const fields = this.record(value, path);
        for (const name of Object.keys(fields))
            this.json(fields[name], path + "." + name, depth + 1);
    }
}
