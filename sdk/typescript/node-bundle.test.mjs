import assert from "node:assert/strict";
import { test } from "node:test";
import { cp, mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { DataBundle, DataStore } from "./dist/node-bundle.js";

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
    const bundle = await DataBundle.open(new URL(source).pathname);
    assert.equal(bundle.decode("tabforge.demo.structures.Tree", '{"name":"root","children":[{"name":"leaf","state":"ACTIVE"}]}').children[0].name, "leaf");
    for (const json of ['{"items":[{"ownerId":18446744073709551615}]}','{"items":[{"textEffect":"x","powerEffect":0}]}','{"unknown":1}','{"items":[{"rarity":"NO"}]}','{"items":[],"\\u0069tems":[]}', '{"items":[{"createdAt":"2026-02-30T00:00:00Z"}]}']) {
        assert.throws(() => bundle.decode("tabforge.demo.config.Tables", json));
    }
    const cases = JSON.parse(await readFile(new URL("../../examples/backend/cases.json", import.meta.url), "utf8"));
    for (const c of cases) {
        if (c.valid) bundle.decode(c.message, c.json);
        else assert.throws(() => bundle.decode(c.message, c.json), `accepted ${c.json}`);
    }
});
