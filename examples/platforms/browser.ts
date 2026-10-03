import { CocosTransport } from "../../sdk/cocos/transport.ts";
import { protocolVersion, schemaHash, wireSchema } from "../protocol/generated/types.ts";
import { runSmoke } from "./smoke.ts";

const output = document.querySelector<HTMLPreElement>("#results")!;
const button = document.querySelector<HTMLButtonElement>("#run")!;
button.onclick = async () => {
  button.disabled = true; output.textContent = "Running…";
  const results = [];
  for (const backend of ["fetch", "xhr"] as const) {
    const transport = new CocosTransport(location.origin, protocolVersion, { schemaHash, wireSchema, backend });
    results.push({ backend, results: await runSmoke(transport) });
  }
  output.textContent = JSON.stringify({ userAgent: navigator.userAgent, results }, null, 2);
  const total = results.flatMap(item => item.results);
  document.querySelector("#summary")!.textContent = `通过 ${total.filter(item => item.passed).length} / ${total.length} 项；Fetch 与 XHR 各 12 项。`;
  output.dataset.done = "true"; button.disabled = false;
};
