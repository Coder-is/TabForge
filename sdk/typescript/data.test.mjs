import assert from "node:assert/strict";
import { test } from "node:test";
import { readFile } from "node:fs/promises";
import { DataSchema } from "./data.ts";
import { wireSchema } from "../../examples/complete/Generated/schema/types.ts";

test("portable data loader validates all example structures and preserves uint64 strings", async () => {
  const loader = new DataSchema(wireSchema);
  const json = await readFile(new URL('../../examples/complete/Generated/data/tables.json', import.meta.url), 'utf8');
  const tables = loader.decode('tabforge.demo.config.Tables', json);
  assert.equal(tables.items.length, 3);
  assert.equal(tables.items[0].reward.count, '18446744073709551615');
  assert.equal(tables.items[0].signedTotal, '-9223372036854775808');
  assert.equal(tables.items[0].unlockLevel, 0);
  assert.equal('unlockLevel' in tables.items[1], false);
  assert.equal(tables.items[0].metadata.count, '9');
  const mapped = loader.decode('tabforge.demo.config.Tables', await readFile(new URL('../../examples/complete/Generated/data/tables-map.json', import.meta.url), 'utf8'));
  assert.equal(mapped.byId['1001'].name, '新手剑😀');
  assert.deepEqual(loader.decode('tabforge.demo.structures.Tree', '{"name":"root","children":[{"name":"leaf","state":"ACTIVE"}]}').children[0].name, 'leaf');
});

test("data loader rejects invalid structures before returning typed data", () => {
  const loader = new DataSchema(wireSchema);
  for (const json of ['{', '{"items":[{"ownerId":18446744073709551615}]}', '{"items":[{"textEffect":"x","powerEffect":0}]}', '{"unknown":1}', '{"items":[{"rarity":"INVALID"}]}']) {
    assert.throws(() => loader.decode('tabforge.demo.config.Tables', json));
  }
  assert.throws(() => loader.decode('Missing', '{}'));
});
