#if UNITY_EDITOR
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using Newtonsoft.Json.Linq;
using UnityEditor;
using UnityEditor.PackageManager;
using UnityEngine;
using Debug = UnityEngine.Debug;

namespace TabForge.Editor
{
    public sealed class TabForgeEditor : EditorWindow
    {
        private Process process;
        private readonly StringBuilder output = new StringBuilder();
        [SerializeField] private string source, bundle;
        private string action, message = "在项目 TabForge 目录维护 Protocols 和 Tables。";
        private Vector2 scroll;
        private string Root => Directory.GetParent(Application.dataPath).FullName;
        [MenuItem("Tools/TabForge/项目导出")]
        public static void Open() { GetWindow<TabForgeEditor>("TabForge"); }
        private void OnEnable()
        {
            if (string.IsNullOrEmpty(source)) source = File.Exists(Path.Combine(Root, "tabforge.json")) ? Root : Path.Combine(Root, "TabForge");
            EditorApplication.update += Poll;
        }
        private void OnDisable()
        {
            EditorApplication.update -= Poll;
            if (process != null)
            {
                var pid = process.Id;
                if (!process.HasExited) process.Kill();
                if (process.WaitForExit(1000)) CleanupLocks(pid);
                process.Dispose(); process = null;
            }
        }
        private void OnGUI()
        {
            EditorGUILayout.LabelField("TabForge 协议与配置", EditorStyles.boldLabel);
            source = EditorGUILayout.TextField("源项目目录", source);
            bundle = EditorGUILayout.TextField("已有 Generated 目录", bundle);
            EditorGUILayout.HelpBox(message, MessageType.Info);
            using (new EditorGUI.DisabledScope(process != null))
            {
                if (GUILayout.Button("创建完整示例")) Run("init");
                if (GUILayout.Button("校验（保留当前产物）")) Run("check");
                if (GUILayout.Button("导出并导入 Unity")) Run("export");
                if (GUILayout.Button("选择已有协议包并导入"))
                {
                    var selected = EditorUtility.OpenFolderPanel("选择 Generated 目录", bundle ?? source, "");
                    if (!string.IsNullOrEmpty(selected)) { bundle = selected; Run("import"); }
                }
            }
            if (process != null && GUILayout.Button("取消")) process.Kill();
            if (GUILayout.Button("打开最近报告")) EditorUtility.RevealInFinder(Path.Combine(Root, ".tabforge-report.json"));
            scroll = EditorGUILayout.BeginScrollView(scroll);
            lock (output) EditorGUILayout.SelectableLabel(output.ToString(), GUILayout.MinHeight(240));
            EditorGUILayout.EndScrollView();
        }
        private void Run(string operation)
        {
            if (process != null) return;
            action = operation;
            try
            {
                var name = Application.platform == RuntimePlatform.WindowsEditor ? "tabforge.exe" : "tabforge";
                var platform = Application.platform == RuntimePlatform.WindowsEditor ? "win32" : Application.platform == RuntimePlatform.OSXEditor ? "darwin" : "linux";
                var arch = RuntimeInformation.ProcessArchitecture == Architecture.Arm64 ? "arm64" : "x64";
                var package = PackageInfo.FindForAssembly(typeof(TabForgeEditor).Assembly);
                var local = Path.Combine(source, "Tools", "TabForge", name);
                var bundled = Path.Combine(package?.resolvedPath ?? "", "bin", platform + "-" + arch, name);
                if (!File.Exists(bundled))
                {
                    // The editor may run under Rosetta/x64 emulation while the
                    // separately launched Go compiler uses the host native CPU.
                    foreach (var cpu in new[] { "arm64", "x64" })
                    {
                        var candidate = Path.Combine(package?.resolvedPath ?? "", "bin", platform + "-" + cpu, name);
                        if (File.Exists(candidate)) { bundled = candidate; break; }
                    }
                }
                var tool = File.Exists(bundled) ? bundled : local;
                if (!File.Exists(tool)) throw new FileNotFoundException("安装对应平台的 TabForge Editor 包，或把工具放入源项目 Tools/TabForge。", tool);
                var args = new List<string> { "-report", "-editor=unity", "-editor_project=" + Root };
                args.Add(operation == "init" ? "-init=" + source : operation == "import" ? "-import=" + bundle : "-project=" + source);
                if (operation == "check") args.Add("-check");
                File.Delete(Path.Combine(Root, ".tabforge-report.json"));
                lock (output) output.Clear();
                process = new Process { StartInfo = new ProcessStartInfo { FileName = tool, Arguments = string.Join(" ", args.ConvertAll(Quote)), WorkingDirectory = Root, UseShellExecute = false, CreateNoWindow = true, RedirectStandardOutput = true, RedirectStandardError = true, StandardOutputEncoding = Encoding.UTF8, StandardErrorEncoding = Encoding.UTF8 } };
                process.OutputDataReceived += (_, e) => Append(e.Data);
                process.ErrorDataReceived += (_, e) => Append(e.Data);
                process.Start(); process.BeginOutputReadLine(); process.BeginErrorReadLine();
                message = "正在" + operation + "；可取消。";
            }
            catch (Exception error) { process?.Dispose(); process = null; message = error.Message; Debug.LogError(error); }
        }
        private void Append(string text)
        {
            if (text == null) return;
            lock (output) { if (output.Length < 1024 * 1024) output.AppendLine(text); }
        }
        private void Poll()
        {
            if (process == null) return;
            if (!process.HasExited) { Repaint(); return; }
            process.WaitForExit();
            CleanupLocks(process.Id);
            var code = process.ExitCode;
            process.Dispose(); process = null;
            try
            {
                var report = JObject.Parse(File.ReadAllText(Path.Combine(Root, ".tabforge-report.json")));
                var success = code == 0 && (string)report["format"] == "tabforge.report.v1" && (bool?)report["success"] == true;
                message = success ? "操作成功。生成目录：" + (string)report["editorOutput"] : "操作失败；下方日志和报告包含文件、工作表及单元格。";
                foreach (var d in report["diagnostics"] as JArray ?? new JArray()) Append((string)d["path"] + " " + (string)d["sheet"] + " " + (string)d["cell"] + ": " + (string)d["message"] + "\n" + (string)d["hint"]);
                if (success && action != "check") AssetDatabase.Refresh();
                if (!success) Debug.LogError("TabForge: " + output);
            }
            catch (Exception error) { message = "工具未返回报告，查看日志：" + error.Message; }
            Repaint();
        }
        // ProcessStartInfo.Arguments uses command-line quoting, never a shell.
        private static string Quote(string value)
        {
            var b = new StringBuilder("\""); var slashes = 0;
            foreach (var c in value)
            {
                if (c == '\\') { slashes++; continue; }
                if (c == '"') { b.Append('\\', slashes * 2 + 1); b.Append(c); }
                else { b.Append('\\', slashes); b.Append(c); }
                slashes = 0;
            }
            b.Append('\\', slashes * 2); b.Append('"'); return b.ToString();
        }
        private void CleanupLocks(int pid)
        {
            var sourceRoot = File.Exists(source) ? Path.GetDirectoryName(source) : source;
            foreach (var file in new[] { Path.Combine(sourceRoot, ".tabforge-export.lock"), Path.Combine(Root, ".tabforge-client.lock") })
            {
                try { if (File.Exists(file) && File.ReadAllText(file).Trim() == pid.ToString()) File.Delete(file); }
                catch (IOException) { /* Another exporter may own the directory. */ }
                catch (UnauthorizedAccessException) { /* Report the next lock failure without deleting blindly. */ }
            }
        }
    }
}
#endif
