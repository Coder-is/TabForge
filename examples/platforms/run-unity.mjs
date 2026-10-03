import { spawn } from "node:child_process";
import { readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const project = fileURLToPath(new URL("./unity", import.meta.url));
const metadata = fileURLToPath(new URL("../protocol/generated/runtime.json", import.meta.url));
if (!process.env.UNITY_EDITOR) throw new Error("Set UNITY_EDITOR to the installed, licensed editor executable");
const result = project + "/TestResults.xml";
await rm(result, { force: true });
const child = spawn(process.env.UNITY_EDITOR, ["-batchmode", "-nographics", "-projectPath", project, "-runTests", "-testPlatform", "PlayMode", "-testFilter", "TabForgeNetworkTests", "-testResults", result, "-logFile", project + "/acceptance.log"], {
  stdio: "inherit", env: { ...process.env, TABFORGE_TEST_URL: process.env.TABFORGE_TEST_URL || "http://127.0.0.1:18083", TABFORGE_RUNTIME_JSON: metadata }
});
const timer = setTimeout(() => child.kill(), 10 * 60 * 1000);
const code = await new Promise((resolve, reject) => { child.on("error", reject); child.on("exit", resolve); }).finally(() => clearTimeout(timer));
if (code !== 0) throw new Error("Unity test process failed: " + code);
const report = await readFile(result, "utf8");
if (!/<test-case\b[^>]*NativeHttpAndStreaming[^>]*result="Passed"/.test(report) || /result="(?:Failed|Skipped|Inconclusive)"/.test(report)) throw new Error("Unity acceptance was not fully executed/passed: " + result);
console.log("Unity engine native HTTP/SSE acceptance passed: " + result);
