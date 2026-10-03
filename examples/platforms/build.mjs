import { build } from "../../sdk/typescript/node_modules/esbuild/lib/main.js";
import { mkdir, copyFile, cp, readdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";
import ts from "../../sdk/typescript/node_modules/typescript/lib/typescript.js";
const root = fileURLToPath(new URL("./", import.meta.url));
await build({ entryPoints: [root + "browser.ts"], bundle: true, format: "esm", target: "es2020", outfile: root + "dist/browser.js" });
await build({ entryPoints: [root + "wechat.ts"], bundle: true, format: "cjs", target: "es2020", outfile: root + "wechat/vendor/tabforge.js" });
await build({ entryPoints: [root + "cocos/entry.ts"], bundle: true, format: "esm", target: "es2020", tsconfigRaw: {}, outfile: root + "cocos/assets/tabforge.js" });
// Ship declarations with the bundle, so Creator never has to compile SDK source
// outside assets or understand Node's explicit .ts import paths.
const declarations = path.join(root, "cocos/assets/types");
const program = ts.createProgram([root + "cocos/entry.ts"], {
  strict: true, declaration: true, emitDeclarationOnly: true,
  allowImportingTsExtensions: true, module: ts.ModuleKind.ESNext,
  moduleResolution: ts.ModuleResolutionKind.Bundler, target: ts.ScriptTarget.ES2020,
  rootDir: path.resolve(root, "../.."), outDir: declarations
});
const diagnostics = ts.getPreEmitDiagnostics(program);
if (diagnostics.length) throw new Error(ts.formatDiagnosticsWithColorAndContext(diagnostics, {
  getCurrentDirectory: () => process.cwd(), getNewLine: () => "\n", getCanonicalFileName: name => name
}));
if (program.emit().emitSkipped) throw new Error("Cocos SDK declarations were not emitted");
async function normalizeImports(directory) {
  for (const item of await readdir(directory, { withFileTypes: true })) {
    const file = path.join(directory, item.name);
    if (item.isDirectory()) await normalizeImports(file);
    else if (file.endsWith(".d.ts")) await writeFile(file, (await readFile(file, "utf8")).replace(/(["'][^"']+)\.ts(["'])/g, "$1$2"));
  }
}
await normalizeImports(declarations);
await mkdir(root + "unity/Assets/Resources", { recursive: true });
await copyFile(root + "../protocol/generated/runtime.json", root + "unity/Assets/Resources/tabforge-runtime.json");
await cp(root + "../../sdk/unreal", root + "unreal/Plugins/TabForgeProtocol", { recursive: true, filter: path => !/[\\/](Binaries|Intermediate)([\\/]|$)/.test(path) });
console.log("Built browser, WeChat, Cocos bundles, Unity metadata and Unreal plugin fixture");
