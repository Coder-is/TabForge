extends RefCounted

# JSON budgets shared by every TabForge runtime. Depth counts container nesting
# (the root value is depth 0); nodes count every JSON value, not object keys.
# Network messages are also capped at 1 MiB; data bundle files are not, so data
# loaders use the larger DATA_MAX_NODES budget.
const MAX_DEPTH := 64
const PROTOCOL_MAX_NODES := 100000
const DATA_MAX_NODES := 10000000

const WRAPPERS := {"DoubleValue": "double", "FloatValue": "float", "Int64Value": "int64", "UInt64Value": "uint64", "Int32Value": "int32", "UInt32Value": "uint32", "BoolValue": "bool", "StringValue": "string", "BytesValue": "bytes"}
const INTEGER_KINDS := {"int32": [true, 32], "sint32": [true, 32], "sfixed32": [true, 32], "uint32": [false, 32], "fixed32": [false, 32], "int64": [true, 64], "sint64": [true, 64], "sfixed64": [true, 64], "uint64": [false, 64], "fixed64": [false, 64]}
const BASE64 := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
const BASE64_URL := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

# A JSON number whose exact source text matters (1.0, 1e2, 18446744073709551615).
# Only parse_json(..., keep_raw = true) creates these; a normalizing validator
# replaces every one of them.
class RawNumber extends RefCounted:
	var text: String
	var value: float
	func _init(source: String, number: float) -> void:
		text = source
		value = number

var schema: Dictionary
var max_nodes: int
var normalize: bool
# Validated value; canonical ProtoJSON when normalize is set.
var result: Variant
var _nodes := 0
var _error := ""

func _init(wire_schema: Dictionary = {}, node_budget: int = PROTOCOL_MAX_NODES, normalize_values: bool = false) -> void:
	schema = wire_schema
	max_nodes = node_budget
	normalize = normalize_values

func validate(type_name: String, value: Variant) -> String:
	_nodes = 0; _error = ""; result = null
	var output: Variant = _message(type_name, value, "$", 0, false, false)
	if _error.is_empty():
		result = output if normalize else value
	return _error

func _fail(path: String, expected: String) -> void:
	if _error.is_empty():
		_error = "Invalid ProtoJSON at %s: expected %s" % [path, expected]

func _budget(path: String, depth: int) -> bool:
	_nodes += 1
	if depth > MAX_DEPTH or _nodes > max_nodes:
		_fail(path, "bounded depth/size")
	return _error.is_empty()

# Canonical decimal string (the form 64-bit integers are emitted in).
static func integer_string(value: Variant, signed_value: bool, bits: int = 64) -> bool:
	if not value is String or value.is_empty() or value == "-0":
		return false
	var negative: bool = value.begins_with("-")
	var digits: String = value.substr(1) if negative else value
	if digits.is_empty() or (digits.length() > 1 and digits.begins_with("0")) or not _all_digits(digits):
		return false
	return _in_range(digits, negative, signed_value, bits)

static func _all_digits(text: String) -> bool:
	for index in text.length():
		var code := text.unicode_at(index)
		if code < 48 or code > 57:
			return false
	return true

static func _in_range(digits: String, negative: bool, signed_value: bool, bits: int) -> bool:
	if negative and not signed_value:
		return false
	var maximum := "18446744073709551615"
	if signed_value:
		maximum = "9223372036854775808" if negative else "9223372036854775807"
	if bits == 32:
		maximum = ("2147483648" if negative else "2147483647") if signed_value else "4294967295"
	return digits.length() < maximum.length() or (digits.length() == maximum.length() and digits <= maximum)

