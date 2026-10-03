extends SceneTree

const Parser = preload("../sse_parser.gd")
const UTF8 = preload("../utf8.gd")
const Wire = preload("../wire_validator.gd")
const Client = preload("../protocol_client.gd")
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
	check(not Wire.integer_string(123, false), "int64 must be string")
	check(Client.next_sequence("99999999999999999999") == "100000000000000000000", "Sequence without overflow")
	var validator := Wire.new({"messages": {"Test": {"fields": {"id": {"kind": "uint64"}, "text": {"kind": "string"}}}}, "enums": {}})
	check(validator.validate("Test", {"id": "18446744073709551615", "text": "你好😀"}).is_empty(), "Validate message")
	check(not validator.validate("Test", {"id": 123}).is_empty(), "Reject wrong scalar")
	check(not validator.validate("Test", {"unknown": true}).is_empty(), "Reject unknown field")
	check(validator.validate("google.protobuf.Timestamp", "2024-02-29T12:34:56.123456789Z").is_empty(), "Timestamp")
	check(not validator.validate("google.protobuf.Timestamp", "2025-02-29T12:34:56Z").is_empty(), "Invalid leap date")
	check(not validator.validate("google.protobuf.Duration", "315576000001s").is_empty(), "Duration range")
	print("Godot unit tests: %d failures" % failures)
	quit(0 if failures == 0 else 1)
