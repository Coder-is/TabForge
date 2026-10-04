import { readdir, readFile, writeFile } from "node:fs/promises";

await writeFile(new URL("./dist/node-bundle.js", import.meta.url),
    (await readFile(new URL("./node-bundle.mjs", import.meta.url), "utf8")).replaceAll("./data.ts", "./data.js").replaceAll("./json.ts", "./json.js"));
await writeFile(new URL("./dist/node-bundle.d.ts", import.meta.url), await readFile(new URL("./node-bundle.d.ts", import.meta.url)));

// TypeScript rewrites executable imports, but leaves .ts specifiers in .d.ts.
// Published declarations must refer to the corresponding compiled modules.
for (const name of await readdir(new URL("./dist/", import.meta.url))) {
    if (!name.endsWith(".d.ts")) continue;
    const path = new URL("./dist/" + name, import.meta.url);
    const text = await readFile(path, "utf8");
    await writeFile(path, text.replace(/(["'])(\.\.?\/[^"'\n]+)\.ts\1/g,
        (_, quote, module) => quote + module + ".js" + quote));
}