# [negative, integer digits, fraction digits, exponent with sign] of a complete
# JSON number literal, or [] when text is not exactly one.
static func _number_parts(text: String) -> Array:
	var length := text.length()
	var pos := 0
	var negative := false
	if pos < length and text.unicode_at(pos) == 45:
		negative = true; pos += 1
	var start := pos
	if pos < length and text.unicode_at(pos) == 48:
		pos += 1
	elif pos < length and text.unicode_at(pos) >= 49 and text.unicode_at(pos) <= 57:
		while pos < length and text.unicode_at(pos) >= 48 and text.unicode_at(pos) <= 57: pos += 1
	else:
		return []
	var integer := text.substr(start, pos - start)
	var fraction := ""
	if pos < length and text.unicode_at(pos) == 46:
		pos += 1
		start = pos
		while pos < length and text.unicode_at(pos) >= 48 and text.unicode_at(pos) <= 57: pos += 1
		if pos == start: return []
		fraction = text.substr(start, pos - start)
	var exponent := ""
	if pos < length and (text.unicode_at(pos) == 101 or text.unicode_at(pos) == 69):
		pos += 1
		start = pos
		if pos < length and (text.unicode_at(pos) == 43 or text.unicode_at(pos) == 45): pos += 1
		var digits_start := pos
		while pos < length and text.unicode_at(pos) >= 48 and text.unicode_at(pos) <= 57: pos += 1
		if pos == digits_start: return []
		exponent = text.substr(start, pos - start)
	if pos != length:
		return []
	return [negative, integer, fraction, exponent]

# Go protojson integer input: exact value of JSON number text after shifting
# the exponent (1.0, 1e2 and 100e-2 are integers; 1.5 and 1e-2 are not),
# including Go's 20-digit cap for positive exponents. Canonical decimal or null.
static func proto_integer(text: String, signed_value: bool, bits: int) -> Variant:
	var parts := _number_parts(text)
	if parts.is_empty():
		return null
	var integer: String = parts[1]
	if integer == "0":
		integer = ""
	var fraction: String = parts[2]
	fraction = fraction.rstrip("0")
	if integer.is_empty() and fraction.is_empty():
		return "0"
	var exponent := 0
	var exponent_text: String = parts[3]
	if not exponent_text.is_empty():
		var sign := 1
		if exponent_text.begins_with("+") or exponent_text.begins_with("-"):
			sign = -1 if exponent_text.begins_with("-") else 1
			exponent_text = exponent_text.substr(1)
		exponent_text = exponent_text.lstrip("0")
		if exponent_text.length() > 10:
			return null
		exponent = sign * (exponent_text.to_int() if not exponent_text.is_empty() else 0)
		if exponent < -2147483648 or exponent > 2147483647:
			return null
	var digits := ""
	if exponent >= 0:
		if fraction.length() > exponent or integer.length() + exponent > 20:
			return null
		digits = integer + fraction + "0".repeat(exponent - fraction.length())
	else:
		var index := integer.length() + exponent
		if not fraction.is_empty() or index < 0:
			return null
		for position in range(index, integer.length()):
			if integer.unicode_at(position) != 48:
				return null
		digits = integer.substr(0, index)
	digits = digits.lstrip("0")
	if digits.is_empty():
		digits = "0"
	var negative: bool = parts[0] and digits != "0"
	if not _in_range(digits, negative, signed_value, bits):
		return null
	return ("-" + digits) if negative else digits

# Go strconv.ParseInt/ParseUint(key, 10, bits) for map keys. Canonical key or null.
static func map_key(key: String, kind: String) -> Variant:
	if kind == "string":
		return key
	if kind == "bool":
		return key if key == "true" or key == "false" else null
	if not INTEGER_KINDS.has(kind):
		return null
	var signed_value: bool = INTEGER_KINDS[kind][0]
	var bits: int = INTEGER_KINDS[kind][1]
	var sign := ""
	var digits := key
	if key.begins_with("+") or key.begins_with("-"):
		if not signed_value:
			return null
		sign = key.left(1)
		digits = key.substr(1)
	if digits.is_empty() or not _all_digits(digits):
		return null
	digits = digits.lstrip("0")
	if digits.is_empty():
		digits = "0"
	var negative := sign == "-" and digits != "0"
	if not _in_range(digits, negative, signed_value, bits):
		return null
	return ("-" + digits) if negative else digits

