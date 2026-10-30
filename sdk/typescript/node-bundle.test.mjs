import assert from "node:assert/strict";
import { test } from "node:test";
import { cp, mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import { DataBundle, DataStore } from "./dist/node-bundle.js";
import { DATA_LIMITS, parseJSON } from "./dist/json.js";

const source = new URL("../../examples/complete/Generated/", import.meta.url);
test("Node bundle loads source-free snapshots and failed reloads preserve old data", async t => {
    const root = await mkdtemp(join(tmpdir(), "tabforge backend 中文 "));
    t.after(() => rm(root, { recursive: true, force: true }));
    await cp(source, root, { recursive: true });
    const store = new DataStore(), bundle = await store.reload(root);
    const tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables");
    assert.equal(tables.items[0].ownerId, "18446744073709551615");
    tables.items[0].name = "mutated";
    assert.equal(bundle.read("data/tables.json").items[0].name, "新手剑😀");
    assert.equal(bundle.read("data/tables-map.json").byId[1001].reward.count, "18446744073709551615");
    assert.throws(() => bundle.read("data/tables.pbb"), /ProtoJSON/);
    assert.throws(() => bundle.read("data/tables.json", "Wrong"), /mismatch/);
    await assert.rejects(DataBundle.open(root, "0".repeat(64)), /hash mismatch/);
    await writeFile(join(root, "data/tables.json"), '{"unknown":1}');
    await assert.rejects(store.reload(root), /Checksum/);
    assert.equal(store.snapshot, bundle);
    assert.equal(bundle.read("data/tables.json").items.length, 3);
    const manifestPath = join(root, "data_manifest.json");
    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    manifest.data.find(e => e.path === "data/tables.json").sha256 = createHash("sha256").update('{"unknown":1}').digest("hex");
    await writeFile(manifestPath, JSON.stringify(manifest));
    await assert.rejects(DataBundle.open(root), /declared field/);
    manifest.data[0].path = "../outside";
    await writeFile(manifestPath, JSON.stringify(manifest));
    await assert.rejects(DataBundle.open(root), /Invalid/);
});

test("all ProtoJSON wire structures and unambiguous parsing work in the Node package", async () => {
    const bundle = await DataBundle.open(fileURLToPath(source));
    assert.equal(bundle.decode("tabforge.demo.structures.Tree", '{"name":"root","children":[{"name":"leaf","state":"ACTIVE"}]}').children[0].name, "leaf");
    for (const json of ['{"items":[{"textEffect":"x","powerEffect":0}]}','{"unknown":1}','{"items":[{"rarity":"NO"}]}','{"items":[],"\\u0069tems":[]}', '{"items":[{"createdAt":"2026-02-30T00:00:00Z"}]}']) {
        assert.throws(() => bundle.decode("tabforge.demo.config.Tables", json));
    }
    const cases = JSON.parse(await readFile(new URL("../../examples/backend/cases.json", import.meta.url), "utf8"));
    for (const c of cases) {
        if (c.valid) bundle.decode(c.message, c.json);
        else assert.throws(() => bundle.decode(c.message, c.json), `accepted ${c.json}`);
    }
});

// Same fixture as databundle/cases_test.go: 20,000 rows, 180,002 JSON values.
const largeTable = () => '{"items":[' + Array.from({ length: 20000 }, (_, i) => `{"id":${i + 1},"name":"item ${i + 1}","ownerId":"${i + 1}","levels":[1,2,3],"price":1.5}`).join(",") + "]}";

test("data files above the 100,000-value message budget load with the data bundle budget", async t => {
    const root = await mkdtemp(join(tmpdir(), "tabforge large "));
    t.after(() => rm(root, { recursive: true, force: true }));
    await cp(source, root, { recursive: true });
    const table = largeTable();
    await writeFile(join(root, "data/tables.json"), table);
    const manifestPath = join(root, "data_manifest.json");
    const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
    manifest.data.find(e => e.path === "data/tables.json").sha256 = createHash("sha256").update(table).digest("hex");
    await writeFile(manifestPath, JSON.stringify(manifest));
    const bundle = await DataBundle.open(root);
    const tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables");
    assert.equal(tables.items.length, 20000);
    assert.equal(tables.items[19999].ownerId, "20000");
    assert.throws(() => parseJSON(table), /exceeds/); // network messages keep the 100,000-value budget
    assert.equal(DATA_LIMITS.maxNodes, 10000000);
    assert.equal(DATA_LIMITS.maxDepth, 64);
    const nested = depth => "[".repeat(depth) + "0" + "]".repeat(depth);
    parseJSON(nested(64), DATA_LIMITS);
    assert.throws(() => parseJSON(nested(65), DATA_LIMITS), /exceeds/);
    assert.throws(() => parseJSON("[" + "0,".repeat(9) + "0]", { maxDepth: 64, maxNodes: 10 }), /exceeds/);
});

test("decoded data is canonical ProtoJSON with exact 64-bit integers", async () => {
    const bundle = await DataBundle.open(fileURLToPath(source));
    const item = bundle.decode("tabforge.demo.config.Item", '{"id":"1e2","ownerId":18446744073709551615,"signedTotal":-9.223372036854775808e18,"rarity":1.0,"levels":[1.0,"2"],"iconHash":"AAF","priority":"7","price":"1.5","name":null,"stats":{"__proto__":1e0}}');
    assert.deepEqual(item, { id: 100, ownerId: "18446744073709551615", signedTotal: "-9223372036854775808", rarity: 1, levels: [1, 2], iconHash: "AAE=", priority: 7, price: 1.5, stats: JSON.parse('{"__proto__":1}') });
    assert.equal(Object.getPrototypeOf(item.stats), Object.prototype);
    assert.deepEqual(bundle.decode("tabforge.demo.config.Tables", '{"byId":{"+1":{"id":1},"02":{}}}'), { byId: { "1": { id: 1 }, "2": {} } });
    assert.equal(bundle.decode("google.protobuf.UInt64Value", "18446744073709551615"), "18446744073709551615");
});
