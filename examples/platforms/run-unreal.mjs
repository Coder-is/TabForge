import { spawn } from "node:child_process";
import { cp, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const project = fileURLToPath(new URL("./unreal", import.meta.url));
const metadata = fileURLToPath(new URL("../protocol/generated/runtime.json", import.meta.url));
if (!process.env.UNREAL_EDITOR || !process.env.UNREAL_PLUGIN_BUILD) throw new Error("Set UNREAL_EDITOR and UNREAL_PLUGIN_BUILD (the RunUAT BuildPlugin output directory)");
await cp(process.env.UNREAL_PLUGIN_BUILD, project + "/Plugins/TabForgeProtocol", { recursive: true });
const report = project + "/Saved/TabForgeReport";
// Only this runner's generated report is cleared to prevent a stale success.
await rm(report, { recursive: true, force: true });
const child = spawn(process.env.UNREAL_EDITOR, [project + "/TabForgeSmoke.uproject", "-unattended", "-nop4", "-nosplash", "-NullRHI", "-ExecCmds=Automation RunTests TabForge.Protocol", "-TestExit=Automation Test Queue Empty", "-ReportExportPath=" + report], {
  stdio: "inherit", env: { ...process.env, TABFORGE_TEST_URL: process.env.TABFORGE_TEST_URL || "http://127.0.0.1:18083", TABFORGE_RUNTIME_JSON: metadata }
});
const timer = setTimeout(() => child.kill(), 10 * 60 * 1000);
const code = await new Promise((resolve, reject) => { child.on("error", reject); child.on("exit", resolve); }).finally(() => clearTimeout(timer));
if (code !== 0) throw new Error("Unreal test process failed: " + code);
const result = JSON.parse(await readFile(report + "/index.json", "utf8"));
for (const path of ["TabForge.Protocol.Parser", "TabForge.Protocol.NativeHTTP"]) {
  const test = result.tests?.find(test => test.fullTestPath === path);
  if (test?.state !== "Success") throw new Error("Unreal acceptance not fully executed/passed: " + path);
}
console.log("Unreal engine native HTTP/SSE acceptance passed: " + report);
