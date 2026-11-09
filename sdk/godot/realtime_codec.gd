extends RefCounted
## Binary Protobuf <-> ProtoJSON Dictionaries for realtime channels, driven by
## the generated codecSchema (CONTRACT.codecSchema). Output equals Go
## protojson.Marshal: 64-bit integers as decimal strings, enums by name, bytes
## as Base64, default scalars omitted. GDScript ints are signed 64-bit, so
## unsigned values are handled as two 32-bit limbs where overflow could occur.

const Wire = preload("wire_validator.gd")

const MAX_DEPTH := 100
const VARINT := 0
const I64 := 1
const LEN := 2
const I32 := 5
const MASK32 := 0xFFFFFFFF
const MIN_INT64 := -9223372036854775807 - 1
const LONG_KINDS := ["int64", "uint64", "sint64", "fixed64", "sfixed64"]
const PACKABLE := ["int32", "int64", "uint32", "uint64", "sint32", "sint64", "bool", "enum", "fixed32", "sfixed32", "float", "fixed64", "sfixed64", "double"]
const WRAPPERS := {"google.protobuf.DoubleValue": "double", "google.protobuf.FloatValue": "float", "google.protobuf.Int64Value": "int64", "google.protobuf.UInt64Value": "uint64", "google.protobuf.Int32Value": "int32", "google.protobuf.UInt32Value": "uint32", "google.protobuf.BoolValue": "bool", "google.protobuf.StringValue": "string", "google.protobuf.BytesValue": "bytes"}

var _messages := {}
var _by_number := {}
var _enum_numbers := {}
var _enum_names := {}
var error := ""

func _init(codec_schema: Dictionary) -> void:
	for type_name in codec_schema.get("messages", {}):
		var fields: Array = codec_schema.messages[type_name]
		_messages[type_name] = fields
		var numbers := {}
		for field in fields:
			numbers[int(field.no)] = field
		_by_number[type_name] = numbers
	for enum_name in codec_schema.get("enums", {}):
		var numbers := {}
		var names := {}
		for pair in codec_schema.enums[enum_name]:
			numbers[pair[0]] = int(pair[1])
			if not names.has(int(pair[1])):
				names[int(pair[1])] = pair[0]
		_enum_numbers[enum_name] = numbers
		_enum_names[enum_name] = names

func _fail(message: String) -> void:
	if error.is_empty():
		error = message

# ---- 64-bit helpers ----

## Unsigned decimal text of 64 bits stored in a signed int.
static func u64_string(value: int) -> String:
	if value >= 0:
		return str(value)
	var hi := (value >> 32) & MASK32
	var lo := value & MASK32
	var digits := ""
	while hi != 0 or lo != 0:
		var rest := hi % 10
		hi = hi / 10
		var low := (rest << 32) | lo
		digits = str(low % 10) + digits
		lo = low / 10
	return digits

## Parses canonical decimal text (signed or unsigned) into 64 bits.
static func parse_u64(text: String) -> int:
	var negative := text.begins_with("-")
	var hi := 0
	var lo := 0
	for index in range(1 if negative else 0, text.length()):
		var low := lo * 10 + text.unicode_at(index) - 48
		lo = low & MASK32
		hi = (hi * 10 + (low >> 32)) & MASK32
	var bits: int
	if hi >= 0x80000000:
		bits = (hi - 0x100000000) * 0x100000000 + lo
	else:
		bits = hi * 0x100000000 + lo
	if negative:
		if bits == MIN_INT64:
			return bits
		return -bits
	return bits

static func _logical_shift(value: int, bits: int) -> int:
	if bits == 0 or value >= 0:
		return value >> bits
	return ((value >> 1) & 0x7FFFFFFFFFFFFFFF) >> (bits - 1)

# ---- writer ----

static func _varint(out: PackedByteArray, value: int) -> void:
	for i in 10:
		var b := value & 0x7f
		value = _logical_shift(value, 7)
		if value == 0:
			out.append(b)
			return
		out.append(b | 0x80)

static func _tag(out: PackedByteArray, number: int, wire: int) -> void:
	_varint(out, number * 8 + wire)

static func _fixed32(out: PackedByteArray, value: int) -> void:
	for i in 4:
		out.append((value >> (8 * i)) & 0xff)

static func _fixed64(out: PackedByteArray, value: int) -> void:
	for i in 8:
		out.append(_logical_shift(value, 8 * i) & 0xff)

