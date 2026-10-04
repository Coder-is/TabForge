using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text.RegularExpressions;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    public sealed class WireValidator
    {
        private static readonly Dictionary<string, string> Wrappers = new Dictionary<string, string>
        {
            {
                "DoubleValue",
                "double"
            },
            {
                "FloatValue",
                "float"
            },
            {
                "Int64Value",
                "int64"
            },
            {
                "UInt64Value",
                "uint64"
            },
            {
                "Int32Value",
                "int32"
            },
            {
                "UInt32Value",
                "uint32"
            },
            {
                "BoolValue",
                "bool"
            },
            {
                "StringValue",
                "string"
            },
            {
                "BytesValue",
                "bytes"
            }
        };
        private readonly JObject schema;
        private int nodes;
        public WireValidator(JObject schema)
        {
            this.schema = schema;
        }

        private static void Fail(string path, string expected)
        {
            throw new ProtocolException("invalid_message", path + ": expected " + expected);
        }

        private void Budget(string path, int depth)
        {
            if (depth > 64 || ++nodes > 100000)
                Fail(path, "bounded message depth/size");
        }

        public void Validate(string type, JToken value)
        {
            nodes = 0;
            Message(type, value, "$", 0);
        }

        public static bool IntegerString(string value, bool signed, int bits = 64)
        {
            if (value == null || value == "-0")
                return false;
            var match = Regex.Match(value, "^-?(0|[1-9][0-9]*)$");
            if (!match.Success || match.Length != value.Length)
                return false;
            var negative = value.StartsWith("-", StringComparison.Ordinal);
            if (negative && !signed)
                return false;
            var digits = negative ? value.Substring(1) : value;
            var max = bits == 32 ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295") : (signed ? (negative ? "9223372036854775808" : "9223372036854775807") : "18446744073709551615");
            return digits.Length < max.Length || (digits.Length == max.Length && string.CompareOrdinal(digits, max) <= 0);
        }

        private void Message(string type, JToken value, string path, int depth)
        {
            Budget(path, depth);
            if (type == null)
                Fail(path, "message type");
            if (type.StartsWith("google.protobuf.", StringComparison.Ordinal) && WellKnown(type, value, path, depth))
                return;
            var shape = schema["messages"]?[type] as JObject;
            var record = value as JObject;
            if (shape == null || record == null)
                Fail(path, "known message object");
            foreach (var group in shape["oneofs"] as JArray ?? new JArray())
                if (((JArray)group).Count(field => record.Property((string)field) != null) > 1)
                    Fail(path, "one oneof member");
            var declared = (JObject)shape["fields"];
            foreach (var field in declared.Properties())
                if ((bool? )field.Value["required"] == true && record.Property(field.Name) == null)
                    Fail(path, "required " + field.Name);
            foreach (var entry in record.Properties())
            {
                var field = declared[entry.Name] as JObject;
                var next = path + "." + entry.Name;
                if (field == null)
                    Fail(next, "declared field");
                if (field["mapKey"] != null)
                {
                    var entries = entry.Value as JObject;
                    if (entries == null)
                        Fail(next, "map");
                    var keyKind = (string)field["mapKey"];
                    foreach (var item in entries.Properties())
                    {
                        if (keyKind == "bool")
                        {
                            if (item.Name != "true" && item.Name != "false")
                                Fail(next, "boolean key");
                        }
                        else if (keyKind != "string" && !IntegerString(item.Name, keyKind.StartsWith("s", StringComparison.Ordinal) || keyKind.StartsWith("int", StringComparison.Ordinal), keyKind.EndsWith("32", StringComparison.Ordinal) ? 32 : 64))
                            Fail(next, "integer key");
                        Scalar(field, item.Value, next, depth + 1);
                    }
                }
                else if ((bool? )field["list"] == true)
                {
                    if (!(entry.Value is JArray))
                        Fail(next, "array");
                    foreach (var item in (JArray)entry.Value)
                        Scalar(field, item, next, depth + 1);
                }
                else
                    Scalar(field, entry.Value, next, depth + 1);
            }
        }

        private bool WellKnown(string type, JToken value, string path, int depth)
        {
            var name = type.Substring(16);
            if (Wrappers.TryGetValue(name, out var kind))
            {
                Scalar(new JObject { ["kind"] = kind }, value, path, depth + 1);
                return true;
            }

            if (name == "Timestamp")
            {
                if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z\z") || !DateTime.TryParseExact(((string)value).Substring(0, 19), "yyyy-MM-dd'T'HH:mm:ss", CultureInfo.InvariantCulture, DateTimeStyles.None, out _))
                    Fail(path, "canonical UTC timestamp");
                return true;
            }

            if (name == "Duration")
            {
                if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A-?(0|[1-9][0-9]*)(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s\z") || !decimal.TryParse(((string)value).TrimEnd('s'), NumberStyles.AllowLeadingSign | NumberStyles.AllowDecimalPoint, CultureInfo.InvariantCulture, out var duration) || Math.Abs(decimal.Truncate(duration)) > 315576000000m)
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
                JsonValue(value, path, depth + 1);
                return true;
            }

            if (name == "Struct")
            {
                if (!(value is JObject))
                    Fail(path, "object");
                foreach (var entry in ((JObject)value).Properties())
                    JsonValue(entry.Value, path + "." + entry.Name, depth + 1);
                return true;
            }

            if (name == "ListValue")
            {
                if (!(value is JArray))
                    Fail(path, "array");
                foreach (var entry in (JArray)value)
                    JsonValue(entry, path, depth + 1);
                return true;
            }

            if (name == "Any")
            {
                var fields = value as JObject;
                if (fields == null || fields["@type"]?.Type != JTokenType.String)
                    Fail(path, "Any @type");
                var nestedType = ((string)fields["@type"]).Split('/').Last();
                if (nestedType == type || schema["messages"]?[nestedType] == null)
                    Fail(path, "known Any type");
                var nested = (JObject)fields.DeepClone();
                nested.Remove("@type");
                if (nestedType.StartsWith("google.protobuf.", StringComparison.Ordinal) && nestedType != "google.protobuf.Empty")
                {
                    if (nested.Count != 1 || nested.Property("value") == null)
                        Fail(path, "Any value");
                    Message(nestedType, nested["value"], path, depth + 1);
                }
                else
                    Message(nestedType, nested, path, depth + 1);
                return true;
            }

            return false;
        }

        private void Scalar(JObject field, JToken value, string path, int depth)
        {
            Budget(path, depth);
            var kind = (string)field["kind"];
            if (kind == "message" || kind == "group")
            {
                Message((string)field["type"], value, path, depth + 1);
                return;
            }

            if (kind == "enum")
            {
                if ((string)field["type"] == "google.protobuf.NullValue" && value?.Type == JTokenType.Null)
                    return;
                if (Number(value, out var enumNumber) && Math.Truncate(enumNumber) == enumNumber && enumNumber >= int.MinValue && enumNumber <= int.MaxValue)
                    return;
                if (value?.Type == JTokenType.String && (schema["enums"]?[(string)field["type"]] as JArray)?.Any(name => (string)name == (string)value) == true)
                    return;
                Fail(path, "enum name/int32");
            }

            if (kind == "int64" || kind == "sint64" || kind == "sfixed64" || kind == "uint64" || kind == "fixed64")
            {
                if (value?.Type != JTokenType.String || !IntegerString((string)value, kind != "uint64" && kind != "fixed64"))
                    Fail(path, "decimal " + kind);
                return;
            }

            if (kind == "int32" || kind == "sint32" || kind == "sfixed32" || kind == "uint32" || kind == "fixed32")
            {
                var signed = kind != "uint32" && kind != "fixed32";
                if (!Number(value, out var integer) || Math.Truncate(integer) != integer || integer < (signed ? int.MinValue : 0d) || integer > (signed ? int.MaxValue : uint.MaxValue))
                    Fail(path, kind);
                return;
            }

            if (kind == "double" || kind == "float")
            {
                if (value?.Type == JTokenType.String && new[]
                {
                    "NaN",
                    "Infinity",
                    "-Infinity"
                }.Contains((string)value))
                    return;
                if (!Number(value, out var floating) || (kind == "float" && Math.Abs(floating) > 3.4028234663852886e38))
                    Fail(path, kind);
                return;
            }

            if (kind == "bool")
            {
                if (value?.Type != JTokenType.Boolean)
                    Fail(path, "boolean");
                return;
            }

            if (kind == "string" || kind == "bytes")
            {
                if (value?.Type != JTokenType.String)
                    Fail(path, "string");
                if (kind == "bytes" && !Regex.IsMatch((string)value, @"\A(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?\z"))
                    Fail(path, "Base64");
                return;
            }

            Fail(path, "supported scalar");
        }

        private void JsonValue(JToken value, string path, int depth)
        {
            Budget(path, depth);
            if (value == null)
                Fail(path, "JSON value");
            if (value is JObject record)
            {
                foreach (var property in record.Properties())
                    JsonValue(property.Value, path, depth + 1);
                return;
            }

            if (value is JArray array)
            {
                foreach (var item in array)
                    JsonValue(item, path, depth + 1);
                return;
            }

            if (value.Type == JTokenType.Null || value.Type == JTokenType.String || value.Type == JTokenType.Boolean)
                return;
            if (Number(value, out _))
                return;
            Fail(path, "finite JSON value");
        }

        private static bool Number(JToken value, out double number)
        {
            number = 0;
            // Also handles Json.NET BigInteger without IConvertible exceptions.
            return value != null && (value.Type == JTokenType.Integer || value.Type == JTokenType.Float) && double.TryParse(value.ToString(Newtonsoft.Json.Formatting.None), NumberStyles.Float, CultureInfo.InvariantCulture, out number) && !double.IsNaN(number) && !double.IsInfinity(number);
        }
    }
}