# Go protojson bytes input: URL alphabet when "-" or "_" occurs, padded exactly
# when the length is a multiple of 4, CR/LF ignored, unused bits ignored.
# Returns canonical padded standard Base64, or null.
static func proto_bytes(value: String) -> Variant:
	var url := value.contains("-") or value.contains("_")
	var padded := value.length() % 4 == 0
	var text := value.replace("\r", "").replace("\n", "")
	var pads := 0
	if padded:
		if text.length() % 4 != 0:
			return null
		if text.ends_with("=="):
			pads = 2
		elif text.ends_with("="):
			pads = 1
	var body := text.substr(0, text.length() - pads)
	if body.length() % 4 == 1:
		return null
	var alphabet := BASE64_URL if url else BASE64
	for index in body.length():
		if alphabet.find(body[index]) < 0:
			return null
	body = body.replace("-", "+").replace("_", "/")
	var rest := body.length() % 4
	if rest != 0:
		var last := BASE64.find(body[body.length() - 1]) & (0x30 if rest == 2 else 0x3c)
		body = body.left(-1) + BASE64[last] + ("==" if rest == 2 else "=")
	return body

# Exact decimal text of an integral float, or text that is never an integer.
static func _float_text(value: float) -> String:
	if not is_finite(value) or value != floor(value):
		return "0.5"
	var magnitude := absf(value)
	var digits := ""
	if magnitude < 9.0e18:
		digits = str(int(magnitude))
	elif magnitude < 18446744073709551616.0:
		# Above 2^53 floats are even, so halving is exact; double the decimal digits.
		var half := str(int(magnitude / 2.0))
		var carry := 0
		for index in range(half.length() - 1, -1, -1):
			var doubled := (half.unicode_at(index) - 48) * 2 + carry
			digits = str(doubled % 10) + digits
			carry = 1 if doubled >= 10 else 0
		if carry > 0:
			digits = "1" + digits
	else:
		return "0.5"
	return ("-" + digits) if value < 0 and digits != "0" else digits

static func _number_text(value: Variant) -> String:
	if value is RawNumber:
		return (value as RawNumber).text
	if value is int:
		return str(value)
	if value is float:
		return _float_text(value)
	return ""

static func _nullable(field: Dictionary) -> bool:
	var kind: String = field.get("kind", "")
	var type_name: String = field.get("type", "")
	return (kind == "message" and type_name == "google.protobuf.Value") or (kind == "enum" and type_name == "google.protobuf.NullValue")

# JSON null means "field absent" except where null is itself a value.
static func _present(fields: Dictionary, shapes: Dictionary, name: String) -> bool:
	if not fields.has(name):
		return false
	if typeof(fields[name]) != TYPE_NIL or not shapes.has(name):
		return true
	return _nullable(shapes[name])