static func _bytes(out: PackedByteArray, data: PackedByteArray) -> void:
	_varint(out, data.size())
	out.append_array(data)

static func wire_type(kind: String) -> int:
	match kind:
		"fixed64", "sfixed64", "double": return I64
		"fixed32", "sfixed32", "float": return I32
		"string", "bytes", "message": return LEN
	return VARINT

## Encodes canonical ProtoJSON (a normalizing wire_validator result).
func encode(type_name: String, value: Variant) -> PackedByteArray:
	error = ""
	var out := PackedByteArray()
	_write_message(out, type_name, value, 0)
	return out

func _write_message(out: PackedByteArray, type_name: String, value: Variant, depth: int) -> void:
	if depth > MAX_DEPTH: _fail("message too deep"); return
	value = _from_special(type_name, value)
	if not value is Dictionary or not _messages.has(type_name):
		_fail(type_name + ": expected a known message object"); return
	for field in _messages[type_name]:
		if not value.has(field.name) or value[field.name] == null:
			continue
		var item: Variant = value[field.name]
		var kind: String = field.kind
		var type: String = field.get("type", "")
		if field.has("mapKey"):
			for key in item:
				var entry := PackedByteArray()
				_write_scalar(entry, 1, field.mapKey, "", _map_key(field.mapKey, key), depth + 1)
				_write_scalar(entry, 2, kind, type, item[key], depth + 1)
				_tag(out, field.no, LEN)
				_bytes(out, entry)
		elif field.get("list", false):
			if item.is_empty():
				continue
			if field.get("packed", false) and kind in PACKABLE:
				var packed := PackedByteArray()
				for element in item:
					_write_value(packed, kind, type, element, depth + 1)
				_tag(out, field.no, LEN)
				_bytes(out, packed)
			else:
				for element in item:
					_write_scalar(out, field.no, kind, type, element, depth + 1)
		elif field.get("presence", false) or kind == "message" or not _is_default(field, item):
			_write_scalar(out, field.no, kind, type, item, depth + 1)

static func _map_key(kind: String, key: Variant) -> Variant:
	var text := str(key)
	if kind == "bool": return text == "true"
	if kind == "string" or kind in LONG_KINDS: return text
	return text.to_int()

func _is_default(field: Dictionary, value: Variant) -> bool:
	match field.kind:
		"string", "bytes": return value is String and value.is_empty()
		"bool": return value is bool and not value
		"enum":
			if value is int or value is float: return int(value) == 0
			return value is String and _enum_numbers.get(field.get("type", ""), {}).get(value, -1) == 0
	if value is String: return value == "0"
	if value is int: return value == 0
	if value is float: return value == 0.0 and not signbit(value)
	return false

static func signbit(value: float) -> bool:
	var bytes := PackedByteArray()
	bytes.resize(8)
	bytes.encode_double(0, value)
	return (bytes[7] & 0x80) != 0

func _write_scalar(out: PackedByteArray, number: int, kind: String, type: String, value: Variant, depth: int) -> void:
	_tag(out, number, wire_type(kind))
	_write_value(out, kind, type, value, depth)

static func _integer(value: Variant) -> int:
	if value is String: return parse_u64(value)
	return int(value)

static func _real(value: Variant) -> float:
	if value is String:
		match value:
			"NaN": return NAN
			"Infinity": return INF
			"-Infinity": return -INF
	return float(value)

func _write_value(out: PackedByteArray, kind: String, type: String, value: Variant, depth: int) -> void:
	match kind:
		"message":
			var body := PackedByteArray()
			_write_message(body, type, value, depth)
			_bytes(out, body)
		"string": _bytes(out, str(value).to_utf8_buffer())
		"bytes": _bytes(out, Marshalls.base64_to_raw(value))
		"bool": out.append(1 if value else 0)
		"enum":
			var number: Variant = int(value) if value is int or value is float else _enum_numbers.get(type, {}).get(value)
			if number == null: _fail("unknown enum value " + str(value)); return
			_varint(out, number)
		"int32", "int64", "uint32", "uint64": _varint(out, _integer(value))
		"sint32":
			var n := int(value)
			_varint(out, ((n << 1) ^ (n >> 31)) & MASK32)
		"sint64":
			var n := _integer(value)
			_varint(out, (n << 1) ^ (n >> 63))
		"fixed32", "sfixed32": _fixed32(out, _integer(value))
		"fixed64", "sfixed64": _fixed64(out, _integer(value))
		"float":
			var bytes := PackedByteArray()
			bytes.resize(4)
			bytes.encode_float(0, _real(value))
			out.append_array(bytes)
		"double":
			var bytes := PackedByteArray()
			bytes.resize(8)
			bytes.encode_double(0, _real(value))
			out.append_array(bytes)
		_: _fail("unsupported kind " + kind)

