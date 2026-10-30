import datetime
import json
import math
import re

# Data bundle budget shared by every TabForge data loader (Go, Node.js, Java,
# Python, Unity, Godot, Unreal): container nesting depth (root value = 0) and
# the number of JSON values (scalars and containers, not keys) per document.
# Network protocol messages elsewhere keep a 100,000-value budget.
MAX_JSON_DEPTH = 64
MAX_JSON_NODES = 10000000

_JSON_NUMBER = re.compile(r"-?(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?")
_PREFIX = "google.protobuf."
_WRAPPERS = {"DoubleValue": "double", "FloatValue": "float", "Int64Value": "int64", "UInt64Value": "uint64", "Int32Value": "int32", "UInt32Value": "uint32", "BoolValue": "bool", "StringValue": "string", "BytesValue": "bytes"}
_INTEGER_KINDS = {
    "int32": (True, 32), "sint32": (True, 32), "sfixed32": (True, 32), "uint32": (False, 32), "fixed32": (False, 32),
    "int64": (True, 64), "sint64": (True, 64), "sfixed64": (True, 64), "uint64": (False, 64), "fixed64": (False, 64),
}
_BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
_FLOAT_SPECIALS = ("NaN", "Infinity", "-Infinity")
_UNSET = object()


class _RawFloat(float):
    """A parsed JSON number with a fraction or exponent; keeps its exact source text."""


def _raw_float(text):
    number = _RawFloat(text)
    number.raw = text
    return number


def _parse_int(text):
    try:
        return int(text)
    except ValueError as error:  # e.g. Python 3.11+ integer string length limit
        raise ValueError("JSON integer is too long") from error


def parse_json(text, max_depth=MAX_JSON_DEPTH, max_nodes=MAX_JSON_NODES):
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
        result = json.loads(text, object_pairs_hook=pairs, parse_constant=constant, parse_float=_raw_float, parse_int=_parse_int)
    except RecursionError as error:
        raise ValueError("JSON depth exceeds budget") from error
    nodes = 0
    stack = [(result, 0)]
    while stack:
        value, depth = stack.pop()
        nodes += 1
        if depth > max_depth or nodes > max_nodes:
            raise ValueError("JSON exceeds depth %d or %d values" % (max_depth, max_nodes))
        if isinstance(value, dict):
            stack.extend((item, depth + 1) for item in value.values())
        elif isinstance(value, list):
            stack.extend((item, depth + 1) for item in value)
    return result


def _in_range(digits, negative, signed, bits):
    if negative and not signed:
        return False
    if bits == 32:
        maximum = ("2147483648" if negative else "2147483647") if signed else "4294967295"
    else:
        maximum = ("9223372036854775808" if negative else "9223372036854775807") if signed else "18446744073709551615"
    return len(digits) < len(maximum) or (len(digits) == len(maximum) and digits <= maximum)


def proto_integer(text, signed, bits):
    """Go protojson integer input (normalizeToIntString): canonical decimal or None."""
    match = _JSON_NUMBER.fullmatch(text)
    if not match:
        return None
    intp = "" if match.group(1) == "0" else match.group(1)
    frac = (match.group(2) or "").rstrip("0")
    if not intp and not frac:
        return "0"
    exponent = 0
    if match.group(3) is not None:
        magnitude = match.group(3).lstrip("+-").lstrip("0") or "0"
        if len(magnitude) > 10:
            return None
        exponent = -int(magnitude) if match.group(3).startswith("-") else int(magnitude)
        if not -2147483648 <= exponent <= 2147483647:
            return None
    if exponent >= 0:
        if len(frac) > exponent or len(intp) + exponent > 20:
            return None
        digits = intp + frac + "0" * (exponent - len(frac))
    else:
        index = len(intp) + exponent
        if frac or index < 0 or intp[index:].strip("0"):
            return None
        digits = intp[:index]
    digits = digits.lstrip("0") or "0"
    negative = text.startswith("-")
    return ("-" if negative else "") + digits if _in_range(digits, negative, signed, bits) else None


def proto_map_key(key, kind):
    """Go strconv.ParseInt/ParseUint(key, 10, bits) for map keys: canonical key or None."""
    if kind == "string":
        return key
    if kind == "bool":
        return key if key in ("true", "false") else None
    shape = _INTEGER_KINDS.get(kind)
    match = re.fullmatch(r"([+-]?)([0-9]+)", key) if shape else None
    if not match or (match.group(1) and not shape[0]):
        return None
    digits = match.group(2).lstrip("0") or "0"
    negative = match.group(1) == "-" and digits != "0"
    return ("-" if negative else "") + digits if _in_range(digits, negative, shape[0], shape[1]) else None


