extends SceneTree

const Parser = preload("../sse_parser.gd")
const UTF8 = preload("../utf8.gd")
const Wire = preload("../wire_validator.gd")
const Client = preload("../protocol_client.gd")
const Data = preload("../data_schema.gd")
var failures := 0

func check(condition: bool, description: String) -> void:
	if not condition:
		failures += 1
		printerr(description)

func _initialize() -> void:
	var input := "\ufeff: heartbeat\r\nid: 1\revent: delta\ndata: 你好😀\r\ndata: 世界\r\n\r\n".to_utf8_buffer()
	for cut in range(input.size() + 1):
		var parser := Parser.new()
		var first: Dictionary = parser.feed(input.slice(0, cut))
		var second: Dictionary = parser.feed(input.slice(cut))
		var frames: Array = first.frames + second.frames
		check(first.error.is_empty() and second.error.is_empty(), "UTF-8 frame split %d" % cut)
		check(frames.size() == 1 and frames[0].data == "你好😀\n世界" and frames[0].id == "1", "Framing split %d" % cut)
	check(UTF8.decode(PackedByteArray([0xc0, 0x80])).has("error"), "Reject overlong UTF-8")
	check(UTF8.decode(PackedByteArray([0xed, 0xa0, 0x80])).has("error"), "Reject surrogate")
	var limited := Parser.new(); limited.maximum_bytes = 4
	check(limited.feed("data: long".to_utf8_buffer()).error == "frame_too_large", "Bound frame memory")
	check(Wire.integer_string("18446744073709551615", false), "uint64 max")
	check(not Wire.integer_string("18446744073709551616", false), "uint64 overflow")
	check(not Wire.integer_string(123, false), "Canonical int64 text is a string")
	check(Client.next_sequence("99999999999999999999") == "100000000000000000000", "Sequence without overflow")
	var validator := Wire.new({"messages": {"Test": {"fields": {"id": {"kind": "uint64"}, "text": {"kind": "string"}}}}, "enums": {}})
	check(validator.validate("Test", {"id": "18446744073709551615", "text": "你好😀"}).is_empty(), "Validate message")
	check(validator.validate("Test", {"id": 123}).is_empty(), "Go accepts uint64 as a JSON number")
	check(not validator.validate("Test", {"id": 1.5}).is_empty(), "Reject wrong scalar")
	check(not validator.validate("Test", {"unknown": true}).is_empty(), "Reject unknown field")
	check(validator.validate("google.protobuf.Timestamp", "2024-02-29T12:34:56.123456789Z").is_empty(), "Timestamp")
	check(not validator.validate("google.protobuf.Timestamp", "2025-02-29T12:34:56Z").is_empty(), "Invalid leap date")
	check(not validator.validate("google.protobuf.Duration", "315576000001s").is_empty(), "Duration range")
	_json_tests()
	_data_tests()
	print("Godot unit tests: %d failures" % failures)
	quit(0 if failures == 0 else 1)

