import { readdir, readFile, writeFile } from "node:fs/promises";

// TypeScript rewrites executable imports, but leaves .ts specifiers in .d.ts.
// Published declarations must refer to the corresponding compiled modules.
for (const name of await readdir(new URL("./dist/", import.meta.url))) {
    if (!name.endsWith(".d.ts")) continue;
    const path = new URL("./dist/" + name, import.meta.url);
    const text = await readFile(path, "utf8");
    await writeFile(path, text.replace(/(["'])(\.\.?\/[^"'\n]+)\.ts\1/g,
        (_, quote, module) => quote + module + ".js" + quote));
}