def proto_bytes(value):
    """Go protojson bytes input; returns canonical padded standard Base64 or None."""
    url = "-" in value or "_" in value
    text = value.replace("\r", "").replace("\n", "")
    alphabet = "A-Za-z0-9_-" if url else "A-Za-z0-9+/"
    if len(value) % 4 == 0:
        pattern = "(?:[%s]{4})*(?:[%s]{2}==|[%s]{3}=)?" % (alphabet, alphabet, alphabet)
    else:
        pattern = "(?:[%s]{4})*(?:[%s]{2,3})?" % (alphabet, alphabet)
    if not re.fullmatch(pattern, text):
        return None
    body = text.rstrip("=").replace("-", "+").replace("_", "/")
    rest = len(body) % 4
    if rest:
        last = _BASE64.index(body[-1]) & (0x30 if rest == 2 else 0x3C)
        body = body[:-1] + _BASE64[last] + ("==" if rest == 2 else "=")
    return body


def _number_text(value):
    if isinstance(value, _RawFloat):
        return value.raw
    if type(value) is int:
        return str(value)
    if type(value) is float and math.isfinite(value):
        return repr(value)
    return None


def _finite(value):
    try:
        return math.isfinite(float(value))
    except OverflowError:
        return False


def _nullable(field):
    return (field.get("kind") == "message" and field.get("type") == "google.protobuf.Value") or (field.get("kind") == "enum" and field.get("type") == "google.protobuf.NullValue")


class SchemaValidator:
    """Validates with Go protojson's input rules and returns canonical ProtoJSON.

    Depth equals the JSON nesting depth and each JSON value counts at most once,
    so for parsed text the parser's budget is the binding one. The value is
    normalized in place: 32-bit integers and enum numbers become int, 64-bit
    integers decimal strings, numeric strings floats, bytes padded standard
    Base64, and null members are removed.
    """

    def __init__(self, schema, max_depth=MAX_JSON_DEPTH, max_nodes=MAX_JSON_NODES):
        self.schema = schema
        self.max_depth = max_depth
        self.max_nodes = max_nodes

    def validate(self, message, value):
        # Keep budgets per call so one validator can serve concurrent readers.
        return _Validation(self).message(message, value, "$", 0, False)