func _json_tests() -> void:
	check(Wire.parse_json("{\"x\":1,\"x\":2}").has("error"), "Reject duplicate key")
	check(Wire.parse_json("{\"x\":1,\"\\u0078\":2}").has("error"), "Reject duplicate key after unescaping")
	for bad in ["[1,]", "{'x':1}", "{\"x\":NaN}", "{} []", "{\"x\":\"\\ud800\"}", "\"\\udc00\"", "01", "[1 2]", "\ufeff{}", "{\"a\":\"\t\"}", "{\"a\":1,}", "-", "1.", "1e", "\"\\x\"", "", " ", "[1]//"]:
		check(Wire.parse_json(bad).has("error"), "Reject invalid JSON: " + bad)
	var parsed: Dictionary = Wire.parse_json("{\"x\":[1,true,null,\"你好😀\"],\"n\":-1.2e+3,\"e\":\"\\ud83d\\ude00\\n\"}")
	check(parsed.has("value") and parsed.value.x == [1, true, null, "你好😀"] and parsed.value.n == -1200.0 and parsed.value.e == "😀\n", "Parse valid JSON")
	check(Wire.parse_json("123").value is int and Wire.parse_json("1.0").value is float and Wire.parse_json("-0").value is float, "Integer literals become int")
	check(Wire.parse_json("9223372036854775807").value == 9223372036854775807, "int64 max stays exact")
	check(Wire.parse_json("18446744073709551615", Wire.DATA_MAX_NODES, true).value is Wire.RawNumber, "Keep raw number text for data")
	check(Wire.parse_json("1.5").value is float, "Protocol parsing returns plain floats")
	# UTF-8 is validated before parsing; a BOM is not JSON whitespace.
	check(UTF8.decode(PackedByteArray([0xf4, 0x90, 0x80, 0x80])).has("error"), "Reject code point above U+10FFFF")
	check(Wire.parse_json_bytes(PackedByteArray([0x5b, 0xe4, 0x5d])).error == "invalid_utf8", "Reject truncated UTF-8")
	check(Wire.parse_json_bytes(PackedByteArray([0x22, 0xe4, 0xbd])).error == "invalid_utf8", "Reject UTF-8 truncated at end")
	check(Wire.parse_json_bytes(PackedByteArray([0xef, 0xbb, 0xbf, 0x7b, 0x7d])).error == "invalid_json", "Reject BOM")
	check(Wire.parse_json_bytes(PackedByteArray([0x7b, 0x7d, 0x00])).error == "invalid_json", "Reject NUL")
	check(Wire.parse_json_bytes("{\"k\":\"你好\"}".to_utf8_buffer()).value.k == "你好", "Parse UTF-8 bytes")
	# Budgets: depth counts container nesting from 0; nodes count every value.
	check(not Wire.parse_json("[".repeat(64) + "0" + "]".repeat(64)).has("error"), "Depth 64 accepted")
	check(Wire.parse_json("[".repeat(65) + "0" + "]".repeat(65)).has("error"), "Depth 65 rejected")
	check(not Wire.parse_json("[0,0,0]", 4).has("error") and Wire.parse_json("[0,0,0]", 3).has("error"), "Node budget")
	var zeros := PackedStringArray()
	zeros.resize(150000)
	zeros.fill("0")
	var big := "[" + ",".join(zeros) + "]"
	check(Wire.parse_json(big).has("error"), "Protocol budget rejects 150,001 values")
	check(not Wire.parse_json(big, Wire.DATA_MAX_NODES).has("error"), "Data budget accepts 150,001 values")
	# Go protojson integer, map key and bytes rules.
	check(Wire.proto_integer("1.0", true, 32) == "1" and Wire.proto_integer("1e2", true, 32) == "100" and Wire.proto_integer("100e-2", true, 32) == "1", "Integral floats")
	check(Wire.proto_integer("-0", false, 32) == "0" and Wire.proto_integer("1e+0002", true, 32) == "100" and Wire.proto_integer("0e99999999999", true, 64) == "0", "Zero and exponent forms")
	check(Wire.proto_integer("9007199254740993", true, 64) == "9007199254740993" and Wire.proto_integer("1.8446744073709551615e19", false, 64) == "18446744073709551615", "Exact 64-bit numbers")
	for text in ["1.5", "1e-2", "2147483648", "01", " 1", "1.0000000000000001", "0.000000000000000000001e21", "1e99999999999", "", "+1", "0x1"]:
		check(Wire.proto_integer(text, true, 32) == null, "Reject integer " + text)
	check(Wire.proto_integer("-1", false, 64) == null, "Reject negative unsigned")
	check(Wire.map_key("01", "int32") == "1" and Wire.map_key("+1", "sint64") == "1" and Wire.map_key("-0", "int32") == "0", "Go ParseInt map keys")
	check(Wire.map_key("+1", "uint32") == null and Wire.map_key("1e0", "int64") == null and Wire.map_key("4294967296", "uint32") == null, "Reject map keys")
	check(Wire.proto_bytes("AAE") == "AAE=" and Wire.proto_bytes("AAF=") == "AAE=" and Wire.proto_bytes("-_-_") == "+/+/" and Wire.proto_bytes("YWJj\n") == "YWJj" and Wire.proto_bytes("") == "", "Go Base64 forms")
	check(Wire.proto_bytes("AA=") == null and Wire.proto_bytes("AA+_") == null and Wire.proto_bytes("A") == null and Wire.proto_bytes("YWJ\n") == null, "Reject Base64")

