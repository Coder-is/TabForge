import { DataSchema } from "../../Generated/schema/data.ts";
import { wireSchema } from "../../Generated/schema/types.ts";
import type { MessageTypes } from "../../Generated/schema/types.ts";

const loader = new DataSchema<MessageTypes>(wireSchema);

// In a browser, use fetch to read the JSON from the project's public assets.
// In Node, pass the result of readFile(path, "utf8") to this function.
export function loadTables(json: string) {
    const tables = loader.decode("tabforge.demo.config.Tables", json);
    const byID = new Map(tables.items?.map(item => [item.id, item]));
    console.log(byID.get(1001)?.name, byID.get(1001)?.reward?.count);
    return tables;
}

export function loadTree(json: string) {
    return loader.decode("tabforge.demo.structures.Tree", json);
}