class _Validation:
    def __init__(self, owner):
        self.schema = owner.schema
        self.max_depth = owner.max_depth
        self.max_nodes = owner.max_nodes
        self.nodes = 0

    @staticmethod
    def fail(path, expected):
        raise ValueError("Invalid ProtoJSON at %s: expected %s" % (path, expected))

    def budget(self, path, depth):
        self.nodes += 1
        if depth > self.max_depth or self.nodes > self.max_nodes:
            self.fail(path, "bounded message depth/size")

    def record(self, value, path):
        if not isinstance(value, dict):
            self.fail(path, "object")
        return value

    def message(self, name, value, path, depth, counted, skip_type=False):
        if not counted:
            self.budget(path, depth)
        if name.startswith(_PREFIX):
            known = self.well_known(name, value, path, depth)
            if known is not _UNSET:
                return known
        if name not in self.schema["messages"]:
            self.fail(path, "known message type")
        shape = self.schema["messages"][name]
        declared = shape["fields"]
        fields = self.record(value, path)

        def present(key):
            return key in fields and (fields[key] is not None or key not in declared or _nullable(declared[key]))

        for group in shape.get("oneofs", []):
            if sum(1 for key in group if present(key)) > 1:
                self.fail(path, "at most one oneof member")
        for key, field in declared.items():
            if field.get("required") and not present(key):
                self.fail(path + "." + key, "required field")
        for key in list(fields):
            if skip_type and key == "@type":
                continue
            next_path = path + "." + key
            if key not in declared:
                self.fail(next_path, "declared field")
            field = declared[key]
            item = fields[key]
            if item is None and not _nullable(field):
                del fields[key]
                continue
            if field.get("mapKey"):
                entries = self.record(item, next_path)
                self.budget(next_path, depth + 1)
                keys = []
                seen = set()
                for entry_key in list(entries):
                    canonical = proto_map_key(entry_key, field["mapKey"])
                    if canonical is None:
                        self.fail(next_path, field["mapKey"] + " map key")
                    if canonical in seen:
                        self.fail(next_path, "unique map key")
                    seen.add(canonical)
                    keys.append((entry_key, canonical))
                    entries[entry_key] = self.scalar(field, entries[entry_key], next_path + "." + entry_key, depth + 2, False)
                if any(original != canonical for original, canonical in keys):
                    fields[key] = {canonical: entries[original] for original, canonical in keys}
            elif field.get("list"):
                if not isinstance(item, list):
                    self.fail(next_path, "array")
                self.budget(next_path, depth + 1)
                for index, element in enumerate(item):
                    item[index] = self.scalar(field, element, "%s[%d]" % (next_path, index), depth + 2, False)
            else:
                fields[key] = self.scalar(field, item, next_path, depth + 1, False)
        return fields

    def well_known(self, name, value, path, depth):
        short = name[len(_PREFIX):]
        if short in _WRAPPERS:
            return self.scalar({"kind": _WRAPPERS[short]}, value, path, depth, True)
        if short == "Timestamp":
            if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z", value):
                self.fail(path, "canonical UTC timestamp")
            try:
                datetime.datetime.strptime(value[:19], "%Y-%m-%dT%H:%M:%S")
            except ValueError:
                self.fail(path, "valid UTC timestamp")
            return value
        if short == "Duration":
            if not isinstance(value, str) or not re.fullmatch(r"-?(0|[1-9][0-9]*)(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s", value) or abs(int(value[:-1].split(".")[0])) > 315576000000:
                self.fail(path, "duration in range")
            return value
        if short == "FieldMask":
            if not isinstance(value, str) or not re.fullmatch(r"(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?", value):
                self.fail(path, "canonical field mask")
            return value
        if short == "Value":
            return self.any_json(value, path, depth, True)
        if short == "Struct":
            fields = self.record(value, path)
            for key in list(fields):
                fields[key] = self.any_json(fields[key], path + "." + key, depth + 1, False)
            return fields
        if short == "ListValue":
            if not isinstance(value, list):
                self.fail(path, "array")
            for index, element in enumerate(value):
                value[index] = self.any_json(element, path, depth + 1, False)
            return value
        if short == "Any":
            fields = self.record(value, path)
            # protojson writes an unset Any as {} and Empty as {"@type": ..., "value": {}}.
            if not fields:
                return fields
            url = fields.get("@type")
            if not isinstance(url, str):
                self.fail(path, "Any @type")
            nested = url.split("/")[-1]
            if nested == name or nested not in self.schema["messages"]:
                self.fail(path, "known Any type")
            members = [key for key in fields if key != "@type"]
            if nested == _PREFIX + "Empty" and not members:
                return fields  # Go tolerates an omitted Empty value on input.
            if nested.startswith(_PREFIX):
                if members != ["value"]:
                    self.fail(path, "Any value")
                fields["value"] = self.message(nested, fields["value"], path + ".value", depth + 1, False)
            else:
                self.message(nested, fields, path, depth, True, True)
            return fields
        return _UNSET

    def scalar(self, field, value, path, depth, counted):
        kind = field["kind"]
        if kind in ("message", "group"):
            return self.message(field["type"], value, path, depth, counted)
        if not counted:
            self.budget(path, depth)
        if kind == "enum":
            if field["type"] == "google.protobuf.NullValue" and value is None:
                return value
            if isinstance(value, str) and value in self.schema["enums"].get(field["type"], []):
                return value
            text = None if isinstance(value, str) else _number_text(value)
            number = proto_integer(text, True, 32) if text is not None else None
            if number is None:
                self.fail(path, "enum name or int32")
            return int(number)
        if kind in _INTEGER_KINDS:
            signed, bits = _INTEGER_KINDS[kind]
            text = value if isinstance(value, str) else _number_text(value)
            integer = proto_integer(text, signed, bits) if text is not None else None
            if integer is None:
                self.fail(path, kind)
            return integer if bits == 64 else int(integer)
        if kind in ("double", "float"):
            if isinstance(value, str) and value in _FLOAT_SPECIALS:
                return value
            if isinstance(value, str) and _JSON_NUMBER.fullmatch(value):
                number = float(value)
            elif type(value) in (int, float, _RawFloat) and _finite(value):
                number = float(value)
            else:
                number = math.inf
            if not math.isfinite(number) or (kind == "float" and abs(number) >= 3.4028235677973366e38):
                self.fail(path, kind)
            return value if type(value) is int else number
        if kind == "bool":
            if type(value) is not bool:
                self.fail(path, "boolean")
            return value
        if kind == "string":
            if not isinstance(value, str):
                self.fail(path, "string")
            return value
        if kind == "bytes":
            canonical = proto_bytes(value) if isinstance(value, str) else None
            if canonical is None:
                self.fail(path, "Base64 string")
            return canonical
        self.fail(path, "supported scalar type")

    def any_json(self, value, path, depth, counted):
        if not counted:
            self.budget(path, depth)
        if value is None or isinstance(value, (str, bool)):
            return value
        if type(value) is int:
            if not _finite(value):
                self.fail(path, "finite JSON number")
            return value
        if isinstance(value, float):
            if not math.isfinite(value):
                self.fail(path, "finite JSON number")
            return float(value)
        if isinstance(value, list):
            for index, item in enumerate(value):
                value[index] = self.any_json(item, path, depth + 1, False)
            return value
        fields = self.record(value, path)
        for key in list(fields):
            fields[key] = self.any_json(fields[key], path + "." + key, depth + 1, False)
        return fields
