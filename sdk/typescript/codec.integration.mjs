// Launched by protocol/realtime tests. Reads cases written by Go, checks that
// the TypeScript codec decodes each binary message to Go's protojson output and
// writes re-encoded binaries for Go to compare with proto.Equal.
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import { ProtobufCodec, encodeBase64, decodeBase64 } from "./protobuf.ts";
import { SchemaValidator } from "./schema.ts";
import { codecSchema, wireSchema } from "../../examples/realtime/Generated/protocol/types.ts";

const [input, output] = process.argv.slice(2);
const cases = JSON.parse(readFileSync(input, "utf8"));
const codec = new ProtobufCodec(codecSchema);
const normalizer = new SchemaValidator(wireSchema, { normalize: true });

// protojson prints float32 with float32 precision; compare as float32.
function same(a, b, path) {
    if (typeof a === "number" && typeof b === "number") {
        if (a !== b && Math.fround(a) !== Math.fround(b))
            assert.fail(`${path}: ${a} != ${b}`);
        return;
    }
    if (a && b && typeof a === "object" && typeof b === "object") {
        assert.equal(Array.isArray(a), Array.isArray(b), path);
        const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
        for (const key of keys) {
            if (!(key in a) || !(key in b))
                assert.fail(`${path}.${key}: present on one side only (${JSON.stringify(a[key])} vs ${JSON.stringify(b[key])})`);
            same(a[key], b[key], `${path}.${key}`);
        }
        return;
    }
    assert.equal(a, b, path);
}

const results = [];
for (const [i, c] of cases.entries()) {
    const expected = JSON.parse(c.json);
    const decoded = codec.decode(c.type, decodeBase64(c.binary));
    try {
        same(decoded, expected, `case ${i} ${c.type}`);
    } catch (error) {
        console.error(c.json);
        console.error(JSON.stringify(decoded));
        throw error;
    }
    // The decoded object must also be valid ProtoJSON for the validator.
    new SchemaValidator(wireSchema).validate(c.type, decoded);
    const fromJSON = codec.encode(c.type, normalizer.validate(c.type, JSON.parse(c.json)));
    const fromDecoded = codec.encode(c.type, decoded);
    results.push({ fromJSON: encodeBase64(fromJSON), fromDecoded: encodeBase64(fromDecoded) });
}
writeFileSync(output, JSON.stringify(results));
console.log(`codec: ${cases.length} messages match Go`);
