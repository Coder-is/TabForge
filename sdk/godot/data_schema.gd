extends RefCounted

const Validator = preload("wire_validator.gd")
var _validator: Validator
var error := ""

# Data files use the data bundle budget shared by every TabForge loader (depth
# 64, 10,000,000 JSON values) and are returned as canonical ProtoJSON: 32-bit
# integers and enum numbers as int, 64-bit integers as decimal String, other
# numbers as float, padded standard Base64, null members removed.
func _init(wire_schema: Dictionary = {}) -> void:
	_validator = Validator.new(wire_schema, Validator.DATA_MAX_NODES, true)

# Returns null on failure; inspect error. Message names are full Proto names.
func decode(type_name: String, text: String) -> Variant:
	error = ""
	var parsed: Dictionary = Validator.parse_json(text, Validator.DATA_MAX_NODES, true)
	if parsed.has("error"):
		error = "Invalid JSON: " + str(parsed.error)
		return null
	return validate(type_name, parsed.value)

# Strict UTF-8 (no overlongs, surrogates or BOM) before JSON parsing.
func decode_bytes(type_name: String, bytes: PackedByteArray) -> Variant:
	error = ""
	var parsed: Dictionary = Validator.parse_json_bytes(bytes, Validator.DATA_MAX_NODES, true)
	if parsed.has("error"):
		error = "Invalid UTF-8" if parsed.error == "invalid_utf8" else "Invalid JSON: " + str(parsed.get("message", ""))
		return null
	return validate(type_name, parsed.value)

# Validates an already parsed value and normalizes it in place.
func validate(type_name: String, value: Variant) -> Variant:
	error = _validator.validate(type_name, value)
	return _validator.result if error.is_empty() else null

func read_file(type_name: String, path: String) -> Variant:
	var file := FileAccess.open(path, FileAccess.READ)
	if file == null:
		error = "Cannot read %s (error %d)" % [path, FileAccess.get_open_error()]
		return null
	return decode_bytes(type_name, file.get_buffer(file.get_length()))
