/** Loads standalone ProtoJSON data using the same field rules as network SDKs. */
import { SchemaValidator } from "./schema.ts";
import type { WireSchema } from "./schema.ts";
import { parseJSON } from "./json.ts";

export class DataSchema<T extends object> {
    private readonly validator: SchemaValidator;
    private readonly schema: WireSchema;
    constructor(schema: WireSchema) { this.schema = schema; this.validator = new SchemaValidator(schema); }

    decode<K extends keyof T & string>(type: K, json: string): T[K] {
        return this.validate(type, parseJSON(json));
    }

    validate<K extends keyof T & string>(type: K, value: unknown): T[K] {
        if (!Object.prototype.hasOwnProperty.call(this.schema.messages, type)) throw new Error(`Unknown message type ${type}`);
        this.validator.validate(type, value);
        return value as T[K];
    }
}
