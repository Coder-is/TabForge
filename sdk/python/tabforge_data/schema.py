import base64
import datetime
import json
import math
import re


def parse_json(text):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("Duplicate JSON member: " + key)
            result[key] = value
        return result

    def constant(value):
        raise ValueError("Invalid JSON constant: " + value)

    try:
        result = json.loads(text, object_pairs_hook=pairs, parse_constant=constant)
    except RecursionError as error:
        raise ValueError("JSON depth exceeds budget") from error
    return result


class SchemaValidator:
    def __init__(self, schema):
        self.schema = schema

    def validate(self, message, value):
        # Keep budgets per call so one validator can serve concurrent readers.
        nodes = [0]

        def fail(path, expected):
            raise ValueError("Invalid ProtoJSON at %s: expected %s" % (path, expected))

        def budget(path, depth):
            nodes[0] += 1
            if depth > 64 or nodes[0] > 100000:
                fail(path, "bounded message depth/size")

        def record(value, path):
            if not isinstance(value, dict):
                fail(path, "object")
            return value

        def integer(value, signed, bits=64):
            if not isinstance(value, str) or not re.fullmatch(r"-?(0|[1-9][0-9]*)", value) or value == "-0":
                return False
            number = int(value)
            return -(1 << (bits - 1)) <= number < (1 << (bits - 1)) if signed else 0 <= number < (1 << bits)

        def any_json(value, path, depth):
            budget(path, depth)
            if value is None or isinstance(value, (str, bool)):
                return
            if type(value) in (int, float):
                if not math.isfinite(value):
                    fail(path, "finite JSON number")
                return
            if isinstance(value, list):
                for item in value:
                    any_json(item, path, depth + 1)
            else:
                for key, item in record(value, path).items():
                    any_json(item, path + "." + key, depth + 1)

        def scalar(field, value, path, depth):
            budget(path, depth)
            kind = field["kind"]
            if kind in ("message", "group"):
                return msg(field["type"], value, path, depth + 1)
            if kind == "enum":
                if field["type"] == "google.protobuf.NullValue" and value is None:
                    return
                if type(value) is int and -(1 << 31) <= value < (1 << 31):
                    return
                if isinstance(value, str) and value in self.schema["enums"].get(field["type"], []):
                    return
                fail(path, "enum name or int32")
            elif kind in ("int64", "sint64", "sfixed64", "uint64", "fixed64"):
                if not integer(value, kind[0] != "u" and kind != "fixed64"):
                    fail(path, kind + " decimal string")
            elif kind in ("int32", "sint32", "sfixed32", "uint32", "fixed32"):
                if type(value) is not int or not integer(str(value), kind[0] != "u" and kind != "fixed32", 32):
                    fail(path, kind)
            elif kind in ("double", "float"):
                if value in ("NaN", "Infinity", "-Infinity"):
                    return
                if type(value) not in (int, float) or not math.isfinite(value) or (kind == "float" and abs(value) > 3.4028234663852886e38):
                    fail(path, kind)
            elif kind == "bool":
                if type(value) is not bool:
                    fail(path, "boolean")
            elif kind in ("string", "bytes"):
                if not isinstance(value, str):
                    fail(path, "string")
                if kind == "bytes":
                    if not re.fullmatch(r"(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?", value):
                        fail(path, "padded Base64")
                    base64.b64decode(value, validate=True)
            else:
                fail(path, "supported scalar type")

        def msg(name, value, path, depth):
            budget(path, depth)
            if name not in self.schema["messages"]:
                fail(path, "known message type")
            prefix = "google.protobuf."
            if name.startswith(prefix):
                short = name[len(prefix):]
                wrappers = {"DoubleValue": "double", "FloatValue": "float", "Int64Value": "int64", "UInt64Value": "uint64", "Int32Value": "int32", "UInt32Value": "uint32", "BoolValue": "bool", "StringValue": "string", "BytesValue": "bytes"}
                if short in wrappers:
                    return scalar({"kind": wrappers[short]}, value, path, depth + 1)
                if short == "Timestamp":
                    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z", value):
                        fail(path, "canonical UTC timestamp")
                    try:
                        datetime.datetime.strptime(value[:19], "%Y-%m-%dT%H:%M:%S")
                    except ValueError:
                        fail(path, "valid UTC timestamp")
                    return
                if short == "Duration":
                    if not isinstance(value, str) or not re.fullmatch(r"-?(0|[1-9][0-9]*)(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s", value) or abs(int(value[:-1].split(".")[0])) > 315576000000:
                        fail(path, "duration in range")
                    return
                if short == "FieldMask":
                    if not isinstance(value, str) or not re.fullmatch(r"(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?", value):
                        fail(path, "canonical field mask")
                    return
                if short in ("Value", "Struct", "ListValue"):
                    if short == "Struct":
                        record(value, path)
                    if short == "ListValue" and not isinstance(value, list):
                        fail(path, "array")
                    return any_json(value, path, depth + 1)
                if short == "Any":
                    fields = record(value, path)
                    url = fields.get("@type")
                    if not isinstance(url, str):
                        fail(path, "Any @type")
                    nested = url.split("/")[-1]
                    if nested == name or nested not in self.schema["messages"]:
                        fail(path, "known Any type")
                    fields = {key: item for key, item in fields.items() if key != "@type"}
                    if nested.startswith(prefix) and nested != prefix + "Empty":
                        if set(fields) != {"value"}:
                            fail(path, "Any value")
                        return msg(nested, fields["value"], path + ".value", depth + 1)
                    return msg(nested, fields, path, depth + 1)
            shape = self.schema["messages"][name]
            fields = record(value, path)
            for group in shape.get("oneofs", []):
                if sum(key in fields for key in group) > 1:
                    fail(path, "at most one oneof member")
            for key, field in shape["fields"].items():
                if field.get("required") and key not in fields:
                    fail(path + "." + key, "required field")
            for key, item in fields.items():
                if key not in shape["fields"]:
                    fail(path + "." + key, "declared field")
                field = shape["fields"][key]
                next_path = path + "." + key
                if field.get("mapKey"):
                    for map_key, map_value in record(item, next_path).items():
                        kind = field["mapKey"]
                        if kind == "bool" and map_key not in ("true", "false"):
                            fail(next_path, "boolean map key")
                        if kind not in ("string", "bool") and not integer(map_key, kind.startswith(("int", "s")), 32 if kind.endswith("32") else 64):
                            fail(next_path, "integer map key")
                        scalar(field, map_value, next_path + "." + map_key, depth + 1)
                elif field.get("list"):
                    if not isinstance(item, list):
                        fail(next_path, "array")
                    for index, element in enumerate(item):
                        scalar(field, element, "%s[%d]" % (next_path, index), depth + 1)
                else:
                    scalar(field, item, next_path, depth + 1)

        msg(message, value, "$", 0)
