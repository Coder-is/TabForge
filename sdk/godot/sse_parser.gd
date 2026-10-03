extends RefCounted

const UTF8 = preload("utf8.gd")
var maximum_bytes := 1048576
var _line := PackedByteArray()
var _data := PackedStringArray()
var _event := ""
var _id := ""
var _skip_lf := false
var _first := true
var _size := 0
var _error := ""

# Newline bytes cannot occur inside UTF-8 codepoints. Decode only complete lines.
func feed(chunk: PackedByteArray) -> Dictionary:
	var frames: Array = []
	if not _error.is_empty():
		return {"frames": frames, "error": _error}
	for byte in chunk:
		if _skip_lf:
			_skip_lf = false
			if byte == 10:
				continue
		_size += 1
		if _size > maximum_bytes:
			_error = "frame_too_large"
			break
		if byte == 10 or byte == 13:
			_consume(frames)
			_skip_lf = byte == 13
			if not _error.is_empty():
				break
		else:
			_line.append(byte)
	return {"frames": frames, "error": _error}

func _consume(frames: Array) -> void:
	if _line.has(0):
		if _line.size() >= 3 and _line[0] == 105 and _line[1] == 100 and _line[2] == 58:
			_line.clear()
			return
		_error = "invalid_utf8"
		return
	var decoded: Dictionary = UTF8.decode(_line)
	_line.clear()
	if decoded.has("error"):
		_error = decoded.error
		return
	var line: String = decoded.text
	if _first:
		_first = false
		line = line.trim_prefix("\ufeff")
	if line.is_empty():
		if not _data.is_empty():
			frames.append({"id": _id, "event": _event if not _event.is_empty() else "message", "data": "\n".join(_data)})
		_data.clear(); _event = ""; _size = 0
		return
	if line.begins_with(":"):
		return
	var colon := line.find(":")
	var field := line if colon < 0 else line.substr(0, colon)
	var value := "" if colon < 0 else line.substr(colon + 1)
	value = value.trim_prefix(" ")
	match field:
		"data": _data.append(value)
		"event": _event = value
		"id":
			_id = value
