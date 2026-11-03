using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    // Validates ProtoJSON with Go protojson's input rules. Depth equals the JSON
    // nesting depth of each value and every JSON value counts at most once, so for
    // parsed text the JsonSyntax budget is always the binding one.
    public sealed class WireValidator
    {
        // Shared by every TabForge runtime. Network messages are also capped at
        // 1 MiB; data bundle files are not, so they get the larger node budget.
        public const int MaxDepth = 64;
        public const int ProtocolMaxNodes = 100000;
        public const int DataMaxNodes = 10000000;

        private static readonly Dictionary<string, string> Wrappers = new Dictionary<string, string>
        {
            { "DoubleValue", "double" }, { "FloatValue", "float" }, { "Int64Value", "int64" }, { "UInt64Value", "uint64" },
            { "Int32Value", "int32" }, { "UInt32Value", "uint32" }, { "BoolValue", "bool" }, { "StringValue", "string" }, { "BytesValue", "bytes" }
        };
        private static readonly Regex JsonNumber = new Regex(@"\A-?(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?\z", RegexOptions.CultureInvariant);
        private const string Base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        private static readonly Regex TimestampPattern = new Regex(@"\A([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:([.,])([0-9]+))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))\z", RegexOptions.CultureInvariant);
        private static readonly Regex DurationPattern = new Regex(@"\A[+-]?(0|[1-9][0-9]*)?(\.[0-9]{0,9})?s\z", RegexOptions.CultureInvariant);
        // 2^128 - 2^104 is the largest float32; decimals from 2^128 - 2^103 up overflow it.
        private const double Float32Limit = 3.4028235677973366e38;
        private const string Float32Halfway = "340282356779733661637539395458142568448";
        private readonly JObject schema;
        private readonly int maxDepth, maxNodes;
        private readonly bool normalize;
        private int nodes;
        // Message name -> original Proto field name -> JSON name.
        private readonly Dictionary<string, Dictionary<string, string>> aliases = new Dictionary<string, Dictionary<string, string>>(StringComparer.Ordinal);

        public WireValidator(JObject schema) : this(schema, MaxDepth, ProtocolMaxNodes, false)
        {
        }

        // normalize rewrites accepted values in place to canonical ProtoJSON (data loaders).
        public WireValidator(JObject schema, int maxDepth, int maxNodes, bool normalize)
        {
            this.schema = schema;
            this.maxDepth = maxDepth;
            this.maxNodes = maxNodes;
            this.normalize = normalize;
        }

        private static void Fail(string path, string expected)
        {
            throw new ProtocolException("invalid_message", path + ": expected " + expected);
        }

        private void Budget(string path, int depth)
        {
            if (depth > maxDepth || ++nodes > maxNodes)
                Fail(path, "bounded message depth/size");
        }

        // Returns the value, or its canonical replacement when normalizing.
        public JToken Validate(string type, JToken value)
        {
            nodes = 0;
            var result = Message(type, value, "$", 0, false, false);
            return normalize ? result : value;
        }

        // Canonical decimal string; 64-bit integers are emitted this way.
        public static bool IntegerString(string value, bool signed, int bits = 64)
        {
            if (value == null || value == "-0" || !Regex.IsMatch(value, @"\A-?(?:0|[1-9][0-9]*)\z", RegexOptions.CultureInvariant))
                return false;
            var negative = value.StartsWith("-", StringComparison.Ordinal);
            return InRange(negative ? value.Substring(1) : value, negative, signed, bits);
        }

        private static bool InRange(string digits, bool negative, bool signed, int bits)
        {
            if (negative && !signed)
                return false;
            var max = bits == 32 ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295") : (signed ? (negative ? "9223372036854775808" : "9223372036854775807") : "18446744073709551615");
            return digits.Length < max.Length || (digits.Length == max.Length && string.CompareOrdinal(digits, max) <= 0);
        }

        private static string StripZeros(string digits)
        {
            var trimmed = digits.TrimStart('0');
            return trimmed.Length == 0 ? "0" : trimmed;
        }

        // Go protojson integer input: JSON number text, exact after shifting the
        // exponent (1.0, 1e2, 100e-2 are integers; 1.5, 1e-2 are not), including
        // normalizeToIntString's 20-digit cap. Returns the canonical decimal or null.
        public static string ProtoInteger(string text, bool signed, int bits)
        {
            if (text == null)
                return null;
            var match = JsonNumber.Match(text);
            if (!match.Success)
                return null;
            var intp = match.Groups[1].Value == "0" ? "" : match.Groups[1].Value;
            var frac = match.Groups[2].Value.TrimEnd('0');
            if (intp.Length == 0 && frac.Length == 0)
                return "0";
            long exponent = 0;
            if (match.Groups[3].Success)
            {
                var exp = match.Groups[3].Value;
                var negativeExp = exp.StartsWith("-", StringComparison.Ordinal);
                var magnitude = exp.TrimStart('+', '-').TrimStart('0');
                if (magnitude.Length > 10)
                    return null;
                exponent = magnitude.Length == 0 ? 0 : long.Parse(magnitude, CultureInfo.InvariantCulture);
                if (negativeExp)
                    exponent = -exponent;
                if (exponent < int.MinValue || exponent > int.MaxValue)
                    return null;
            }

            string digits;
            if (exponent >= 0)
            {
                if (frac.Length > exponent || intp.Length + exponent > 20)
                    return null;
                digits = intp + frac + new string('0', (int)exponent - frac.Length);
            }
            else
            {
                var index = intp.Length + exponent;
                if (frac.Length > 0 || index < 0)
                    return null;
                for (var i = (int)index; i < intp.Length; i++)
                    if (intp[i] != '0')
                        return null;
                digits = intp.Substring(0, (int)index);
            }

            digits = StripZeros(digits);
            var negative = text.StartsWith("-", StringComparison.Ordinal);
            return InRange(digits, negative, signed, bits) ? (negative ? "-" : "") + digits : null;
        }

        // Go strconv.ParseInt/ParseUint(key, 10, bits) for map keys; returns the canonical key or null.
        public static string ProtoMapKey(string key, string kind)
        {
            if (kind == "string")
                return key;
            if (kind == "bool")
                return key == "true" || key == "false" ? key : null;
            if (!IntegerKind(kind, out var signed, out var bits))
                return null;
            var match = Regex.Match(key, @"\A([+-]?)([0-9]+)\z", RegexOptions.CultureInvariant);
            if (!match.Success || (match.Groups[1].Length > 0 && !signed))
                return null;
            var digits = StripZeros(match.Groups[2].Value);
            var negative = match.Groups[1].Value == "-" && digits != "0";
            return InRange(digits, negative, signed, bits) ? (negative ? "-" : "") + digits : null;
        }

        // Go protojson bytes input: URL alphabet when "-" or "_" occurs, padded iff the
        // length is a multiple of 4, CR/LF ignored, unused bits ignored. Returns padded
        // standard Base64 or null.
        public static string ProtoBytes(string value)
        {
            var url = value.IndexOf('-') >= 0 || value.IndexOf('_') >= 0;
            var text = value.Replace("\r", "").Replace("\n", "");
            var alphabet = url ? "A-Za-z0-9_-" : "A-Za-z0-9+/";
            var pattern = value.Length % 4 == 0
                ? @"\A(?:[" + alphabet + "]{4})*(?:[" + alphabet + "]{2}==|[" + alphabet + @"]{3}=)?\z"
                : @"\A(?:[" + alphabet + "]{4})*(?:[" + alphabet + @"]{2,3})?\z";
            if (!Regex.IsMatch(text, pattern, RegexOptions.CultureInvariant))
                return null;
            var body = text.TrimEnd('=').Replace('-', '+').Replace('_', '/');
            var rest = body.Length % 4;
            if (rest != 0)
                body = body.Substring(0, body.Length - 1) + Base64Alphabet[Base64Alphabet.IndexOf(body[body.Length - 1]) & (rest == 2 ? 0x30 : 0x3c)] + (rest == 2 ? "==" : "=");
            return body;
        }

        // Days from 1970-01-01, proleptic Gregorian; year 0 is allowed (offsets can reach year 1).
        private static long CivilDays(int year, int month, int day)
        {
            long y = year + 400 - (month <= 2 ? 1 : 0), era = y / 400, yoe = y - era * 400;
            long doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
            return era * 146097 + yoe * 365 + yoe / 4 - yoe / 100 + doy - 719468 - 146097;
        }

        private static int DaysInMonth(int year, int month)
        {
            if (month == 2)
                return year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) ? 29 : 28;
            return new[] { 31, 0, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31 }[month - 1];
        }

        // Go protojson Timestamp input: time.Parse(time.RFC3339Nano), then 0001-01-01 to
        // 9999-12-31 in UTC and at most 9 digits after ".". Like Go: any offset (hours to 24,
        // minutes to 60), a one-digit hour, and "," before a fraction of any length.
        public static bool ProtoTimestamp(string value)
        {
            var match = value == null ? null : TimestampPattern.Match(value);
            if (match == null || !match.Success)
                return false;
            Func<int, int> group = index => int.Parse(match.Groups[index].Value, CultureInfo.InvariantCulture);
            int year = group(1), month = group(2), day = group(3), hour = group(4), minute = group(5), second = group(6);
            if ((match.Groups[7].Value == "." && match.Groups[8].Length > 9) || month < 1 || month > 12 || day < 1 || day > DaysInMonth(year, month) || hour > 23 || minute > 59 || second > 59)
                return false;
            long offset = 0;
            if (match.Groups[9].Success)
            {
                int hours = group(10), minutes = group(11);
                if (hours > 24 || minutes > 60)
                    return false;
                offset = (match.Groups[9].Value == "-" ? -1 : 1) * (hours * 3600L + minutes * 60L);
            }
            var seconds = CivilDays(year, month, day) * 86400L + hour * 3600L + minute * 60L + second - offset;
            return seconds >= -62135596800L && seconds <= 253402300799L;
        }

        // Go protojson Duration input: optional sign, integer and/or "." with up to 9 digits
        // ("1.s", ".5s" and ".s" are accepted), then "s", within 315576000000 seconds.
        public static bool ProtoDuration(string value)
        {
            var match = value == null ? null : DurationPattern.Match(value);
            if (match == null || !match.Success || (!match.Groups[1].Success && !match.Groups[2].Success))
                return false;
            var seconds = match.Groups[1].Value;
            return seconds.Length < 12 || (seconds.Length == 12 && string.CompareOrdinal(seconds, "315576000000") <= 0);
        }

        // Whether JSON number text is below 2^128 - 2^103 in magnitude, compared exactly.
        public static bool BelowFloat32Limit(string text)
        {
            var match = text == null ? null : JsonNumber.Match(text);
            if (match == null || !match.Success)
                return false;
            // The value is 0.<digits> * 10^exponent.
            var digits = match.Groups[1].Value + match.Groups[2].Value;
            long exponent = match.Groups[1].Length;
            if (match.Groups[3].Success)
            {
                if (!long.TryParse(match.Groups[3].Value, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out var shift))
                    return match.Groups[3].Value.StartsWith("-", StringComparison.Ordinal);
                exponent += shift;
            }
            var significant = digits.TrimStart('0');
            exponent -= digits.Length - significant.Length;
            significant = significant.TrimEnd('0');
            return significant.Length == 0 || exponent < Float32Halfway.Length || (exponent == Float32Halfway.Length && string.CompareOrdinal(significant, Float32Halfway) < 0);
        }

        private static bool IntegerKind(string kind, out bool signed, out int bits)
        {
            signed = kind == "int32" || kind == "sint32" || kind == "sfixed32" || kind == "int64" || kind == "sint64" || kind == "sfixed64";
            bits = kind.EndsWith("32", StringComparison.Ordinal) ? 32 : 64;
            return signed || kind == "uint32" || kind == "fixed32" || kind == "uint64" || kind == "fixed64";
        }

        // JSON number text of a token: exact for integers and raw literals.
        private static string NumberText(JToken value)
        {
            if (value == null)
                return null;
            if (value.Type == JTokenType.Raw)
                return ((JValue)value).Value as string;
            if (value.Type == JTokenType.Integer)
                return Convert.ToString(((JValue)value).Value, CultureInfo.InvariantCulture);
            if (value.Type == JTokenType.Float && Number(value, out var number))
                return number.ToString("R", CultureInfo.InvariantCulture);
            return null;
        }

        private static bool IsNumber(JToken value) => value != null && (value.Type == JTokenType.Integer || value.Type == JTokenType.Float || value.Type == JTokenType.Raw);

        private static bool Number(JToken value, out double number)
        {
            number = 0;
            if (value == null)
                return false;
            string text;
            if (value.Type == JTokenType.Raw)
                text = ((JValue)value).Value as string;
            else if (value.Type == JTokenType.Integer || value.Type == JTokenType.Float)
                // Also handles Json.NET BigInteger without IConvertible exceptions.
                text = value.ToString(Newtonsoft.Json.Formatting.None);
            else
                return false;
            return text != null && double.TryParse(text, NumberStyles.Float, CultureInfo.InvariantCulture, out number) && !double.IsNaN(number) && !double.IsInfinity(number);
        }

        // JSON null means "field absent" except where null is itself a value.
        private static bool Nullable(JObject field)
        {
            var kind = (string)field["kind"];
            var type = (string)field["type"];
            return (kind == "message" && type == "google.protobuf.Value") || (kind == "enum" && type == "google.protobuf.NullValue");
        }

        private JToken Message(string type, JToken value, string path, int depth, bool counted, bool skipType)
        {
            if (!counted)
                Budget(path, depth);
            if (type == null)
                Fail(path, "message type");
            if (type.StartsWith("google.protobuf.", StringComparison.Ordinal) && WellKnown(type, value, path, depth, out var known))
                return known;
            var shape = schema["messages"]?[type] as JObject;
            var record = value as JObject;
            if (shape == null || record == null)
                Fail(path, "known message object");
            var declared = (JObject)shape["fields"];
            if (!aliases.TryGetValue(type, out var protoNames))
            {
                protoNames = new Dictionary<string, string>(StringComparer.Ordinal);
                foreach (var field in declared.Properties())
                    if ((field.Value as JObject)?["protoName"]?.Type == JTokenType.String && declared.Property((string)field.Value["protoName"]) == null)
                        protoNames[(string)field.Value["protoName"]] = field.Name;
                aliases[type] = protoNames;
            }

            // A member is spelled as the JSON name or the original Proto name; like Go protojson,
            // a field given twice (also once in each spelling) is rejected.
            var spelled = new Dictionary<string, JProperty>(StringComparer.Ordinal);
            var members = new List<KeyValuePair<JProperty, string>>();
            foreach (var entry in record.Properties().ToList())
            {
                if (skipType && entry.Name == "@type")
                    continue;
                var jsonName = declared.Property(entry.Name) != null ? entry.Name : protoNames.TryGetValue(entry.Name, out var alias) ? alias : null;
                if (jsonName == null)
                    Fail(path + "." + entry.Name, "declared field");
                if (spelled.ContainsKey(jsonName))
                    Fail(path + "." + entry.Name, "field given once");
                spelled[jsonName] = entry;
                members.Add(new KeyValuePair<JProperty, string>(entry, jsonName));
            }

            Func<string, bool> present = name =>
            {
                if (!spelled.TryGetValue(name, out var property))
                    return false;
                return property.Value.Type != JTokenType.Null || Nullable((JObject)declared[name]);
            };
            foreach (var group in shape["oneofs"] as JArray ?? new JArray())
                if (((JArray)group).Count(field => present((string)field)) > 1)
                    Fail(path, "one oneof member");
            foreach (var field in declared.Properties())
                if ((bool?)field.Value["required"] == true && !present(field.Name))
                    Fail(path, "required " + field.Name);
            foreach (var member in members)
            {
                var entry = member.Key;
                var field = declared[member.Value] as JObject;
                var next = path + "." + entry.Name;
                if (field == null)
                    Fail(next, "field schema");
                if (entry.Value.Type == JTokenType.Null && !Nullable(field))
                {
                    if (normalize)
                        entry.Remove();
                    continue;
                }

                if (field["mapKey"] != null)
                {
                    var entries = entry.Value as JObject;
                    if (entries == null)
                        Fail(next, "map");
                    Budget(next, depth + 1);
                    var keyKind = (string)field["mapKey"];
                    var seen = new HashSet<string>(StringComparer.Ordinal);
                    var canonical = new List<KeyValuePair<string, JToken>>();
                    var renamed = false;
                    foreach (var item in entries.Properties().ToList())
                    {
                        var key = ProtoMapKey(item.Name, keyKind);
                        if (key == null)
                            Fail(next, keyKind + " map key");
                        if (!seen.Add(key))
                            Fail(next, "unique map key");
                        renamed |= key != item.Name;
                        var result = Scalar(field, item.Value, next + "." + item.Name, depth + 2, false);
                        if (normalize && !ReferenceEquals(result, item.Value))
                            item.Value = result;
                        canonical.Add(new KeyValuePair<string, JToken>(key, item.Value));
                    }

                    if (normalize && renamed)
                    {
                        entries.RemoveAll();
                        foreach (var pair in canonical)
                            entries.Add(new JProperty(pair.Key, pair.Value));
                    }
                }
                else if ((bool?)field["list"] == true)
                {
                    var array = entry.Value as JArray;
                    if (array == null)
                        Fail(next, "array");
                    Budget(next, depth + 1);
                    for (var i = 0; i < array.Count; i++)
                    {
                        var result = Scalar(field, array[i], next + "[" + i + "]", depth + 2, false);
                        if (normalize && !ReferenceEquals(result, array[i]))
                            array[i] = result;
                    }
                }
                else
                {
                    var result = Scalar(field, entry.Value, next, depth + 1, false);
                    if (normalize && !ReferenceEquals(result, entry.Value))
                        entry.Value = result;
                }

                if (normalize && entry.Name != member.Value)
                    entry.Replace(new JProperty(member.Value, entry.Value));
            }

            return record;
        }

        private bool WellKnown(string type, JToken value, string path, int depth, out JToken result)
        {
            result = value;
            var name = type.Substring(16);
            if (Wrappers.TryGetValue(name, out var kind))
            {
                result = Scalar(new JObject { ["kind"] = kind }, value, path, depth, true);
                return true;
            }

            if (name == "Timestamp")
            {
                if (value?.Type != JTokenType.String || !ProtoTimestamp((string)value))
                    Fail(path, "RFC 3339 timestamp in range");
                return true;
            }

            if (name == "Duration")
            {
                if (value?.Type != JTokenType.String || !ProtoDuration((string)value))
                    Fail(path, "duration in range");
                return true;
            }

            if (name == "FieldMask")
            {
                if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?\z"))
                    Fail(path, "field mask");
                return true;
            }

            if (name == "Value")
            {
                result = JsonValue(value, path, depth, true);
                return true;
            }

            if (name == "Struct")
            {
                if (!(value is JObject))
                    Fail(path, "object");
                foreach (var entry in ((JObject)value).Properties().ToList())
                {
                    var item = JsonValue(entry.Value, path + "." + entry.Name, depth + 1, false);
                    if (normalize && !ReferenceEquals(item, entry.Value))
                        entry.Value = item;
                }

                return true;
            }

            if (name == "ListValue")
            {
                var array = value as JArray;
                if (array == null)
                    Fail(path, "array");
                for (var i = 0; i < array.Count; i++)
                {
                    var item = JsonValue(array[i], path, depth + 1, false);
                    if (normalize && !ReferenceEquals(item, array[i]))
                        array[i] = item;
                }

                return true;
            }

            if (name == "Any")
            {
                var fields = value as JObject;
                if (fields == null)
                    Fail(path, "object");
                // protojson writes an unset Any as {}.
                if (fields.Count == 0)
                    return true;
                if (fields["@type"]?.Type != JTokenType.String)
                    Fail(path, "Any @type");
                var nestedType = ((string)fields["@type"]).Split('/').Last();
                // An Any may hold another Any; the depth budget bounds the recursion.
                if (schema["messages"]?[nestedType] == null)
                    Fail(path, "known Any type");
                var members = fields.Properties().Where(property => property.Name != "@type").ToList();
                if (nestedType == "google.protobuf.Empty" && members.Count == 0)
                    return true; // Go tolerates an omitted Empty value on input.
                if (nestedType.StartsWith("google.protobuf.", StringComparison.Ordinal))
                {
                    if (members.Count != 1 || members[0].Name != "value")
                        Fail(path, "Any value");
                    var nested = Message(nestedType, members[0].Value, path + ".value", depth + 1, false, false);
                    if (normalize && !ReferenceEquals(nested, members[0].Value))
                        members[0].Value = nested;
                }
                else
                    Message(nestedType, fields, path, depth, true, true);
                return true;
            }

            return false;
        }

        private JToken Scalar(JObject field, JToken value, string path, int depth, bool counted)
        {
            var kind = (string)field["kind"];
            if (kind == "message" || kind == "group")
                return Message((string)field["type"], value, path, depth, counted, false);
            if (!counted)
                Budget(path, depth);
            if (kind == "enum")
            {
                if ((string)field["type"] == "google.protobuf.NullValue" && value?.Type == JTokenType.Null)
                    return value;
                if (value?.Type == JTokenType.String && (schema["enums"]?[(string)field["type"]] as JArray)?.Any(name => (string)name == (string)value) == true)
                    return value;
                var number = IsNumber(value) ? ProtoInteger(NumberText(value), true, 32) : null;
                if (number == null)
                    Fail(path, "enum name/int32");
                return Canonical(value, new JValue(long.Parse(number, CultureInfo.InvariantCulture)));
            }

            if (IntegerKind(kind, out var signed, out var bits))
            {
                var text = value?.Type == JTokenType.String ? (string)value : IsNumber(value) ? NumberText(value) : null;
                var integer = ProtoInteger(text, signed, bits);
                if (integer == null)
                    Fail(path, kind);
                return bits == 64 ? Canonical(value, new JValue(integer)) : Canonical(value, new JValue(long.Parse(integer, CultureInfo.InvariantCulture)));
            }

            if (kind == "double" || kind == "float")
            {
                if (value?.Type == JTokenType.String && ((string)value == "NaN" || (string)value == "Infinity" || (string)value == "-Infinity"))
                    return value;
                double floating = 0;
                var ok = value?.Type == JTokenType.String
                    ? JsonNumber.IsMatch((string)value) && double.TryParse((string)value, NumberStyles.Float, CultureInfo.InvariantCulture, out floating) && !double.IsInfinity(floating)
                    : Number(value, out floating);
                if (!ok)
                    Fail(path, kind);
                if (kind == "float")
                {
                    // Go keeps float32, rounded once from the exact decimal: 3.40282356e+38 becomes
                    // 3.4028235e+38 and 1e-50 becomes 0.
                    // Near 2^128 - 2^103 decide on the text: a double can round either way there.
                    var nearLimit = Math.Abs(Math.Abs(floating) - Float32Limit) <= Float32Limit * 1e-12;
                    var text = value.Type == JTokenType.String ? (string)value : NumberText(value);
                    if (nearLimit ? !BelowFloat32Limit(text) : Math.Abs(floating) > Float32Limit)
                        Fail(path, kind);
                    var rounded = nearLimit ? Math.Sign(floating) * (double)float.MaxValue : (double)(float)floating;
                    return value.Type == JTokenType.Integer && rounded == floating ? value : Canonical(value, new JValue(rounded));
                }

                return value.Type == JTokenType.Integer || value.Type == JTokenType.Float ? value : new JValue(floating);
            }

            if (kind == "bool")
            {
                if (value?.Type != JTokenType.Boolean)
                    Fail(path, "boolean");
                return value;
            }

            if (kind == "string")
            {
                if (value?.Type != JTokenType.String)
                    Fail(path, "string");
                return value;
            }

            if (kind == "bytes")
            {
                var bytes = value?.Type == JTokenType.String ? ProtoBytes((string)value) : null;
                if (bytes == null)
                    Fail(path, "Base64");
                return Canonical(value, new JValue(bytes));
            }

            Fail(path, "supported scalar");
            return value;
        }

        // Keep the original token when it already holds the canonical value.
        private static JToken Canonical(JToken original, JValue canonical)
        {
            return original is JValue value && original.Type == canonical.Type && Equals(value.Value, canonical.Value) ? original : canonical;
        }

        private JToken JsonValue(JToken value, string path, int depth, bool counted)
        {
            if (!counted)
                Budget(path, depth);
            if (value == null)
                Fail(path, "JSON value");
            if (value is JObject record)
            {
                foreach (var property in record.Properties().ToList())
                {
                    var item = JsonValue(property.Value, path, depth + 1, false);
                    if (normalize && !ReferenceEquals(item, property.Value))
                        property.Value = item;
                }

                return value;
            }

            if (value is JArray array)
            {
                for (var i = 0; i < array.Count; i++)
                {
                    var item = JsonValue(array[i], path, depth + 1, false);
                    if (normalize && !ReferenceEquals(item, array[i]))
                        array[i] = item;
                }

                return value;
            }

            if (value.Type == JTokenType.Null || value.Type == JTokenType.String || value.Type == JTokenType.Boolean)
                return value;
            if (Number(value, out var number))
                return value.Type == JTokenType.Raw ? new JValue(number) : value;
            Fail(path, "finite JSON value");
            return value;
        }
    }
}