func _from_special(type_name: String, value: Variant) -> Variant:
	if not type_name.begins_with("google.protobuf."):
		return value
	if type_name == "google.protobuf.Timestamp":
		return _parse_timestamp(str(value))
	if type_name == "google.protobuf.Duration":
		return _parse_duration(str(value))
	if WRAPPERS.has(type_name):
		return {"value": value}
	return value

# ---- reader ----

class Reader extends RefCounted:
	var data: PackedByteArray
	var pos: int
	var end: int
	var error := ""
	func _init(bytes: PackedByteArray, start: int, stop: int) -> void:
		data = bytes; pos = start; end = stop
	func fail(message: String) -> void:
		if error.is_empty(): error = message
		pos = end
	func varint() -> int:
		var result := 0
		for i in 10:
			if pos >= end:
				fail("truncated varint"); return 0
			var b := data[pos]
			pos += 1
			if i == 9 and b > 1:
				fail("varint overflows 64 bits"); return 0
			if i == 9:
				if b == 1: result |= MIN_INT64
			else:
				result |= (b & 0x7f) << (7 * i)
			if b < 0x80:
				return result
		fail("varint too long")
		return 0
	func advance(count: int) -> bool:
		if count < 0 or pos + count > end:
			fail("truncated field"); return false
		pos += count
		return true
	func length() -> Array:
		var count := varint()
		if count < 0 or count > 0x7fffffff:
			fail("length out of range"); return [end, end]
		var start := pos
		if not advance(count): return [end, end]
		return [start, pos]
	func skip(wire: int, depth: int) -> void:
		match wire:
			0: varint()
			1: advance(8)
			2: length()
			5: advance(4)
			3:
				if depth > 100: fail("message too deep"); return
				while error.is_empty():
					var tag := varint()
					if tag & 7 == 4: return
					skip(tag & 7, depth + 1)
			_: fail("invalid wire type")

## Decodes binary Protobuf into what Go protojson.Marshal would produce.
## Returns null and sets error on malformed input.
func decode(type_name: String, bytes: PackedByteArray) -> Variant:
	error = ""
	var reader := Reader.new(bytes, 0, bytes.size())
	var result := {}
	_read_message(type_name, reader, 0, result)
	if not reader.error.is_empty(): _fail(reader.error)
	return null if not error.is_empty() else result

func _read_message(type_name: String, r: Reader, depth: int, into: Dictionary) -> void:
	if depth > MAX_DEPTH: r.fail("message too deep"); return
	if not _by_number.has(type_name): r.fail("unknown message type " + type_name); return
	var fields: Dictionary = _by_number[type_name]
	while r.pos < r.end and r.error.is_empty() and error.is_empty():
		var tag := r.varint()
		var number := _logical_shift(tag, 3)
		var wire := tag & 7
		if number == 0 or number > 0x7fffffff: r.fail("invalid field number"); return
		if not fields.has(number):
			r.skip(wire, depth)
			continue
		var field: Dictionary = fields[number]
		var name: String = field.name
		var kind: String = field.kind
		var type: String = field.get("type", "")
		if field.has("mapKey"):
			if wire != LEN: r.fail("wrong wire type"); return
			var span := r.length()
			var entry := Reader.new(r.data, span[0], span[1])
			var key: Variant = null
			var value: Variant = null
			while entry.pos < entry.end and entry.error.is_empty():
				var entry_tag := entry.varint()
				var entry_number := _logical_shift(entry_tag, 3)
				if entry_number == 1: key = _read_value(entry, entry_tag & 7, field.mapKey, "", depth + 1)
				elif entry_number == 2: value = _read_value(entry, entry_tag & 7, kind, type, depth + 1)
				else: entry.skip(entry_tag & 7, depth)
			if not entry.error.is_empty(): r.fail(entry.error); return
			if not into.get(name) is Dictionary: into[name] = {}
			var key_text: String
			if key == null: key_text = "false" if field.mapKey == "bool" else ("" if field.mapKey == "string" else "0")
			elif key is bool: key_text = "true" if key else "false"
			else: key_text = str(key)
			into[name][key_text] = value if value != null else _default_value(field)
			continue
		if field.get("list", false):
			if not into.get(name) is Array: into[name] = []
			if wire == LEN and kind in PACKABLE:
				var span := r.length()
				var packed := Reader.new(r.data, span[0], span[1])
				while packed.pos < packed.end and packed.error.is_empty():
					into[name].append(_read_value(packed, wire_type(kind), kind, type, depth + 1))
				if not packed.error.is_empty(): r.fail(packed.error); return
			else:
				into[name].append(_read_value(r, wire, kind, type, depth + 1))
			continue
		if field.get("oneof", 0) != 0:
			for other in fields.values():
				if other.get("oneof", 0) == field.oneof and other.name != name:
					into.erase(other.name)
		if kind == "message" and wire == LEN and into.get(name) is Dictionary and not _is_special(type):
			var span := r.length()
			var nested := Reader.new(r.data, span[0], span[1])
			_read_message(type, nested, depth + 1, into[name])
			if not nested.error.is_empty(): r.fail(nested.error)
			continue
		into[name] = _read_value(r, wire, kind, type, depth + 1)
	for field in fields.values():
		if into.has(field.name) and not field.get("list", false) and not field.has("mapKey") and not field.get("presence", false) and field.kind != "message" and _is_default(field, into[field.name]):
			into.erase(field.name)

