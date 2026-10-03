extends RefCounted

var schema: Dictionary
var _nodes := 0
var _error := ""

func _init(wire_schema: Dictionary = {}) -> void:
	schema = wire_schema

func validate(type_name: String, value: Variant) -> String:
	_nodes = 0; _error = ""
	_message(type_name, value, "$", 0)
	return _error

func _fail(path: String, expected: String) -> void:
	if _error.is_empty():
		_error = "Invalid ProtoJSON at %s: expected %s" % [path, expected]

func _budget(path: String, depth: int) -> bool:
	_nodes += 1
	if depth > 64 or _nodes > 100000:
		_fail(path, "bounded depth/size")
	return _error.is_empty()

static func integer_string(value: Variant, signed_value: bool, bits: int = 64) -> bool:
	if not value is String or value.is_empty() or value == "-0":
		return false
	var negative: bool = value.begins_with("-")
	if negative and not signed_value:
		return false
	var digits: String = value.substr(1) if negative else value
	if digits.is_empty() or (digits.length() > 1 and digits.begins_with("0")):
		return false
	for char in digits:
		if char < "0" or char > "9":
			return false
	var maximum := "18446744073709551615"
	if signed_value:
		maximum = "9223372036854775808" if negative else "9223372036854775807"
	if bits == 32:
		maximum = ("2147483648" if negative else "2147483647") if signed_value else "4294967295"
	return digits.length() < maximum.length() or (digits.length() == maximum.length() and digits <= maximum)

func _message(type_name: String, value: Variant, path: String, depth: int) -> void:
	if not _budget(path, depth):
		return
	if type_name.begins_with("google.protobuf."):
		var name := type_name.trim_prefix("google.protobuf.")
		var wrappers := {"DoubleValue": "double", "FloatValue": "float", "Int64Value": "int64", "UInt64Value": "uint64", "Int32Value": "int32", "UInt32Value": "uint32", "BoolValue": "bool", "StringValue": "string", "BytesValue": "bytes"}
		if wrappers.has(name):
			_scalar({"kind": wrappers[name]}, value, path, depth + 1); return
		if name in ["Timestamp", "Duration", "FieldMask"]:
			if not value is String or not _well_known_string(name, value):
				_fail(path, name + " string")
			return
		if name == "Value":
			_json(value, path, depth + 1); return
		if name == "Struct" or name == "ListValue":
			if (name == "Struct" and not value is Dictionary) or (name == "ListValue" and not value is Array):
				_fail(path, name); return
			_json(value, path, depth + 1); return
		if name == "Any":
			if not value is Dictionary or not value.get("@type") is String:
				_fail(path, "Any @type"); return
			var nested: String = value["@type"].get_slice("/", value["@type"].get_slice_count("/") - 1)
			if nested == type_name or not schema.messages.has(nested):
				_fail(path, "known Any type"); return
			var fields: Dictionary = value.duplicate()
			fields.erase("@type")
			if nested.begins_with("google.protobuf.") and nested != "google.protobuf.Empty":
				if fields.size() != 1 or not fields.has("value"):
					_fail(path, "Any value"); return
				_message(nested, fields.value, path, depth + 1)
			else:
				_message(nested, fields, path, depth + 1)
			return
	if not schema.get("messages", {}).has(type_name) or not value is Dictionary:
		_fail(path, "known message object"); return
	var shape: Dictionary = schema.messages[type_name]
	for group in shape.get("oneofs", []):
		var count := 0
		for name in group:
			if value.has(name): count += 1
		if count > 1: _fail(path, "at most one oneof member")
	for name in shape.fields:
		if shape.fields[name].get("required", false) and not value.has(name): _fail(path + "." + name, "required field")
	for name in value:
		if not name is String or not shape.fields.has(name):
			_fail(path, "declared field"); return
		var field: Dictionary = shape.fields[name]
		var item: Variant = value[name]
		var next: String = path + "." + name
		if field.has("mapKey"):
			if not item is Dictionary: _fail(next, "map object"); return
			for key in item:
				var kind: String = field.mapKey
				if not key is String: _fail(next, "string map key"); return
				if kind == "bool" and key not in ["true", "false"]: _fail(next, "boolean map key")
				elif kind != "bool" and kind != "string" and not integer_string(key, kind.begins_with("s") or kind.begins_with("int"), 32 if kind.ends_with("32") else 64): _fail(next, "integer map key")
				_scalar(field, item[key], next, depth + 1)
		elif field.get("list", false):
			if not item is Array: _fail(next, "array"); return
			for element in item: _scalar(field, element, next, depth + 1)
		else:
			_scalar(field, item, next, depth + 1)