func _message(type_name: String, value: Variant, path: String, depth: int, counted: bool, skip_type: bool) -> Variant:
	if not counted and not _budget(path, depth):
		return null
	if type_name.begins_with("google.protobuf."):
		var known := _well_known(type_name, value, path, depth)
		if not _error.is_empty():
			return null
		if not known.is_empty():
			return known[0]
	var messages: Dictionary = schema.get("messages", {})
	if not messages.has(type_name) or not value is Dictionary:
		_fail(path, "known message object"); return null
	var shape: Dictionary = messages[type_name]
	var shapes: Dictionary = shape.get("fields", {})
	var fields: Dictionary = value
	for group in shape.get("oneofs", []):
		var count := 0
		for name in group:
			if _present(fields, shapes, name): count += 1
		if count > 1:
			_fail(path, "at most one oneof member"); return null
	for name in shapes:
		if shapes[name].get("required", false) and not _present(fields, shapes, name):
			_fail(path + "." + name, "required field"); return null
	for name in fields.keys():
		if skip_type and name is String and name == "@type":
			continue
		if not name is String or not shapes.has(name):
			_fail(path + "." + str(name), "declared field"); return null
		var field: Dictionary = shapes[name]
		var item: Variant = fields[name]
		var next: String = path + "." + name
		if typeof(item) == TYPE_NIL and not _nullable(field):
			if normalize: fields.erase(name)
			continue
		if field.has("mapKey"):
			if not item is Dictionary:
				_fail(next, "map object"); return null
			if not _budget(next, depth + 1):
				return null
			var entries: Dictionary = item
			var kind: String = field.mapKey
			var rebuilt := {}
			for key in entries.keys():
				if not key is String:
					_fail(next, "string map key"); return null
				var canonical: Variant = map_key(key, kind)
				if typeof(canonical) == TYPE_NIL:
					_fail(next, kind + " map key"); return null
				if rebuilt.has(canonical):
					_fail(next, "unique map key"); return null
				var entry: Variant = _scalar(field, entries[key], next + "." + key, depth + 2, false)
				if not _error.is_empty():
					return null
				rebuilt[canonical] = entry
			if normalize: fields[name] = rebuilt
		elif field.get("list", false):
			if not item is Array:
				_fail(next, "array"); return null
			if not _budget(next, depth + 1):
				return null
			var elements: Array = item
			for index in elements.size():
				var element: Variant = _scalar(field, elements[index], "%s[%d]" % [next, index], depth + 2, false)
				if not _error.is_empty():
					return null
				if normalize: elements[index] = element
		else:
			var single: Variant = _scalar(field, item, next, depth + 1, false)
			if not _error.is_empty():
				return null
			if normalize: fields[name] = single
	return fields

# [value] for types with a special JSON form, [] otherwise.
func _well_known(type_name: String, value: Variant, path: String, depth: int) -> Array:
	var name := type_name.trim_prefix("google.protobuf.")
	if WRAPPERS.has(name):
		return [_scalar({"kind": WRAPPERS[name]}, value, path, depth, true)]
	if name in ["Timestamp", "Duration", "FieldMask"]:
		if not value is String or not _well_known_string(name, value):
			_fail(path, name + " string")
		return [value]
	if name == "Value":
		return [_json(value, path, depth, true)]
	if name == "Struct":
		if not value is Dictionary:
			_fail(path, "Struct"); return [null]
		var members: Dictionary = value
		for key in members.keys():
			var member: Variant = _json(members[key], path + "." + str(key), depth + 1, false)
			if not _error.is_empty():
				return [null]
			if normalize: members[key] = member
		return [value]
	if name == "ListValue":
		if not value is Array:
			_fail(path, "ListValue"); return [null]
		var items: Array = value
		for index in items.size():
			var element: Variant = _json(items[index], path, depth + 1, false)
			if not _error.is_empty():
				return [null]
			if normalize: items[index] = element
		return [value]
	if name == "Any":
		if not value is Dictionary:
			_fail(path, "Any object"); return [null]
		var wrapper: Dictionary = value
		# protojson writes an unset Any as {}.
		if wrapper.is_empty():
			return [value]
		if not wrapper.get("@type") is String:
			_fail(path, "Any @type"); return [null]
		var url: String = wrapper["@type"]
		var nested: String = url.get_slice("/", url.get_slice_count("/") - 1)
		if nested == type_name or not schema.get("messages", {}).has(nested):
			_fail(path, "known Any type"); return [null]
		var members := wrapper.size() - 1
		if nested == "google.protobuf.Empty" and members == 0:
			return [value]
		if nested.begins_with("google.protobuf."):
			if members != 1 or not wrapper.has("value"):
				_fail(path, "Any value"); return [null]
			var inner: Variant = _message(nested, wrapper["value"], path + ".value", depth + 1, false, false)
			if not _error.is_empty():
				return [null]
			if normalize: wrapper["value"] = inner
		else:
			_message(nested, wrapper, path, depth, true, true)
		return [value]
	return []

