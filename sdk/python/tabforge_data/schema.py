import decimal
import json
import math
import re
import struct

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
# json.loads joins escaped surrogate pairs, so any surrogate left is unpaired.
_LONE_SURROGATE = re.compile("[\ud800-\udfff]")
# 2^128 - 2^104 is the largest float32; decimals from 2^128 - 2^103 up overflow.
_FLOAT32_MAX = 3.4028234663852886e38
_FLOAT32_LIMIT = 3.4028235677973366e38
_FLOAT32_HALFWAY = decimal.Decimal(340282356779733661637539395458142568448)
_TIMESTAMP = re.compile(r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:([.,])([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))")
_DURATION = re.compile(r"[+-]?(0|[1-9][0-9]*)?(\.[0-9]{0,9})?s")


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
            for key in value:
                if _LONE_SURROGATE.search(key):
                    raise ValueError("Unpaired surrogate in JSON member name")
            stack.extend((item, depth + 1) for item in value.values())
        elif isinstance(value, list):
            stack.extend((item, depth + 1) for item in value)
        elif isinstance(value, str) and _LONE_SURROGATE.search(value):
            raise ValueError("Unpaired surrogate in JSON string")
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


def _civil_days(year, month, day):
    """Days from 1970-01-01, proleptic Gregorian; year 0 is allowed (offsets can reach year 1)."""
    y = year + 400 - (1 if month <= 2 else 0)
    era, yoe = divmod(y, 400)
    doy = (153 * ((month + 9) % 12) + 2) // 5 + day - 1
    return era * 146097 + yoe * 365 + yoe // 4 - yoe // 100 + doy - 719468 - 146097


def _days_in_month(year, month):
    if month == 2:
        return 29 if year % 4 == 0 and (year % 100 != 0 or year % 400 == 0) else 28
    return (31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31)[month - 1]


def proto_timestamp(value):
    """Go protojson Timestamp input: time.Parse(time.RFC3339Nano), then 0001-01-01 to
    9999-12-31 in UTC and at most 9 digits after ".". Like Go: any offset (hours to 24,
    minutes to 60), a one-digit hour, and "," before a fraction of any length."""
    match = _TIMESTAMP.fullmatch(value) if isinstance(value, str) else None
    if not match:
        return False
    year, month, day, hour, minute, second = (int(match.group(index)) for index in range(1, 7))
    if (match.group(7) == "." and len(match.group(8)) > 9) or not 1 <= month <= 12 or not 1 <= day <= _days_in_month(year, month) or hour > 23 or minute > 59 or second > 59:
        return False
    offset = 0
    if match.group(9):
        hours, minutes = int(match.group(10)), int(match.group(11))
        if hours > 24 or minutes > 60:
            return False
        offset = (-1 if match.group(9) == "-" else 1) * (hours * 3600 + minutes * 60)
    seconds = _civil_days(year, month, day) * 86400 + hour * 3600 + minute * 60 + second - offset
    return -62135596800 <= seconds <= 253402300799


def proto_duration(value):
    """Go protojson Duration input: optional sign, integer and/or "." with up to 9 digits
    ("1.s", ".5s" and ".s" are accepted), then "s", within 315576000000 seconds."""
    match = _DURATION.fullmatch(value) if isinstance(value, str) else None
    if not match or (match.group(1) is None and match.group(2) is None):
        return False
    seconds = match.group(1) or ""
    return len(seconds) < 12 or (len(seconds) == 12 and seconds <= "315576000000")


def _float32(number, text):
    """Go's float32 value of finite JSON number text (rounded once, from the exact
    decimal), or None when it overflows float32."""
    if abs(number) == _FLOAT32_LIMIT:
        return math.copysign(_FLOAT32_MAX, number) if decimal.Decimal(text).copy_abs() < _FLOAT32_HALFWAY else None
    if abs(number) > _FLOAT32_LIMIT:
        return None
    return struct.unpack("<f", struct.pack("<f", number))[0]


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
        self.aliases = {}  # message name -> {original Proto name: JSON name}

    def validate(self, message, value):
        # Keep budgets per call so one validator can serve concurrent readers.
        return _Validation(self).message(message, value, "$", 0, False)


class _Validation:
    def __init__(self, owner):
        self.schema = owner.schema
        self.max_depth = owner.max_depth
        self.max_nodes = owner.max_nodes
        self.aliases = owner.aliases
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
        aliases = self.aliases.get(name)
        if aliases is None:
            aliases = {field["protoName"]: json_name for json_name, field in declared.items() if isinstance(field.get("protoName"), str) and field["protoName"] not in declared}
            self.aliases[name] = aliases
        # A member is spelled as the JSON name or the original Proto name; like Go
        # protojson, a field given twice (also once in each spelling) is rejected.
        members = []
        spelled = {}
        for key in list(fields):
            if skip_type and key == "@type":
                continue
            json_name = key if key in declared else aliases.get(key)
            if json_name is None:
                self.fail(path + "." + key, "declared field")
            if json_name in spelled:
                self.fail(path + "." + key, "field given once")
            spelled[json_name] = key
            members.append((key, json_name))

        def present(json_name):
            key = spelled.get(json_name)
            return key is not None and (fields[key] is not None or _nullable(declared[json_name]))

        for group in shape.get("oneofs", []):
            if sum(1 for key in group if present(key)) > 1:
                self.fail(path, "at most one oneof member")
        for key, field in declared.items():
            if field.get("required") and not present(key):
                self.fail(path + "." + key, "required field")
        for key, json_name in members:
            next_path = path + "." + key
            field = declared[json_name]
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
            if key != json_name:
                fields[json_name] = fields.pop(key)
        return fields

    def well_known(self, name, value, path, depth):
        short = name[len(_PREFIX):]
        if short in _WRAPPERS:
            return self.scalar({"kind": _WRAPPERS[short]}, value, path, depth, True)
        if short == "Timestamp":
            if not proto_timestamp(value):
                self.fail(path, "RFC 3339 timestamp in range")
            return value
        if short == "Duration":
            if not proto_duration(value):
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
            # An Any may hold another Any; the depth budget bounds the recursion.
            if nested not in self.schema["messages"]:
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
            if not math.isfinite(number):
                self.fail(path, kind)
            if kind == "float":
                # Go keeps float32: 3.40282356e+38 becomes 3.4028234663852886e+38, 1e-50 becomes 0.
                rounded = _float32(number, value if isinstance(value, str) else _number_text(value))
                if rounded is None:
                    self.fail(path, kind)
                return value if type(value) is int and rounded == value else rounded
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
