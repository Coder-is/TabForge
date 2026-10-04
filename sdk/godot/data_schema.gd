extends RefCounted

const Validator = preload("wire_validator.gd")
var _validator: RefCounted
var error := ""

func _init(wire_schema: Dictionary = {}) -> void:
	_validator = Validator.new(wire_schema)

# Returns null on failure; inspect error. Message names are full Proto names.
func decode(type_name: String, text: String) -> Variant:
	error = ""
	var parser := JSON.new()
	if parser.parse(text) != OK:
		error = "Invalid JSON at line %d: %s" % [parser.get_error_line(), parser.get_error_message()]
		return null
	return validate(type_name, parser.data)

func validate(type_name: String, value: Variant) -> Variant:
	error = _validator.validate(type_name, value)
	return value if error.is_empty() else null

func read_file(type_name: String, path: String) -> Variant:
	var file := FileAccess.open(path, FileAccess.READ)
	if file == null:
		error = "Cannot read %s (error %d)" % [path, FileAccess.get_open_error()]
		return null
	return decode(type_name, file.get_as_text())
