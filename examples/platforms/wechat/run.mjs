// Install the optional Tencent automator separately; it is not a SDK dependency.
// TABFORGE_AUTOMATOR=/absolute/path/to/node_modules/miniprogram-automator
import { createRequire } from "node:module";
import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const require = createRequire(import.meta.url);
const automator = require(process.env.TABFORGE_AUTOMATOR || "miniprogram-automator");
// Recompiling/reloading DevTools can drop an outstanding automation response.
const watchdog = setTimeout(() => { console.error("WeChat automation deadline exceeded; wait for project compilation, then reconnect with WECHAT_WS"); process.exit(1); }, 60000);
const mini = process.env.WECHAT_WS ? await automator.connect({ wsEndpoint: process.env.WECHAT_WS }) : await automator.launch({
  projectPath: fileURLToPath(new URL("./", import.meta.url)),
  cliPath: process.env.WECHAT_CLI || "/Applications/wechatwebdevtools.app/Contents/MacOS/cli",
  port: 9420, timeout: 60000, trustProject: true
});
try {
  const page = await mini.currentPage();
  if (process.env.TABFORGE_TEST_URL) await page.setData({ baseURL: process.env.TABFORGE_TEST_URL });
  await page.callMethod("run");
  const info = await mini.systemInfo();
  const report = { system: { platform: info.platform, model: info.model, SDKVersion: info.SDKVersion }, results: await page.data("results") };
  console.log(JSON.stringify(report, null, 2));
  if (process.env.TABFORGE_REPORT) await writeFile(process.env.TABFORGE_REPORT, JSON.stringify(report, null, 2));
  if (!report.results.length || report.results.some(result => !result.passed)) process.exitCode = 1;
} finally { clearTimeout(watchdog); mini.disconnect(); }
