/** Loads standalone ProtoJSON data using the same field rules as network SDKs. */
import { SchemaValidator } from "./schema.ts";
import type { WireSchema } from "./schema.ts";
import { DATA_LIMITS, parseJSON } from "./json.ts";
import type { NumberSources } from "./json.ts";

/**
 * Data files use DATA_LIMITS (depth 64, 10,000,000 JSON values), the budget
 * every TabForge data loader enforces. Accepted input is returned as canonical
 * ProtoJSON: 64-bit integers as decimal strings, other numbers as numbers,
 * padded standard Base64, and null members removed.
 */
export class DataSchema<T extends object> {
    private readonly validator: SchemaValidator;
    private readonly schema: WireSchema;
    constructor(schema: WireSchema) { this.schema = schema; this.validator = new SchemaValidator(schema, { limits: DATA_LIMITS, normalize: true }); }

    decode<K extends keyof T & string>(type: K, json: string): T[K] {
        this.known(type);
        const sources: NumberSources = new WeakMap();
        return this.validator.validate(type, parseJSON(json, DATA_LIMITS, sources), sources) as T[K];
    }

    /** Validates an already parsed value and normalizes it in place. */
    validate<K extends keyof T & string>(type: K, value: unknown): T[K] {
        this.known(type);
        return this.validator.validate(type, value) as T[K];
    }

    private known(type: string): void {
        if (!Object.prototype.hasOwnProperty.call(this.schema.messages, type)) throw new Error(`Unknown message type ${type}`);
    }
}