func _scalar(field: Dictionary, value: Variant, path: String, depth: int) -> void:
	if not _budget(path, depth): return
	var kind: String = field.kind
	if kind in ["message", "group"]:
		_message(field.type, value, path, depth + 1); return
	if kind == "enum":
		if field.type == "google.protobuf.NullValue" and value == null: return
		if value is String and value in schema.get("enums", {}).get(field.type, []): return
		if _integer_number(value, -2147483648.0, 2147483647.0): return
		_fail(path, "enum name or int32"); return
	if kind.ends_with("64"):
		if not integer_string(value, kind.begins_with("s") or kind == "int64"): _fail(path, kind + " decimal string")
		return
	if kind.ends_with("32"):
		var signed_value := kind.begins_with("s") or kind == "int32"
		if not _integer_number(value, -2147483648.0 if signed_value else 0.0, 2147483647.0 if signed_value else 4294967295.0): _fail(path, kind)
		return
	if kind in ["float", "double"]:
		if value is String and value in ["NaN", "Infinity", "-Infinity"]: return
		if not (value is int or value is float) or not is_finite(float(value)) or (kind == "float" and abs(float(value)) > 3.4028234663852886e38): _fail(path, kind)
		return
	if kind == "bool":
		if not value is bool: _fail(path, "boolean")
		return
	if kind in ["string", "bytes"]:
		if not value is String: _fail(path, "string"); return
		if kind == "bytes":
			var regex := RegEx.new()
			regex.compile("^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$")
			if regex.search(value) == null: _fail(path, "padded Base64")
		return
	_fail(path, "supported scalar")

func _integer_number(value: Variant, minimum: float, maximum: float) -> bool:
	return (value is int or value is float) and is_finite(float(value)) and float(value) == floor(float(value)) and float(value) >= minimum and float(value) <= maximum

func _json(value: Variant, path: String, depth: int) -> void:
	if not _budget(path, depth): return
	if value == null or value is String or value is bool: return
	if value is int or value is float:
		if not is_finite(float(value)): _fail(path, "finite JSON number")
		return
	if value is Array:
		for item in value: _json(item, path, depth + 1)
		return
	if value is Dictionary:
		for key in value:
			if not key is String: _fail(path, "JSON object key"); return
			_json(value[key], path, depth + 1)
		return
	_fail(path, "JSON value")

func _well_known_string(name: String, value: String) -> bool:
	var regex := RegEx.new()
	if name == "FieldMask":
		regex.compile("^(?:[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\\.[A-Za-z][A-Za-z0-9]*)*)*)?$")
		return regex.search(value) != null
	if name == "Duration":
		regex.compile("^-?(0|[1-9][0-9]*)(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s$")
		var matched := regex.search(value)
		return matched != null and matched.get_string(1).length() <= 12 and int(matched.get_string(1)) <= 315576000000
	regex.compile("^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z$")
	var matched := regex.search(value)
	if matched == null: return false
	var year := int(matched.get_string(1)); var month := int(matched.get_string(2)); var day := int(matched.get_string(3))
	if year < 1 or month < 1 or month > 12 or day < 1: return false
	var days := [31, 29 if year % 4 == 0 and (year % 100 != 0 or year % 400 == 0) else 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
	return day <= days[month - 1] and int(matched.get_string(4)) < 24 and int(matched.get_string(5)) < 60 and int(matched.get_string(6)) < 60
