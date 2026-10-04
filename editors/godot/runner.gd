@tool
extends RefCounted

var process: Dictionary = {}
var output := PackedByteArray()
var errors := PackedByteArray()
var report_root := ""
var source_root := ""

static func source_project(root: String) -> String:
	return root if FileAccess.file_exists(root.path_join("tabforge.json")) else root.path_join("TabForge")

static func tool_path(root: String, plugin_dir: String) -> String:
	var platform := "win32" if OS.get_name() == "Windows" else "darwin" if OS.get_name() == "macOS" else "linux"
	var arch := "arm64" if Engine.get_architecture_name() == "arm64" else "x64"
	var name := "tabforge.exe" if platform == "win32" else "tabforge"
	var choices: Array[String] = []
	for cpu in [arch, "arm64", "x64"]:
		choices.append(plugin_dir.path_join("bin").path_join(platform + "-" + cpu).path_join(name))
	choices.append(source_project(root).path_join("Tools/TabForge").path_join(name))
	for candidate in choices:
		if FileAccess.file_exists(candidate): return candidate
	return ""

func start(tool: String, root: String, action: String, bundle := "") -> bool:
	if not process.is_empty() or tool.is_empty(): return false
	report_root = root
	source_root = root.path_join("TabForge") if action == "init" else source_project(root)
	var report_path := root.path_join(".tabforge-report.json")
	if FileAccess.file_exists(report_path): DirAccess.remove_absolute(report_path)
	var args := PackedStringArray(["-report", "-editor=godot", "-editor_project=" + root])
	if action == "init": args.append("-init=" + root.path_join("TabForge"))
	elif action == "import": args.append("-import=" + bundle)
	else:
		args.append("-project=" + source_project(root))
		if action == "check": args.append("-check")
	output.clear(); errors.clear()
	process = OS.execute_with_pipe(tool, args, false)
	return not process.is_empty()

func poll() -> Dictionary:
	if process.is_empty(): return {}
	output.append_array(process.stdio.get_buffer(8192))
	errors.append_array(process.stderr.get_buffer(8192))
	if OS.is_process_running(process.pid): return {}
	var code := OS.get_process_exit_code(process.pid)
	_cleanup_locks(process.pid)
	for stream in [process.stdio, process.stderr]:
		var bytes: PackedByteArray = stream.get_buffer(8192)
		while not bytes.is_empty():
			if stream == process.stdio: output.append_array(bytes)
			else: errors.append_array(bytes)
			bytes = stream.get_buffer(8192)
		stream.close()
	process.clear()
	var report_path := report_root.path_join(".tabforge-report.json")
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(report_path)) if FileAccess.file_exists(report_path) else null
	var report: Dictionary = parsed if parsed is Dictionary else {}
	return {"success": code == 0 and report.get("success", false) and report.get("format", "") == "tabforge.report.v1", "code":code, "report":report, "output":output.get_string_from_utf8() + errors.get_string_from_utf8()}

func cancel() -> void:
	if not process.is_empty(): OS.kill(process.pid)

func close() -> void:
	cancel()
	if not process.is_empty():
		var deadline := Time.get_ticks_msec() + 1000
		while OS.is_process_running(process.pid) and Time.get_ticks_msec() < deadline: OS.delay_msec(2)
		if not OS.is_process_running(process.pid): _cleanup_locks(process.pid)
		process.stdio.close(); process.stderr.close(); process.clear()

func _cleanup_locks(pid: int) -> void:
	for path in [source_root.path_join(".tabforge-export.lock"), report_root.path_join(".tabforge-client.lock")]:
		if FileAccess.file_exists(path) and FileAccess.get_file_as_string(path).strip_edges() == str(pid):
			DirAccess.remove_absolute(path)