static func _is_special(type_name: String) -> bool:
	return type_name in ["google.protobuf.Timestamp", "google.protobuf.Duration", "google.protobuf.Empty"] or WRAPPERS.has(type_name)

func _default_value(field: Dictionary) -> Variant:
	match field.kind:
		"message": return _to_special(field.get("type", ""), {})
		"string", "bytes": return ""
		"bool": return false
		"enum": return _enum_names.get(field.get("type", ""), {}).get(0, 0)
	return "0" if field.kind in LONG_KINDS else 0

static func _real_json(value: float) -> Variant:
	if is_nan(value): return "NaN"
	if is_inf(value): return "Infinity" if value > 0 else "-Infinity"
	return value

func _read_value(r: Reader, wire: int, kind: String, type: String, depth: int) -> Variant:
	if wire != wire_type(kind):
		r.fail("wrong wire type %d for %s" % [wire, kind]); return null
	match kind:
		"message":
			var span := r.length()
			var nested := Reader.new(r.data, span[0], span[1])
			var message := {}
			_read_message(type, nested, depth, message)
			if not nested.error.is_empty(): r.fail(nested.error); return null
			return _to_special(type, message)
		"string":
			var span := r.length()
			var decoded: Dictionary = Wire.decode_utf8(r.data.slice(span[0], span[1]))
			if decoded.has("error"): r.fail("invalid UTF-8"); return null
			return decoded.text
		"bytes":
			var span := r.length()
			return "" if span[0] == span[1] else Marshalls.raw_to_base64(r.data.slice(span[0], span[1]))
		"bool": return r.varint() != 0
		"enum":
			var n := _int32(r.varint())
			return _enum_names.get(type, {}).get(n, n)
		"int32": return _int32(r.varint())
		"uint32": return r.varint() & MASK32
		"sint32":
			var n := r.varint() & MASK32
			return _int32(_logical_shift(n, 1) ^ -(n & 1))
		"int64": return str(r.varint())
		"uint64": return u64_string(r.varint())
		"sint64":
			var n := r.varint()
			return str(_logical_shift(n, 1) ^ -(n & 1))
		"fixed32":
			if not r.advance(4): return null
			return r.data.decode_u32(r.pos - 4)
		"sfixed32":
			if not r.advance(4): return null
			return r.data.decode_s32(r.pos - 4)
		"fixed64":
			if not r.advance(8): return null
			return u64_string(r.data.decode_s64(r.pos - 8))
		"sfixed64":
			if not r.advance(8): return null
			return str(r.data.decode_s64(r.pos - 8))
		"float":
			if not r.advance(4): return null
			return _real_json(r.data.decode_float(r.pos - 4))
		"double":
			if not r.advance(8): return null
			return _real_json(r.data.decode_double(r.pos - 8))
	r.fail("unsupported kind " + kind)
	return null

