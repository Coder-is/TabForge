import assert from "node:assert/strict";
import { test } from "node:test";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

test("distributed JS and declarations work in an independent consumer without source files", async t => {
  const root = await mkdtemp(join(tmpdir(), "tabforge-package-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const pkg = join(root, "node_modules", "@tabforge", "protocol-runtime");
  await mkdir(pkg, { recursive: true });
  await cp(new URL("./dist/", import.meta.url), join(pkg, "dist"), { recursive: true });
  await cp(new URL("./package.json", import.meta.url), join(pkg, "package.json"));
  for (const file of await readdir(join(pkg, "dist"))) {
    if (file.endsWith(".d.ts")) assert.doesNotMatch(await readFile(join(pkg, "dist", file), "utf8"), /["']\.\.?\/[^"'\n]+\.ts["']/);
  }
  await writeFile(join(root, "package.json"), '{"type":"module"}');
  await cp(new URL("../../examples/complete/Generated/schema/types.ts", import.meta.url), join(root, "types.ts"));
  await writeFile(join(root, "main.mjs"), `
    import { DataSchema } from '@tabforge/protocol-runtime/data';
    import { ProtocolClient, FetchTransport } from '@tabforge/protocol-runtime';
    import { CallbackTransport } from '@tabforge/protocol-runtime/callback';
    import { DataBundle, DataStore } from '@tabforge/protocol-runtime/node';
    const loader = new DataSchema({messages:{M:{fields:{count:{kind:'uint64'}}}},enums:{}});
    const result = loader.decode('M','{"count":"18446744073709551615"}');
    if(result.count !== '18446744073709551615') throw Error('precision loss');
    console.log(result.count);
  `);
  assert.match(execFileSync(process.execPath, [join(root, "main.mjs")], { encoding: "utf8" }), /18446744073709551615/);
  await writeFile(join(root, "main.ts"), `
    import { DataSchema } from '@tabforge/protocol-runtime/data';
    import { ProtocolClient } from '@tabforge/protocol-runtime';
    import { DataBundle, DataStore } from '@tabforge/protocol-runtime/node';
    import { wireSchema } from './types.js';
    import type { MessageTypes } from './types.js';
    const loader = new DataSchema<MessageTypes>(wireSchema);
    const tables = loader.decode('tabforge.demo.config.Tables','{}');
    const count: string | undefined = tables.items?.[0].reward?.count;
    // @ts-expect-error Invalid message name must be rejected.
    loader.decode('Missing','{}');
    // @ts-expect-error Unknown fields must be rejected.
    tables.items?.[0].missingField;
    const bundle = await DataBundle.open<MessageTypes>('Generated');
    const typed = bundle.read('data/tables.json','tabforge.demo.config.Tables');
    const id: string | undefined = typed.items?.[0].ownerId;
    // @ts-expect-error Invalid message name must be rejected.
    bundle.read('data/tables.json','Missing');
    const store = new DataStore<MessageTypes>();
    const optional = store.snapshot?.read('data/tables.json','tabforge.demo.config.Tables');
  `);
  const tsc = fileURLToPath(new URL("./node_modules/typescript/bin/tsc", import.meta.url));
  execFileSync(process.execPath, [tsc, "--strict", "--noEmit", "--target", "ES2022", "--module", "NodeNext", "--moduleResolution", "NodeNext", join(root, "main.ts")], { encoding: "utf8" });
});