func _scalar(field: Dictionary, value: Variant, path: String, depth: int, counted: bool) -> Variant:
	var kind: String = field.get("kind", "")
	if kind == "message" or kind == "group":
		return _message(field.get("type", ""), value, path, depth, counted, false)
	if not counted and not _budget(path, depth):
		return null
	if kind == "enum":
		if field.get("type", "") == "google.protobuf.NullValue" and typeof(value) == TYPE_NIL:
			return null
		if value is String:
			if value in schema.get("enums", {}).get(field.get("type", ""), []):
				return value
		elif value is int or value is float or value is RawNumber:
			var number: Variant = proto_integer(_number_text(value), true, 32)
			if typeof(number) != TYPE_NIL:
				return str(number).to_int()
		_fail(path, "enum name or int32"); return null
	if INTEGER_KINDS.has(kind):
		var signed_value: bool = INTEGER_KINDS[kind][0]
		var bits: int = INTEGER_KINDS[kind][1]
		var text := ""
		if value is String:
			text = value
		elif value is int or value is float or value is RawNumber:
			text = _number_text(value)
		var integer: Variant = proto_integer(text, signed_value, bits)
		if typeof(integer) == TYPE_NIL:
			_fail(path, kind); return null
		if bits == 64:
			return integer
		return str(integer).to_int()
	if kind == "float" or kind == "double":
		if value is String and value in ["NaN", "Infinity", "-Infinity"]:
			return value
		var number := NAN
		if value is int or value is float:
			number = float(value)
		elif value is RawNumber:
			number = (value as RawNumber).value
		elif value is String and not _number_parts(value).is_empty():
			number = str(value).to_float()
		if not is_finite(number) or (kind == "float" and absf(number) >= 3.4028235677973366e38):
			_fail(path, kind); return null
		return number
	if kind == "bool":
		if not value is bool: _fail(path, "boolean")
		return value
	if kind == "string":
		if not value is String: _fail(path, "string")
		return value
	if kind == "bytes":
		var bytes: Variant = proto_bytes(value) if value is String else null
		if typeof(bytes) == TYPE_NIL: _fail(path, "Base64 string")
		return bytes
	_fail(path, "supported scalar"); return null

func _json(value: Variant, path: String, depth: int, counted: bool) -> Variant:
	if not counted and not _budget(path, depth):
		return null
	if typeof(value) == TYPE_NIL or value is String or value is bool or value is int:
		return value
	if value is float or value is RawNumber:
		var number: float = (value as RawNumber).value if value is RawNumber else value
		if not is_finite(number): _fail(path, "finite JSON number")
		return number
	if value is Array:
		var items: Array = value
		for index in items.size():
			var item: Variant = _json(items[index], path, depth + 1, false)
			if not _error.is_empty():
				return null
			if normalize: items[index] = item
		return value
	if value is Dictionary:
		var members: Dictionary = value
		for key in members.keys():
			if not key is String:
				_fail(path, "JSON object key"); return null
			var member: Variant = _json(members[key], path + "." + key, depth + 1, false)
			if not _error.is_empty():
				return null
			if normalize: members[key] = member
		return value
	_fail(path, "JSON value"); return null

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

# Strict UTF-8 validation before Godot's decoder, including overlong sequences.
static func decode_utf8(bytes: PackedByteArray) -> Dictionary:
	var index := 0
	while index < bytes.size():
		var first := int(bytes[index])
		var count := 1
		var codepoint := first
		var minimum := 0
		if first < 0x80:
			pass
		elif first >= 0xc2 and first <= 0xdf:
			count = 2; codepoint = first & 0x1f; minimum = 0x80
		elif first >= 0xe0 and first <= 0xef:
			count = 3; codepoint = first & 0x0f; minimum = 0x800
		elif first >= 0xf0 and first <= 0xf4:
			count = 4; codepoint = first & 0x07; minimum = 0x10000
		else:
			return {"error": "invalid_utf8"}
		if index + count > bytes.size():
			return {"error": "invalid_utf8"}
		for offset in range(1, count):
			var continuation := int(bytes[index + offset])
			if continuation < 0x80 or continuation > 0xbf:
				return {"error": "invalid_utf8"}
			codepoint = (codepoint << 6) | (continuation & 0x3f)
		if codepoint < minimum or codepoint > 0x10ffff or (codepoint >= 0xd800 and codepoint <= 0xdfff):
			return {"error": "invalid_utf8"}
		index += count
	return {"text": bytes.get_string_from_utf8()}