static func _int32(value: int) -> int:
	var n := value & MASK32
	return n - 0x100000000 if n >= 0x80000000 else n

func _to_special(type_name: String, value: Dictionary) -> Variant:
	if not type_name.begins_with("google.protobuf."):
		return value
	var seconds := parse_u64(value.get("seconds", "0"))
	var nanos := int(value.get("nanos", 0))
	if type_name == "google.protobuf.Timestamp":
		return _format_timestamp(seconds, nanos)
	if type_name == "google.protobuf.Duration":
		return _format_duration(seconds, nanos)
	if WRAPPERS.has(type_name):
		return value["value"] if value.has("value") else _default_value({"kind": WRAPPERS[type_name]})
	return value

# ---- Timestamp / Duration ----

static func _floor_div(a: int, b: int) -> int:
	return a / b if a >= 0 else -((-a + b - 1) / b)

static func _civil_days(year: int, month: int, day: int) -> int:
	var y := year + 400 - (1 if month <= 2 else 0)
	var era := _floor_div(y, 400)
	var yoe := y - era * 400
	var doy := (153 * ((month + 9) % 12) + 2) / 5 + day - 1
	return era * 146097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719468 - 146097

static func _civil_from_days(days: int) -> Array:
	var z := days + 719468
	var era := _floor_div(z, 146097)
	var doe := z - era * 146097
	var yoe := (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365
	var doy := doe - (365 * yoe + yoe / 4 - yoe / 100)
	var mp := (5 * doy + 2) / 153
	var day := doy - (153 * mp + 2) / 5 + 1
	var month := mp + 3 if mp < 10 else mp - 9
	return [yoe + era * 400 + (1 if month <= 2 else 0), month, day]

static func _fraction(nanos: int) -> String:
	if nanos == 0: return ""
	var digits := "%09d" % nanos
	if nanos % 1000000 == 0: return "." + digits.substr(0, 3)
	if nanos % 1000 == 0: return "." + digits.substr(0, 6)
	return "." + digits

func _parse_timestamp(text: String) -> Variant:
	var regex := RegEx.create_from_string("^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:[.,]([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$")
	var m := regex.search(text)
	if m == null: _fail("invalid timestamp"); return {}
	var seconds := _civil_days(m.get_string(1).to_int(), m.get_string(2).to_int(), m.get_string(3).to_int()) * 86400 + m.get_string(4).to_int() * 3600 + m.get_string(5).to_int() * 60 + m.get_string(6).to_int()
	if not m.get_string(8).is_empty():
		seconds -= (-1 if m.get_string(8) == "-" else 1) * (m.get_string(9).to_int() * 3600 + m.get_string(10).to_int() * 60)
	var nanos := (m.get_string(7) + "000000000").substr(0, 9).to_int() if not m.get_string(7).is_empty() else 0
	return {"seconds": str(seconds), "nanos": nanos}

func _format_timestamp(seconds: int, nanos: int) -> String:
	if seconds < -62135596800 or seconds > 253402300799 or nanos < 0 or nanos > 999999999:
		_fail("timestamp out of range"); return ""
	var days := _floor_div(seconds, 86400)
	var rest := seconds - days * 86400
	var date := _civil_from_days(days)
	return "%04d-%02d-%02dT%02d:%02d:%02d%sZ" % [date[0], date[1], date[2], rest / 3600, (rest / 60) % 60, rest % 60, _fraction(nanos)]

func _parse_duration(text: String) -> Variant:
	var regex := RegEx.create_from_string("^(-)?([0-9]+)(?:\\.([0-9]{1,9}))?s$")
	var m := regex.search(text)
	if m == null: _fail("invalid duration"); return {}
	var negative := not m.get_string(1).is_empty()
	var seconds := m.get_string(2).to_int()
	var nanos := (m.get_string(3) + "000000000").substr(0, 9).to_int() if not m.get_string(3).is_empty() else 0
	return {"seconds": str(-seconds if negative else seconds), "nanos": -nanos if negative else nanos}

func _format_duration(seconds: int, nanos: int) -> String:
	if seconds < -315576000000 or seconds > 315576000000 or nanos < -999999999 or nanos > 999999999 or (seconds > 0 and nanos < 0) or (seconds < 0 and nanos > 0):
		_fail("duration out of range"); return ""
	var negative := seconds < 0 or nanos < 0
	return ("-" if negative else "") + str(absi(seconds)) + _fraction(absi(nanos)) + "s"