func _data_tests() -> void:
	var schema := {"messages": {
		"N": {"fields": {"id": {"kind": "int64"}, "n": {"kind": "int32", "list": true}, "u": {"kind": "uint64"}, "e": {"kind": "enum", "type": "E"},
			"b": {"kind": "bytes"}, "m": {"kind": "int32", "mapKey": "int32"}, "s": {"kind": "string"}, "f": {"kind": "double"},
			"next": {"kind": "message", "type": "N"}, "v": {"kind": "message", "type": "google.protobuf.Value"}, "st": {"kind": "message", "type": "google.protobuf.Struct"}},
			"oneofs": [["s", "f"]]},
		"google.protobuf.Value": {"fields": {}}, "google.protobuf.Struct": {"fields": {}}}, "enums": {"E": ["ZERO", "ONE"]}}
	var loader := Data.new(schema)
	var out: Variant = loader.decode("N", "{\"id\":1e2,\"n\":[1.0,\"2\",3e0],\"u\":18446744073709551615,\"e\":1.0,\"b\":\"AAF\",\"m\":{\"+1\":5,\"02\":6},\"s\":null,\"f\":\"1.5\",\"v\":null,\"st\":{\"x\":1.5e0}}")
	check(loader.error.is_empty() and out is Dictionary, "Decode lenient ProtoJSON: " + loader.error)
	if out is Dictionary:
		check(out.id == "100" and out.u == "18446744073709551615" and out.e is int and out.e == 1, "Normalize integers")
		check(out.n == [1, 2, 3] and out.n[0] is int, "Normalize integer list")
		check(out.b == "AAE=" and out.m == {"1": 5, "2": 6}, "Normalize bytes and map keys")
		check(not out.has("s") and out.has("v") and out.v == null and out.f is float and out.f == 1.5, "Null members are absent except Value")
		check(out.st.x is float and out.st.x == 1.5, "Struct numbers become float")
	var exact: Variant = loader.decode("N", "{\"id\":9007199254740993,\"u\":\"1e19\"}")
	check(exact is Dictionary and exact.id == "9007199254740993" and exact.u == "10000000000000000000", "Exact 64-bit numbers and numeric strings")
	var strings: Variant = loader.decode("N", "{\"id\":-0,\"n\":[\"1e2\"]}")
	check(strings is Dictionary and strings.id == "0" and strings.n == [100], "Numeric strings")
	for bad in ["{\"n\":[null]}", "{\"e\":\"1\"}", "{\"m\":{\"1\":1,\"01\":2}}", "{\"b\":\"AA=\"}", "{\"id\":1.5}", "{\"id\":\"01\"}", "{\"u\":-1}",
			"{\"id\":0.000000000000000000001e21}", "{\"id\":1.0000000000000001}", "{\"st\":{\"x\":1e400}}", "{\"s\":\"a\",\"f\":1}", "{\"x\":null}", "{\"id\":1,\"\\u0069d\":2}"]:
		check(loader.decode("N", bad) == null and not loader.error.is_empty(), "Reject data " + bad)
	check(loader.decode("N", "{\"s\":null,\"f\":1}") != null, "null oneof member does not count")
	check(loader.decode_bytes("N", PackedByteArray([0xef, 0xbb, 0xbf, 0x7b, 0x7d])) == null, "Data loader rejects BOM")
	# Validator depth equals JSON depth: id below is at depth 64, then 65.
	var deep := "{\"id\":\"1\"}"
	for index in 63:
		deep = "{\"next\":" + deep + "}"
	check(loader.decode("N", deep) != null, "Message depth 64 accepted: " + loader.error)
	check(loader.decode("N", "{\"next\":" + deep + "}") == null, "Message depth 65 rejected")
	var ones := PackedStringArray()
	ones.resize(150000)
	ones.fill("1")
	var large := "{\"n\":[" + ",".join(ones) + "]}"
	var decoded: Variant = loader.decode("N", large)
	check(decoded is Dictionary and decoded.n.size() == 150000, "Data loader accepts more than 100,000 values")
	var protocol := Wire.new(schema)
	check(not protocol.validate("N", Wire.parse_json(large, Wire.DATA_MAX_NODES).value).is_empty(), "Protocol validator keeps the 100,000-value budget")
	var request := {"id": 5, "n": ["1e1"]}
	check(protocol.validate("N", request).is_empty() and request.id == 5 and request.n == ["1e1"], "Protocol validation does not rewrite requests")
