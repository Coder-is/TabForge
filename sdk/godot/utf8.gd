extends RefCounted

const Wire = preload("wire_validator.gd")

# Strict UTF-8 validation before Godot's decoder, including overlong sequences.
# The implementation lives in wire_validator.gd so the data loader shares it.
static func decode(bytes: PackedByteArray) -> Dictionary:
	return Wire.decode_utf8(bytes)
