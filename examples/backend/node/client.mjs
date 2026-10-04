import { DataBundle } from "@tabforge/protocol-runtime/node";

const bundle = await DataBundle.open(process.argv[2] ?? "examples/complete/Generated");
const tables = bundle.read("data/tables.json", "tabforge.demo.config.Tables");
console.log(`${tables.items[0].name}: ownerId=${tables.items[0].ownerId}`);