# Strict RFC 8259 JSON: duplicate members (also after unescaping), extensions,
# lone surrogates and trailing content are rejected. Returns {"value": ...} or
# {"error": message}. Integer literals that fit int64 become int, other numbers
# float, or RawNumber when keep_raw is set (for a normalizing validator).
static func parse_json(text: String, node_budget: int = PROTOCOL_MAX_NODES, keep_raw: bool = false) -> Dictionary:
	var parser := _Parser.new(text, MAX_DEPTH, node_budget, keep_raw)
	var value: Variant = parser.parse()
	if not parser.error.is_empty():
		return {"error": parser.error}
	return {"value": value}

# Returns {"value": ...}, {"error": "invalid_utf8"} or {"error": "invalid_json", "message": ...}.
static func parse_json_bytes(bytes: PackedByteArray, node_budget: int = PROTOCOL_MAX_NODES, keep_raw: bool = false) -> Dictionary:
	var decoded := decode_utf8(bytes)
	if decoded.has("error"):
		return decoded
	# NUL is valid UTF-8 but never valid in JSON; Godot strings would truncate there.
	if bytes.find(0) >= 0:
		return {"error": "invalid_json", "message": "NUL byte in JSON"}
	var parsed := parse_json(decoded.text, node_budget, keep_raw)
	if parsed.has("error"):
		return {"error": "invalid_json", "message": parsed.error}
	return parsed

