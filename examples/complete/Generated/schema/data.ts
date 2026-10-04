/** Loads standalone ProtoJSON data using the same field rules as network SDKs. */
import { SchemaValidator } from "./schema.ts";
import type { WireSchema } from "./schema.ts";

export class DataSchema<T extends object> {
    private readonly validator: SchemaValidator;
    constructor(schema: WireSchema) { this.validator = new SchemaValidator(schema); }

    decode<K extends keyof T & string>(type: K, json: string): T[K] {
        return this.validate(type, JSON.parse(json) as unknown);
    }

    validate<K extends keyof T & string>(type: K, value: unknown): T[K] {
        this.validator.validate(type, value);
        return value as T[K];
    }
}
