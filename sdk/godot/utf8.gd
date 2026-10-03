extends RefCounted

# Strict UTF-8 validation before Godot's decoder, including overlong sequences.
static func decode(bytes: PackedByteArray) -> Dictionary:
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