class _Parser extends RefCounted:
	var text: String
	var length: int
	var pos := 0
	var nodes := 0
	var max_depth: int
	var max_nodes: int
	var keep_raw: bool
	var error := ""

	func _init(source: String, depth_budget: int, node_budget: int, raw_numbers: bool) -> void:
		text = source
		length = source.length()
		max_depth = depth_budget
		max_nodes = node_budget
		keep_raw = raw_numbers

	func parse() -> Variant:
		var value: Variant = _value(0)
		if not error.is_empty():
			return null
		_space()
		if pos != length:
			return _fail("Unexpected trailing content")
		return value

	func _fail(message: String) -> Variant:
		if error.is_empty():
			error = "%s at offset %d" % [message, pos]
		return null

	func _peek() -> int:
		return text.unicode_at(pos) if pos < length else -1

	func _digit() -> bool:
		var code := _peek()
		return code >= 48 and code <= 57

	func _space() -> void:
		while pos < length:
			var code := text.unicode_at(pos)
			if code != 32 and code != 9 and code != 10 and code != 13:
				return
			pos += 1

	func _value(depth: int) -> Variant:
		_space()
		nodes += 1
		if depth > max_depth or nodes > max_nodes:
			return _fail("JSON exceeds depth %d or %d values" % [max_depth, max_nodes])
		var code := _peek()
		if code == 34:
			return _string()
		if code == 123:
			return _object(depth)
		if code == 91:
			return _array(depth)
		if code == 116 and text.substr(pos, 4) == "true":
			pos += 4; return true
		if code == 102 and text.substr(pos, 5) == "false":
			pos += 5; return false
		if code == 110 and text.substr(pos, 4) == "null":
			pos += 4; return null
		return _number()

	func _object(depth: int) -> Variant:
		pos += 1
		var result := {}
		_space()
		if _peek() == 125:
			pos += 1
			return result
		while true:
			_space()
			if _peek() != 34:
				return _fail("Expected member name")
			var key: Variant = _string()
			if not error.is_empty():
				return null
			if result.has(key):
				return _fail("Duplicate JSON member " + str(key))
			_space()
			if _peek() != 58:
				return _fail("Expected ':'")
			pos += 1
			var item: Variant = _value(depth + 1)
			if not error.is_empty():
				return null
			result[key] = item
			_space()
			var next := _peek()
			pos += 1
			if next == 125:
				return result
			if next != 44:
				return _fail("Expected ',' or '}'")
		return null

	func _array(depth: int) -> Variant:
		pos += 1
		var result := []
		_space()
		if _peek() == 93:
			pos += 1
			return result
		while true:
			var item: Variant = _value(depth + 1)
			if not error.is_empty():
				return null
			result.append(item)
			_space()
			var next := _peek()
			pos += 1
			if next == 93:
				return result
			if next != 44:
				return _fail("Expected ',' or ']'")
		return null

	func _hex() -> int:
		if pos + 4 > length:
			return -1
		var code := 0
		for index in 4:
			var digit := text.unicode_at(pos + index)
			if digit >= 48 and digit <= 57:
				code = code * 16 + digit - 48
			elif digit >= 97 and digit <= 102:
				code = code * 16 + digit - 87
			elif digit >= 65 and digit <= 70:
				code = code * 16 + digit - 55
			else:
				return -1
		pos += 4
		return code

	func _string() -> Variant:
		pos += 1
		var out := ""
		var start := pos
		while pos < length:
			var code := text.unicode_at(pos)
			if code == 34:
				out += text.substr(start, pos - start)
				pos += 1
				return out
			if code < 32:
				return _fail("Control character in string")
			if code != 92:
				pos += 1
				continue
			out += text.substr(start, pos - start)
			pos += 1
			if pos >= length:
				break
			var escape := text.unicode_at(pos)
			pos += 1
			match escape:
				34: out += "\""
				92: out += "\\"
				47: out += "/"
				98: out += String.chr(8)
				102: out += String.chr(12)
				110: out += "\n"
				114: out += "\r"
				116: out += "\t"
				117:
					var high := _hex()
					if high < 0:
						return _fail("Invalid \\u escape")
					if high >= 0xdc00 and high <= 0xdfff:
						return _fail("Lone surrogate escape")
					if high >= 0xd800 and high <= 0xdbff:
						if pos + 1 >= length or text.unicode_at(pos) != 92 or text.unicode_at(pos + 1) != 117:
							return _fail("Lone surrogate escape")
						pos += 2
						var low := _hex()
						if low < 0xdc00 or low > 0xdfff:
							return _fail("Lone surrogate escape")
						high = 0x10000 + ((high - 0xd800) << 10) + (low - 0xdc00)
					out += String.chr(high)
				_:
					return _fail("Invalid escape")
			start = pos
		return _fail("Unterminated string")

	func _number() -> Variant:
		var start := pos
		if _peek() == 45:
			pos += 1
		var code := _peek()
		if code == 48:
			pos += 1
		elif code >= 49 and code <= 57:
			while _digit(): pos += 1
		else:
			return _fail("Invalid JSON value")
		var plain := true
		if _peek() == 46:
			pos += 1
			if not _digit():
				return _fail("Invalid number")
			while _digit(): pos += 1
			plain = false
		if _peek() == 101 or _peek() == 69:
			pos += 1
			if _peek() == 43 or _peek() == 45:
				pos += 1
			if not _digit():
				return _fail("Invalid number")
			while _digit(): pos += 1
			plain = false
		var raw := text.substr(start, pos - start)
		if plain and raw != "-0":
			var digits := raw.trim_prefix("-")
			if digits.length() < 19 or (digits.length() == 19 and digits <= "9223372036854775807"):
				return raw.to_int()
		var number := raw.to_float()
		if keep_raw:
			return RawNumber.new(raw, number)
		return number
