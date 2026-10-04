import { readFile, lstat } from "node:fs/promises";
import { resolve, join } from "node:path";
import { createHash } from "node:crypto";
import { DataSchema } from "./data.ts";
import { parseJSON } from "./json.ts";

const hash = value => typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
const digest = bytes => createHash("sha256").update(bytes).digest("hex");
const local = name => typeof name === "string" && name !== "" && !/[\\:]/.test(name) && name.split("/").every(p => p && p !== "." && p !== "..");
const record = value => value !== null && typeof value === "object" && !Array.isArray(value);
const utf8 = bytes => new TextDecoder("utf-8", { fatal: true }).decode(bytes);

/** Immutable Node.js snapshot of a portable Generated directory. */
export class DataBundle {
    #hash; #entries; #values; #loader;
    constructor(token, schemaHash, entries, values, loader) {
        if (token !== DataBundle) throw new Error("Use DataBundle.open");
        this.#hash = schemaHash; this.#entries = entries; this.#values = values; this.#loader = loader;
    }
    static async open(directory, expectedHash = "") {
        const root = resolve(directory), seen = new Set(["data_manifest.json"]);
        const read = async name => {
            if (!local(name)) throw new Error(`Invalid bundle path ${name}`);
            let current = root;
            for (const part of name.split("/")) {
                current = join(current, part);
                if ((await lstat(current)).isSymbolicLink()) throw new Error(`Symlink bundle path ${name}`);
            }
            if (!(await lstat(current)).isFile()) throw new Error(`Not a regular file: ${name}`);
            return readFile(current);
        };
        const checked = async entry => {
            if (!record(entry) || !local(entry.path) || !hash(entry.sha256) || seen.has(entry.path.toLowerCase())) throw new Error("Invalid or duplicate manifest file");
            seen.add(entry.path.toLowerCase());
            const data = await read(entry.path);
            if (digest(data) !== entry.sha256) throw new Error(`Checksum mismatch: ${entry.path}`);
            return data;
        };
        const manifest = parseJSON(utf8(await read("data_manifest.json")));
        if (!record(manifest) || manifest.format !== "tabforge.data.v1" || !hash(manifest.schemaHash) || !Array.isArray(manifest.data) || (expectedHash && expectedHash !== manifest.schemaHash)) throw new Error("Invalid manifest or schema hash mismatch");
        await checked(manifest.descriptor);
        const wire = parseJSON(utf8(await checked(manifest.wireSchema)));
        if (!record(wire) || !record(wire.messages) || !record(wire.enums)) throw new Error("Invalid wire schema");
        const loader = new DataSchema(wire), values = new Map(), entries = [];
        for (const entry of manifest.data) {
            const bytes = await checked(entry);
            if (typeof entry.message !== "string" || !Object.hasOwn(wire.messages, entry.message)) throw new Error(`Unknown message: ${entry.message}`);
            if (entry.encoding === "protojson") values.set(entry.path, loader.decode(entry.message, utf8(bytes)));
            else if (entry.encoding !== "protobuf") throw new Error(`Unsupported encoding: ${entry.encoding}`);
            entries.push(Object.freeze({ ...entry }));
        }
        return new DataBundle(DataBundle, manifest.schemaHash, entries, values, loader);
    }
    get schemaHash() { return this.#hash; }
    entries() { return this.#entries.map(entry => ({ ...entry })); }
    read(name, expectedMessage) {
        const entry = this.#entries.find(entry => entry.path === name);
        if (!entry) throw new Error(`Data file not declared: ${name}`);
        if (expectedMessage && entry.message !== expectedMessage) throw new Error("Message type mismatch");
        if (entry.encoding !== "protojson") throw new Error("Node data loader supports ProtoJSON; use the matching .json entry");
        return structuredClone(this.#values.get(name));
    }
    decode(message, json) { return this.#loader.decode(message, json); }
}

/** Failed reloads retain the previous snapshot. Keep one snapshot per request. */
export class DataStore {
    #snapshot;
    get snapshot() { return this.#snapshot; }
    async reload(directory, expectedHash = "") {
        const next = await DataBundle.open(directory, expectedHash);
        this.#snapshot = next;
        return next;
    }
}
