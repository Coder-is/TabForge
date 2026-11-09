using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Text.RegularExpressions;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    /// <summary>
    /// Binary Protobuf to ProtoJSON (JToken) and back, driven by the generated
    /// codecSchema. Output equals Go protojson.Marshal: 64-bit integers as
    /// decimal strings, enums by name, bytes as Base64, defaults omitted.
    /// </summary>
    public sealed class ProtobufCodec
    {
        private sealed class Field
        {
            public int No;
            public string Name, Kind, Type, MapKey;
            public bool List, Packed, Presence;
            public int Oneof;
        }

        private const int MaxDepth = 100;
        private const int WireVarint = 0, I64 = 1, Len = 2, I32 = 5;
        private static readonly HashSet<string> LongKinds = new HashSet<string> { "int64", "uint64", "sint64", "fixed64", "sfixed64" };
        private static readonly HashSet<string> Packable = new HashSet<string> { "int32", "int64", "uint32", "uint64", "sint32", "sint64", "bool", "enum", "fixed32", "sfixed32", "float", "fixed64", "sfixed64", "double" };
        private static readonly Dictionary<string, string> Wrappers = new Dictionary<string, string>
        {
            ["google.protobuf.DoubleValue"] = "double", ["google.protobuf.FloatValue"] = "float", ["google.protobuf.Int64Value"] = "int64",
            ["google.protobuf.UInt64Value"] = "uint64", ["google.protobuf.Int32Value"] = "int32", ["google.protobuf.UInt32Value"] = "uint32",
            ["google.protobuf.BoolValue"] = "bool", ["google.protobuf.StringValue"] = "string", ["google.protobuf.BytesValue"] = "bytes"
        };

        private readonly Dictionary<string, List<Field>> messages = new Dictionary<string, List<Field>>();
        private readonly Dictionary<string, Dictionary<int, Field>> byNumber = new Dictionary<string, Dictionary<int, Field>>();
        private readonly Dictionary<string, Dictionary<string, int>> enumNumbers = new Dictionary<string, Dictionary<string, int>>();
        private readonly Dictionary<string, Dictionary<int, string>> enumNames = new Dictionary<string, Dictionary<int, string>>();

        public ProtobufCodec(JObject codecSchema)
        {
            if (!(codecSchema?["messages"] is JObject types) || !(codecSchema["enums"] is JObject enums))
                throw new ProtocolException("bad_request", "Load codecSchema from the generated runtime.json");
            foreach (var type in types.Properties())
            {
                var fields = new List<Field>();
                var numbers = new Dictionary<int, Field>();
                foreach (JObject f in (JArray)type.Value)
                {
                    var field = new Field
                    {
                        No = (int)f["no"], Name = (string)f["name"], Kind = (string)f["kind"], Type = (string)f["type"], MapKey = (string)f["mapKey"],
                        List = f["list"]?.Value<bool>() ?? false, Packed = f["packed"]?.Value<bool>() ?? false,
                        Presence = f["presence"]?.Value<bool>() ?? false, Oneof = f["oneof"]?.Value<int>() ?? 0
                    };
                    fields.Add(field);
                    numbers[field.No] = field;
                }
                messages[type.Name] = fields;
                byNumber[type.Name] = numbers;
            }
            foreach (var e in enums.Properties())
            {
                var numbers = new Dictionary<string, int>();
                var names = new Dictionary<int, string>();
                foreach (JArray pair in (JArray)e.Value)
                {
                    var name = (string)pair[0];
                    var number = (int)pair[1];
                    numbers[name] = number;
                    if (!names.ContainsKey(number))
                        names[number] = name;
                }
                enumNumbers[e.Name] = numbers;
                enumNames[e.Name] = names;
            }
        }

        private static Exception Fail(string message) => new ProtocolException("invalid_message", message);

        private List<Field> Fields(string type)
        {
            if (!messages.TryGetValue(type, out var fields))
                throw Fail("Unknown message type " + type);
            return fields;
        }

        private static int WireType(string kind)
        {
            switch (kind)
            {
                case "fixed64": case "sfixed64": case "double": return I64;
                case "fixed32": case "sfixed32": case "float": return I32;
                case "string": case "bytes": case "message": return Len;
                default: return WireVarint;
            }
        }

        // ---- encoding ----

        /// <summary>Encodes canonical ProtoJSON (WireValidator with normalize).</summary>
        public byte[] Encode(string type, JToken value)
        {
            var w = new Writer();
            WriteMessage(w, type, value, 0);
            return w.ToArray();
        }

        private void WriteMessage(Writer w, string type, JToken value, int depth)
        {
            if (depth > MaxDepth)
                throw Fail("Message too deep");
            value = FromSpecialJson(type, value);
            if (!(value is JObject obj))
                throw Fail(type + ": expected an object");
            foreach (var field in Fields(type))
            {
                var item = obj[field.Name];
                if (item == null || item.Type == JTokenType.Null)
                    continue;
                if (field.MapKey != null)
                {
                    foreach (var entry in ((JObject)item).Properties())
                    {
                        w.Tag(field.No, Len);
                        w.Nested(() =>
                        {
                            WriteScalar(w, 1, field.MapKey, null, MapKey(field.MapKey, entry.Name), depth + 1);
                            WriteScalar(w, 2, field.Kind, field.Type, entry.Value, depth + 1);
                        });
                    }
                }
                else if (field.List)
                {
                    var items = (JArray)item;
                    if (items.Count == 0)
                        continue;
                    if (field.Packed && Packable.Contains(field.Kind))
                    {
                        w.Tag(field.No, Len);
                        w.Nested(() => { foreach (var element in items) WriteValue(w, field.Kind, field.Type, element, depth + 1); });
                    }
                    else
                        foreach (var element in items)
                            WriteScalar(w, field.No, field.Kind, field.Type, element, depth + 1);
                }
                else if (field.Presence || field.Kind == "message" || !IsDefault(field, item))
                    WriteScalar(w, field.No, field.Kind, field.Type, item, depth + 1);
            }
        }

        private static JToken MapKey(string kind, string key)
        {
            if (kind == "bool")
                return new JValue(key == "true");
            if (kind == "string" || LongKinds.Contains(kind))
                return new JValue(key);
            return new JValue(long.Parse(key, CultureInfo.InvariantCulture));
        }

        private bool IsDefault(Field field, JToken value)
        {
            switch (field.Kind)
            {
                case "string": case "bytes": return value.Type == JTokenType.String && (string)value == "";
                case "bool": return value.Type == JTokenType.Boolean && !(bool)value;
                case "enum":
                    if (value.Type == JTokenType.Integer) return (long)value == 0;
                    return value.Type == JTokenType.String && enumNumbers.TryGetValue(field.Type, out var names) && names.TryGetValue((string)value, out var n) && n == 0;
                default:
                    if (value.Type == JTokenType.String) return (string)value == "0";
                    if (value.Type == JTokenType.Integer) return (long)value == 0;
                    if (value.Type == JTokenType.Float) { var d = (double)value; return d == 0 && !double.IsNegative(d); }
                    return false;
            }
        }

        private void WriteScalar(Writer w, int no, string kind, string type, JToken value, int depth)
        {
            w.Tag(no, WireType(kind));
            WriteValue(w, kind, type, value, depth);
        }

        private static long Int(JToken value) => value.Type == JTokenType.String ? long.Parse((string)value, CultureInfo.InvariantCulture) : (long)value;
        private static ulong UInt(JToken value) => value.Type == JTokenType.String ? ulong.Parse((string)value, CultureInfo.InvariantCulture) : (ulong)value;

        private static double Real(JToken value)
        {
            if (value.Type == JTokenType.String)
            {
                switch ((string)value)
                {
                    case "NaN": return double.NaN;
                    case "Infinity": return double.PositiveInfinity;
                    case "-Infinity": return double.NegativeInfinity;
                }
            }
            return (double)value;
        }

        private void WriteValue(Writer w, string kind, string type, JToken value, int depth)
        {
            switch (kind)
            {
                case "message": w.Nested(() => WriteMessage(w, type, value, depth)); return;
                case "string": w.Bytes(Encoding.UTF8.GetBytes((string)value)); return;
                case "bytes": w.Bytes(Convert.FromBase64String((string)value)); return;
                case "bool": w.Byte((bool)value ? (byte)1 : (byte)0); return;
                case "enum":
                    long number;
                    if (value.Type == JTokenType.Integer)
                        number = (long)value;
                    else if (enumNumbers.TryGetValue(type, out var names) && names.TryGetValue((string)value, out var n))
                        number = n;
                    else
                        throw Fail("Unknown enum value " + value);
                    w.Varint((ulong)number);
                    return;
                case "int32": case "int64": w.Varint((ulong)Int(value)); return;
                case "uint32": case "uint64": w.Varint(UInt(value)); return;
                case "sint32": { var n = (int)Int(value); w.Varint((uint)((n << 1) ^ (n >> 31))); return; }
                case "sint64": { var n = Int(value); w.Varint((ulong)((n << 1) ^ (n >> 63))); return; }
                case "fixed32": w.Fixed32((uint)UInt(value)); return;
                case "sfixed32": w.Fixed32((uint)(int)Int(value)); return;
                case "fixed64": w.Fixed64(UInt(value)); return;
                case "sfixed64": w.Fixed64((ulong)Int(value)); return;
                case "float": w.Fixed32((uint)BitConverter.SingleToInt32Bits((float)Real(value))); return;
                case "double": w.Fixed64((ulong)BitConverter.DoubleToInt64Bits(Real(value))); return;
            }
            throw Fail("Unsupported kind " + kind);
        }

        private static JToken FromSpecialJson(string type, JToken value)
        {
            if (!type.StartsWith("google.protobuf.", StringComparison.Ordinal))
                return value;
            if (type == "google.protobuf.Timestamp")
                return ParseTimestamp((string)value);
            if (type == "google.protobuf.Duration")
                return ParseDuration((string)value);
            if (Wrappers.ContainsKey(type))
                return new JObject { ["value"] = value };
            return value;
        }

        // ---- decoding ----

        /// <summary>Decodes binary Protobuf to what Go protojson.Marshal would produce.</summary>
        public JObject Decode(string type, byte[] data) => Decode(type, data, 0, data.Length);

        public JObject Decode(string type, byte[] data, int start, int end)
        {
            var result = new JObject();
            ReadMessage(type, new Reader(data, start, end), 0, result);
            return result;
        }

        private void ReadMessage(string type, Reader r, int depth, JObject into)
        {
            if (depth > MaxDepth)
                throw Fail("Message too deep");
            Fields(type);
            var fields = byNumber[type];
            while (r.Pos < r.End)
            {
                var tag = r.Varint();
                var no = tag >> 3;
                var wire = (int)(tag & 7);
                if (no == 0 || no > int.MaxValue)
                    throw Fail("Invalid field number");
                if (!fields.TryGetValue((int)no, out var field))
                {
                    r.Skip(wire, depth);
                    continue;
                }
                if (field.MapKey != null)
                {
                    if (wire != Len)
                        throw Fail("Wrong wire type");
                    var (start, end) = r.Length();
                    var entry = new Reader(r.Data, start, end);
                    JToken key = null, value = null;
                    while (entry.Pos < entry.End)
                    {
                        var entryTag = entry.Varint();
                        var entryWire = (int)(entryTag & 7);
                        if ((entryTag >> 3) == 1)
                            key = ReadValue(entry, entryWire, field.MapKey, null, depth + 1);
                        else if ((entryTag >> 3) == 2)
                            value = ReadValue(entry, entryWire, field.Kind, field.Type, depth + 1);
                        else
                            entry.Skip(entryWire, depth);
                    }
                    if (!(into[field.Name] is JObject map))
                        into[field.Name] = map = new JObject();
                    var keyText = key == null ? (field.MapKey == "bool" ? "false" : field.MapKey == "string" ? "" : "0")
                        : key.Type == JTokenType.Boolean ? ((bool)key ? "true" : "false") : key.Type == JTokenType.String ? (string)key : ((long)key).ToString(CultureInfo.InvariantCulture);
                    map[keyText] = value ?? DefaultValue(field);
                    continue;
                }
                if (field.List)
                {
                    if (!(into[field.Name] is JArray list))
                        into[field.Name] = list = new JArray();
                    if (wire == Len && Packable.Contains(field.Kind))
                    {
                        var (start, end) = r.Length();
                        var packed = new Reader(r.Data, start, end);
                        while (packed.Pos < packed.End)
                            list.Add(ReadValue(packed, WireType(field.Kind), field.Kind, field.Type, depth + 1));
                    }
                    else
                        list.Add(ReadValue(r, wire, field.Kind, field.Type, depth + 1));
                    continue;
                }
                if (field.Oneof != 0)
                    foreach (var other in fields.Values)
                        if (other.Oneof == field.Oneof && other != field)
                            into.Remove(other.Name);
                if (field.Kind == "message" && wire == Len && into[field.Name] is JObject existing && !IsSpecial(field.Type))
                {
                    var (start, end) = r.Length();
                    ReadMessage(field.Type, new Reader(r.Data, start, end), depth + 1, existing);
                    continue;
                }
                into[field.Name] = ReadValue(r, wire, field.Kind, field.Type, depth + 1);
            }
            foreach (var field in fields.Values)
            {
                var value = into[field.Name];
                if (value != null && !field.List && field.MapKey == null && !field.Presence && field.Kind != "message" && IsDefault(field, value))
                    into.Remove(field.Name);
            }
        }

        private static bool IsSpecial(string type) => type == "google.protobuf.Timestamp" || type == "google.protobuf.Duration" || Wrappers.ContainsKey(type) || type == "google.protobuf.Empty";

        private JToken DefaultValue(Field field)
        {
            switch (field.Kind)
            {
                case "message": return ToSpecialJson(field.Type, new JObject());
                case "string": case "bytes": return new JValue("");
                case "bool": return new JValue(false);
                case "enum": return enumNames.TryGetValue(field.Type, out var names) && names.TryGetValue(0, out var name) ? new JValue(name) : new JValue(0);
                default: return LongKinds.Contains(field.Kind) ? new JValue("0") : new JValue(0);
            }
        }

        private static JToken Real(double value)
        {
            if (double.IsNaN(value)) return new JValue("NaN");
            if (double.IsPositiveInfinity(value)) return new JValue("Infinity");
            if (double.IsNegativeInfinity(value)) return new JValue("-Infinity");
            return new JValue(value);
        }

        private JToken ReadValue(Reader r, int wire, string kind, string type, int depth)
        {
            if (wire != WireType(kind))
                throw Fail("Wrong wire type " + wire + " for " + kind);
            switch (kind)
            {
                case "message":
                {
                    var (start, end) = r.Length();
                    var message = new JObject();
                    ReadMessage(type, new Reader(r.Data, start, end), depth, message);
                    return ToSpecialJson(type, message);
                }
                case "string":
                {
                    var (start, end) = r.Length();
                    try { return new JValue(StrictUtf8.GetString(r.Data, start, end - start)); }
                    catch (DecoderFallbackException) { throw Fail("Invalid UTF-8"); }
                }
                case "bytes": { var (start, end) = r.Length(); return new JValue(Convert.ToBase64String(r.Data, start, end - start)); }
                case "bool": return new JValue(r.Varint() != 0);
                case "enum":
                {
                    var n = (int)r.Varint();
                    return enumNames.TryGetValue(type, out var names) && names.TryGetValue(n, out var name) ? new JValue(name) : new JValue(n);
                }
                case "int32": return new JValue((int)r.Varint());
                case "uint32": return new JValue((uint)r.Varint());
                case "sint32": { var n = (uint)r.Varint(); return new JValue((int)(n >> 1) ^ -(int)(n & 1)); }
                case "int64": return new JValue(((long)r.Varint()).ToString(CultureInfo.InvariantCulture));
                case "uint64": return new JValue(r.Varint().ToString(CultureInfo.InvariantCulture));
                case "sint64": { var n = r.Varint(); return new JValue(((long)(n >> 1) ^ -(long)(n & 1)).ToString(CultureInfo.InvariantCulture)); }
                case "fixed32": return new JValue(r.Fixed32());
                case "sfixed32": return new JValue((int)r.Fixed32());
                case "fixed64": return new JValue(r.Fixed64().ToString(CultureInfo.InvariantCulture));
                case "sfixed64": return new JValue(((long)r.Fixed64()).ToString(CultureInfo.InvariantCulture));
                case "float": return Real(BitConverter.Int32BitsToSingle((int)r.Fixed32()));
                case "double": return Real(BitConverter.Int64BitsToDouble((long)r.Fixed64()));
            }
            throw Fail("Unsupported kind " + kind);
        }

        private JToken ToSpecialJson(string type, JObject value)
        {
            if (!type.StartsWith("google.protobuf.", StringComparison.Ordinal))
                return value;
            long seconds = value["seconds"] == null ? 0 : long.Parse((string)value["seconds"], CultureInfo.InvariantCulture);
            int nanos = value["nanos"] == null ? 0 : (int)value["nanos"];
            if (type == "google.protobuf.Timestamp")
                return new JValue(FormatTimestamp(seconds, nanos));
            if (type == "google.protobuf.Duration")
                return new JValue(FormatDuration(seconds, nanos));
            if (Wrappers.TryGetValue(type, out var kind))
                return value["value"] ?? DefaultValue(new Field { Kind = kind, Name = "value" });
            return value;
        }

        private static readonly UTF8Encoding StrictUtf8 = new UTF8Encoding(false, true);

        // ---- Timestamp / Duration (Go protojson formats) ----

        private static long CivilDays(long year, long month, long day)
        {
            var y = year + 400 - (month <= 2 ? 1 : 0);
            var era = FloorDiv(y, 400);
            var yoe = y - era * 400;
            var doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
            return era * 146097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719468 - 146097;
        }

        private static long FloorDiv(long a, long b) => a >= 0 ? a / b : -((-a + b - 1) / b);

        private static string Fraction(int nanos)
        {
            if (nanos == 0)
                return "";
            var digits = nanos.ToString("D9", CultureInfo.InvariantCulture);
            return "." + (nanos % 1000000 == 0 ? digits.Substring(0, 3) : nanos % 1000 == 0 ? digits.Substring(0, 6) : digits);
        }

        private static readonly Regex TimestampPattern = new Regex("^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:[.,]([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$", RegexOptions.CultureInvariant);

        private static JObject ParseTimestamp(string text)
        {
            var m = TimestampPattern.Match(text ?? "");
            if (!m.Success)
                throw Fail("Invalid timestamp");
            Func<int, long> g = i => long.Parse(m.Groups[i].Value, CultureInfo.InvariantCulture);
            var seconds = CivilDays(g(1), g(2), g(3)) * 86400 + g(4) * 3600 + g(5) * 60 + g(6);
            if (m.Groups[8].Success)
                seconds -= (m.Groups[8].Value == "-" ? -1 : 1) * (g(9) * 3600 + g(10) * 60);
            var nanos = m.Groups[7].Success ? int.Parse((m.Groups[7].Value + "00000000").Substring(0, 9), CultureInfo.InvariantCulture) : 0;
            return new JObject { ["seconds"] = seconds.ToString(CultureInfo.InvariantCulture), ["nanos"] = nanos };
        }

        private static string FormatTimestamp(long seconds, int nanos)
        {
            if (seconds < -62135596800 || seconds > 253402300799 || nanos < 0 || nanos > 999999999)
                throw Fail("Timestamp out of range");
            var time = DateTimeOffset.FromUnixTimeSeconds(seconds).UtcDateTime;
            return time.ToString("yyyy-MM-dd'T'HH:mm:ss", CultureInfo.InvariantCulture) + Fraction(nanos) + "Z";
        }

        private static readonly Regex DurationPattern = new Regex("^(-)?([0-9]+)(?:\\.([0-9]{1,9}))?s$", RegexOptions.CultureInvariant);

        private static JObject ParseDuration(string text)
        {
            var m = DurationPattern.Match(text ?? "");
            if (!m.Success)
                throw Fail("Invalid duration");
            var negative = m.Groups[1].Success;
            var seconds = long.Parse(m.Groups[2].Value, CultureInfo.InvariantCulture);
            var nanos = m.Groups[3].Success ? int.Parse((m.Groups[3].Value + "00000000").Substring(0, 9), CultureInfo.InvariantCulture) : 0;
            return new JObject { ["seconds"] = (negative ? -seconds : seconds).ToString(CultureInfo.InvariantCulture), ["nanos"] = negative ? -nanos : nanos };
        }

        private static string FormatDuration(long seconds, int nanos)
        {
            if (seconds < -315576000000 || seconds > 315576000000 || nanos < -999999999 || nanos > 999999999 || (seconds > 0 && nanos < 0) || (seconds < 0 && nanos > 0))
                throw Fail("Duration out of range");
            var negative = seconds < 0 || nanos < 0;
            return (negative ? "-" : "") + Math.Abs(seconds).ToString(CultureInfo.InvariantCulture) + Fraction(Math.Abs(nanos)) + "s";
        }

        // ---- wire primitives ----

        internal sealed class Writer
        {
            private byte[] buffer = new byte[64];
            private int pos;

            private void Ensure(int n)
            {
                if (pos + n <= buffer.Length)
                    return;
                var size = buffer.Length * 2;
                while (size < pos + n)
                    size *= 2;
                Array.Resize(ref buffer, size);
            }

            public void Byte(byte b) { Ensure(1); buffer[pos++] = b; }

            public void Varint(ulong v)
            {
                Ensure(10);
                while (v > 0x7f)
                {
                    buffer[pos++] = (byte)(v | 0x80);
                    v >>= 7;
                }
                buffer[pos++] = (byte)v;
            }

            public void Tag(int no, int wire) => Varint(((ulong)no << 3) | (uint)wire);
            public void Fixed32(uint v) { Ensure(4); for (var i = 0; i < 4; i++) buffer[pos++] = (byte)(v >> (8 * i)); }
            public void Fixed64(ulong v) { Ensure(8); for (var i = 0; i < 8; i++) buffer[pos++] = (byte)(v >> (8 * i)); }
            public void Bytes(byte[] data) { Varint((ulong)data.Length); Ensure(data.Length); Buffer.BlockCopy(data, 0, buffer, pos, data.Length); pos += data.Length; }

            /// <summary>Writes body() with a length prefix.</summary>
            public void Nested(Action body)
            {
                var start = pos;
                Ensure(5);
                pos += 5;
                body();
                var length = pos - start - 5;
                var prefix = new byte[5];
                var n = 0;
                var v = (uint)length;
                while (v > 0x7f)
                {
                    prefix[n++] = (byte)(v | 0x80);
                    v >>= 7;
                }
                prefix[n++] = (byte)v;
                Buffer.BlockCopy(buffer, start + 5, buffer, start + n, length);
                Buffer.BlockCopy(prefix, 0, buffer, start, n);
                pos = start + n + length;
            }

            public byte[] ToArray()
            {
                var result = new byte[pos];
                Buffer.BlockCopy(buffer, 0, result, 0, pos);
                return result;
            }
        }

        internal sealed class Reader
        {
            public readonly byte[] Data;
            public readonly int End;
            public int Pos;

            public Reader(byte[] data, int start, int end)
            {
                Data = data;
                Pos = start;
                End = end;
            }

            public ulong Varint()
            {
                ulong result = 0;
                for (var i = 0; i < 10; i++)
                {
                    if (Pos >= End)
                        throw Fail("Truncated varint");
                    var b = Data[Pos++];
                    if (i == 9 && b > 1)
                        throw Fail("Varint overflows 64 bits");
                    result |= (ulong)(b & 0x7f) << (7 * i);
                    if (b < 0x80)
                        return result;
                }
                throw Fail("Varint too long");
            }

            public void Advance(int n)
            {
                if (n < 0 || Pos + n > End)
                    throw Fail("Truncated field");
                Pos += n;
            }

            public uint Fixed32() { Advance(4); uint v = 0; for (var i = 0; i < 4; i++) v |= (uint)Data[Pos - 4 + i] << (8 * i); return v; }
            public ulong Fixed64() { Advance(8); ulong v = 0; for (var i = 0; i < 8; i++) v |= (ulong)Data[Pos - 8 + i] << (8 * i); return v; }

            public (int, int) Length()
            {
                var n = Varint();
                if (n > int.MaxValue)
                    throw Fail("Length out of range");
                var start = Pos;
                Advance((int)n);
                return (start, Pos);
            }

            public void Skip(int wire, int depth)
            {
                switch (wire)
                {
                    case WireVarint: Varint(); return;
                    case I64: Advance(8); return;
                    case Len: Length(); return;
                    case I32: Advance(4); return;
                    case 3:
                        if (depth > MaxDepth)
                            throw Fail("Message too deep");
                        while (true)
                        {
                            var tag = Varint();
                            if ((tag & 7) == 4)
                                return;
                            Skip((int)(tag & 7), depth + 1);
                        }
                    default: throw Fail("Invalid wire type");
                }
            }
        }
    }

    public enum RealtimeEncoding
    {
        Proto,
        Json
    }

    /// <summary>One realtime frame (see doc/realtime.md). Payload is binary Protobuf or a ProtoJSON object.</summary>
    public sealed class RealtimeFrame
    {
        public const int Hello = 1, Welcome = 2, Request = 3, Response = 4, Push = 5, Notify = 6, Ping = 7, Pong = 8, Ack = 9, Close = 10;
        private static readonly string[] Names = { null, "hello", "welcome", "request", "response", "push", "notify", "ping", "pong", "ack", "close" };

        public int Kind;
        public ulong Seq, AckSeq, ReplyTo;
        public uint Message;
        public byte[] Payload;
        public JObject JsonPayload;
        public string ErrorCode, ErrorMessage;
        public bool ErrorRetryable, HasError;
        public string HelloVersion, HelloSchemaHash, HelloToken, HelloSessionId, HelloResumeToken;
        public bool HasWelcome, WelcomeResumed;
        public string WelcomeSessionId, WelcomeResumeToken;
        public uint WelcomeHeartbeatMS, WelcomeResumeWindowMS;

        public string KindName => Kind > 0 && Kind < Names.Length ? Names[Kind] : "kind(" + Kind + ")";

        private static Exception Malformed(string why) => new ProtocolException("protocol_error", "Malformed frame: " + why, true);

        // ---- binary ----

        public byte[] EncodeBinary()
        {
            var w = new ProtobufCodec.Writer();
            void Number(int no, ulong v) { if (v != 0) { w.Tag(no, 0); w.Varint(v); } }
            void Text(ProtobufCodec.Writer target, int no, string s) { if (!string.IsNullOrEmpty(s)) { target.Tag(no, 2); target.Bytes(Encoding.UTF8.GetBytes(s)); } }
            Number(1, (ulong)Kind);
            Number(2, Seq);
            Number(3, AckSeq);
            Number(4, Message);
            Number(5, ReplyTo);
            if (Payload != null && Payload.Length > 0)
            {
                w.Tag(6, 2);
                w.Bytes(Payload);
            }
            if (HasError)
            {
                w.Tag(7, 2);
                w.Nested(() =>
                {
                    Text(w, 1, ErrorCode);
                    Text(w, 2, ErrorMessage);
                    if (ErrorRetryable) { w.Tag(3, 0); w.Varint(1); }
                });
            }
            if (Kind == Hello)
            {
                w.Tag(8, 2);
                w.Nested(() =>
                {
                    Text(w, 1, HelloVersion);
                    Text(w, 2, HelloSchemaHash);
                    Text(w, 3, HelloToken);
                    Text(w, 4, HelloSessionId);
                    Text(w, 5, HelloResumeToken);
                });
            }
            return w.ToArray();
        }

        private static void Walk(byte[] data, int start, int end, Action<ulong, int, ulong, int, int> visit)
        {
            var r = new ProtobufCodec.Reader(data, start, end);
            while (r.Pos < r.End)
            {
                var tag = r.Varint();
                var no = tag >> 3;
                var wire = (int)(tag & 7);
                if (no == 0)
                    throw Malformed("field number");
                if (wire == 0)
                    visit(no, wire, r.Varint(), 0, 0);
                else if (wire == 2)
                {
                    var (s, e) = r.Length();
                    visit(no, wire, 0, s, e);
                }
                else if (wire == 1 || wire == 5)
                    r.Advance(wire == 1 ? 8 : 4);
                else
                    throw Malformed("wire type");
            }
        }

        private static readonly UTF8Encoding StrictUtf8 = new UTF8Encoding(false, true);

        public static RealtimeFrame DecodeBinary(byte[] data)
        {
            var f = new RealtimeFrame();
            try
            {
                Walk(data, 0, data.Length, (no, wire, v, s, e) =>
                {
                    if ((no <= 5 && wire != 0) || (no >= 6 && no <= 9 && wire != 2))
                        throw Malformed("wire type of field " + no);
                    switch (no)
                    {
                        case 1: f.Kind = v > 100 ? -1 : (int)v; break;
                        case 2: f.Seq = v; break;
                        case 3: f.AckSeq = v; break;
                        case 4: if (v > 65535) throw Malformed("message id"); f.Message = (uint)v; break;
                        case 5: f.ReplyTo = v; break;
                        case 6: f.Payload = new byte[e - s]; Buffer.BlockCopy(data, s, f.Payload, 0, e - s); break;
                        case 7:
                            f.HasError = true;
                            Walk(data, s, e, (n, w2, x, a, b) =>
                            {
                                if ((n == 1 || n == 2) && w2 != 2 || n == 3 && w2 != 0) throw Malformed("error");
                                if (n == 1) f.ErrorCode = StrictUtf8.GetString(data, a, b - a);
                                else if (n == 2) f.ErrorMessage = StrictUtf8.GetString(data, a, b - a);
                                else if (n == 3) f.ErrorRetryable = x != 0;
                            });
                            break;
                        case 8: f.HelloVersion = ""; break;
                        case 9:
                            f.HasWelcome = true;
                            Walk(data, s, e, (n, w2, x, a, b) =>
                            {
                                if ((n == 1 || n == 2) && w2 != 2 || n >= 3 && n <= 5 && w2 != 0) throw Malformed("welcome");
                                if (n == 1) f.WelcomeSessionId = StrictUtf8.GetString(data, a, b - a);
                                else if (n == 2) f.WelcomeResumeToken = StrictUtf8.GetString(data, a, b - a);
                                else if (n == 3) f.WelcomeResumed = x != 0;
                                else if (n == 4) f.WelcomeHeartbeatMS = x > uint.MaxValue ? throw Malformed("heartbeat") : (uint)x;
                                else if (n == 5) f.WelcomeResumeWindowMS = x > uint.MaxValue ? 0 : (uint)x;
                            });
                            break;
                    }
                });
            }
            catch (ProtocolException error) when (error.Code != "protocol_error")
            {
                throw Malformed(error.Message);
            }
            catch (DecoderFallbackException)
            {
                throw Malformed("invalid UTF-8");
            }
            return f;
        }

        // ---- JSON ----

        public string EncodeJson()
        {
            var o = new JObject { ["kind"] = KindName };
            if (Seq != 0) o["seq"] = Seq.ToString(CultureInfo.InvariantCulture);
            if (AckSeq != 0) o["ack"] = AckSeq.ToString(CultureInfo.InvariantCulture);
            if (Message != 0) o["message"] = Message;
            if (ReplyTo != 0) o["replyTo"] = ReplyTo.ToString(CultureInfo.InvariantCulture);
            if (JsonPayload != null) o["payload"] = JsonPayload;
            if (HasError) o["error"] = new JObject { ["code"] = ErrorCode, ["message"] = ErrorMessage ?? "", ["retryable"] = ErrorRetryable };
            if (Kind == Hello)
            {
                var h = new JObject { ["protocolVersion"] = HelloVersion };
                if (!string.IsNullOrEmpty(HelloSchemaHash)) h["schemaHash"] = HelloSchemaHash;
                if (!string.IsNullOrEmpty(HelloToken)) h["token"] = HelloToken;
                if (!string.IsNullOrEmpty(HelloSessionId)) h["sessionId"] = HelloSessionId;
                if (!string.IsNullOrEmpty(HelloResumeToken)) h["resumeToken"] = HelloResumeToken;
                o["hello"] = h;
            }
            return o.ToString(Formatting.None);
        }

        private static readonly Regex Decimal = new Regex("^[1-9][0-9]{0,19}$", RegexOptions.CultureInvariant);
        private static readonly HashSet<string> Members = new HashSet<string> { "kind", "seq", "ack", "message", "replyTo", "payload", "error", "hello", "welcome" };

        private static ulong DecimalMember(JObject o, string name)
        {
            var token = o[name];
            if (token == null)
                return 0;
            if (token.Type != JTokenType.String || !Decimal.IsMatch((string)token) || !ulong.TryParse((string)token, NumberStyles.None, CultureInfo.InvariantCulture, out var value))
                throw Malformed(name);
            return value;
        }

        public static RealtimeFrame DecodeJson(string text)
        {
            JObject o;
            try
            {
                o = Json.Parse(text) as JObject;
            }
            catch (ProtocolException)
            {
                throw Malformed("invalid JSON");
            }
            if (o == null)
                throw Malformed("not an object");
            foreach (var p in o.Properties())
                if (!Members.Contains(p.Name))
                    throw Malformed("unknown member " + p.Name);
            var f = new RealtimeFrame { Kind = -1 };
            if (o["kind"]?.Type == JTokenType.String)
                f.Kind = Array.IndexOf(Names, (string)o["kind"]);
            f.Seq = DecimalMember(o, "seq");
            f.AckSeq = DecimalMember(o, "ack");
            f.ReplyTo = DecimalMember(o, "replyTo");
            if (o["message"] != null)
            {
                if (o["message"].Type != JTokenType.Integer || (long)o["message"] < 0 || (long)o["message"] > 65535)
                    throw Malformed("message id");
                f.Message = (uint)(long)o["message"];
            }
            if (o["payload"] != null)
            {
                if (!(o["payload"] is JObject payload))
                    throw Malformed("payload");
                f.JsonPayload = payload;
            }
            if (o["error"] != null)
            {
                if (!(o["error"] is JObject e) || e["code"]?.Type != JTokenType.String || (e["message"] != null && e["message"].Type != JTokenType.String) || (e["retryable"] != null && e["retryable"].Type != JTokenType.Boolean))
                    throw Malformed("error");
                f.HasError = true;
                f.ErrorCode = (string)e["code"];
                f.ErrorMessage = (string)e["message"] ?? "";
                f.ErrorRetryable = e["retryable"] != null && (bool)e["retryable"];
            }
            if (o["welcome"] != null)
            {
                if (!(o["welcome"] is JObject w) || w["sessionId"]?.Type != JTokenType.String || w["resumeToken"]?.Type != JTokenType.String || w["heartbeatMS"]?.Type != JTokenType.Integer)
                    throw Malformed("welcome");
                f.HasWelcome = true;
                f.WelcomeSessionId = (string)w["sessionId"];
                f.WelcomeResumeToken = (string)w["resumeToken"];
                f.WelcomeResumed = w["resumed"]?.Type == JTokenType.Boolean && (bool)w["resumed"];
                f.WelcomeHeartbeatMS = (uint)Math.Min(Math.Max((long)w["heartbeatMS"], 0), uint.MaxValue);
            }
            if (o["hello"] != null)
                f.HelloVersion = "";
            return f;
        }

        /// <summary>Server-to-client envelope rules shared with Go and TypeScript.</summary>
        public void ValidateFromServer()
        {
            var hasPayload = Payload != null || JsonPayload != null;
            var control = Seq == 0 && Message == 0 && ReplyTo == 0 && !hasPayload && HelloVersion == null;
            switch (Kind)
            {
                case Welcome:
                    if (!control || !HasWelcome || string.IsNullOrEmpty(WelcomeSessionId) || string.IsNullOrEmpty(WelcomeResumeToken) || WelcomeHeartbeatMS == 0 || HasError) throw Malformed("welcome");
                    break;
                case Response:
                    if (Seq == 0 || Message == 0 || ReplyTo == 0 || HelloVersion != null || HasWelcome || (HasError && hasPayload)) throw Malformed("response");
                    if (HasError && string.IsNullOrEmpty(ErrorCode)) throw Malformed("error code");
                    break;
                case Push:
                    if (Message == 0 || ReplyTo != 0 || HasError || HelloVersion != null || HasWelcome) throw Malformed("push");
                    break;
                case Pong:
                case Ack:
                    if (!control || HasError || HasWelcome) throw Malformed(KindName);
                    break;
                case Close:
                    if (!control || !HasError || string.IsNullOrEmpty(ErrorCode) || HasWelcome) throw Malformed("close");
                    break;
                default:
                    throw Malformed("unexpected " + KindName + " from server");
            }
        }
    }
}
