@tool
extends EditorPlugin

const Runner = preload("runner.gd")
var runner := Runner.new()
var panel: VBoxContainer
var log_view: TextEdit
var chooser: EditorFileDialog
var running := false
var action := ""

func _enter_tree() -> void:
	panel = VBoxContainer.new()
	panel.name = "TabForge"
	var label := Label.new(); label.text = "TabForge：项目根目录下的 TabForge/ 放置源文件"; panel.add_child(label)
	var buttons := HBoxContainer.new(); panel.add_child(buttons)
	for item in [["创建示例", "init"], ["校验", "check"], ["导出并导入", "export"], ["导入已有包", "import"], ["取消", "cancel"]]:
		var button := Button.new(); button.text = item[0]; button.pressed.connect(_action.bind(item[1])); buttons.add_child(button)
	log_view = TextEdit.new(); log_view.editable = false; log_view.custom_minimum_size = Vector2(0, 200); panel.add_child(log_view)
	add_control_to_bottom_panel(panel, "TabForge")
	add_tool_menu_item("TabForge 导出并导入", _action.bind("export"))
	chooser = EditorFileDialog.new(); chooser.file_mode = EditorFileDialog.FILE_MODE_OPEN_DIR; chooser.access = EditorFileDialog.ACCESS_FILESYSTEM
	chooser.dir_selected.connect(func(dir: String): _run("import", dir)); panel.add_child(chooser)
	set_process(true)

func _action(operation: String) -> void:
	if operation == "cancel": runner.cancel(); return
	if running: log_view.text += "\n正在执行，请等待或取消。"; return
	if operation == "import": chooser.popup_centered_ratio(); return
	_run(operation)

func _run(operation: String, bundle := "") -> void:
	if running: return
	var root := ProjectSettings.globalize_path("res://")
	var tool: String = Runner.tool_path(root, ProjectSettings.globalize_path(get_script().resource_path.get_base_dir()))
	action = operation
	running = runner.start(tool, root, operation, bundle)
	log_view.text = "正在" + operation if running else "找不到导出工具或无法启动；安装对应平台的插件包。"
	make_bottom_panel_item_visible(panel)

func _process(_delta: float) -> void:
	if not running: return
	var result: Dictionary = runner.poll()
	if result.is_empty(): return
	running = false
	log_view.text = result.output
	for diagnostic in result.report.get("diagnostics", []):
		log_view.text += "\n%s %s %s：%s\n%s" % [diagnostic.get("path", ""), diagnostic.get("sheet", ""), diagnostic.get("cell", ""), diagnostic.message, diagnostic.get("hint", "")]
	log_view.text += "\n成功" if result.success else "\n失败：上一版已导入资产保留。"
	if result.success and action != "check": get_editor_interface().get_resource_filesystem().scan()

func _exit_tree() -> void:
	runner.close()
	remove_tool_menu_item("TabForge 导出并导入")
	remove_control_from_bottom_panel(panel)
	panel.queue_free()
