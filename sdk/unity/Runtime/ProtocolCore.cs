using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.RegularExpressions;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    public sealed class ProtocolException : Exception
    {
        public string Code { get; }
        public bool Retryable { get; }
        public ProtocolException(string code, string message, bool retryable = false) : base(message) { Code = code; Retryable = retryable; }
    }

    public sealed class ProtocolContract
    {
        public string Version { get; }
        public string SchemaHash { get; }
        public JObject Operations { get; }
        public JObject WireSchema { get; }
        public ProtocolContract(string runtimeJson)
        {
            var body = Json.Parse(runtimeJson) as JObject;
            if (body == null || body["version"]?.Type != JTokenType.String || body["schemaHash"]?.Type != JTokenType.String || !(body["operations"] is JObject) || !(body["wireSchema"] is JObject)) throw new ProtocolException("bad_request", "Load generated runtime.json");
            Version = (string)body["version"]; SchemaHash = (string)body["schemaHash"];
            Operations = (JObject)body["operations"]; WireSchema = (JObject)body["wireSchema"];
        }
        public JObject Operation(string id, string transport)
        {
            var operation = Operations[id] as JObject;
            if (operation == null || (string)operation["transport"] != transport) throw new ProtocolException("invalid_operation", "Unknown operation or wrong transport");
            return operation;
        }
        public void Validate(string type, JToken value) { new WireValidator(WireSchema).Validate(type, value); }
    }

    public static class Json
    {
        public static JToken Parse(string text)
        {
            try
            {
                if (!new JsonSyntax(text).Valid()) throw new ProtocolException("invalid_json", "Invalid JSON syntax");
                using (var reader = new JsonTextReader(new StringReader(text)) { DateParseHandling = DateParseHandling.None, MaxDepth = 64 })
                    return JToken.Load(reader, new JsonLoadSettings { DuplicatePropertyNameHandling = DuplicatePropertyNameHandling.Error });
            }
            catch (ProtocolException) { throw; }
            catch (Exception exception) when (exception is JsonException || exception is ArgumentException) { throw new ProtocolException("invalid_json", "Invalid JSON response"); }
        }
        public static string RequiredString(JObject body, string name)
        {
            if (body[name]?.Type != JTokenType.String) throw new ProtocolException("invalid_frame", "Expected string " + name);
            return (string)body[name];
        }
    }

    // Json.NET accepts JavaScript extensions; reject those before loading tokens.
    internal sealed class JsonSyntax
    {
        private readonly string text;
        private int pos, nodes;
        public JsonSyntax(string text) { this.text = text ?? ""; }
        private void Space() { while (pos < text.Length && (text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\r' || text[pos] == '\n')) pos++; }
        private bool Take(char ch) { Space(); if (pos == text.Length || text[pos] != ch) return false; pos++; return true; }
        private bool Hex(out int code)
        {
            code = 0;
            for (var i = 0; i < 4; i++) { if (pos == text.Length) return false; var ch = text[pos++]; var digit = ch >= '0' && ch <= '9' ? ch - '0' : ch >= 'a' && ch <= 'f' ? ch - 'a' + 10 : ch >= 'A' && ch <= 'F' ? ch - 'A' + 10 : -1; if (digit < 0) return false; code = (code << 4) | digit; }
            return true;
        }
        private bool String()
        {
            if (!Take('"')) return false;
            while (pos < text.Length)
            {
                var ch = text[pos++];
                if (ch == '"') return true;
                if (ch < 32) return false;
                if (ch == '\\')
                {
                    if (pos == text.Length) return false;
                    var escape = text[pos++];
                    if (escape == 'u') { if (!Hex(out var code)) return false; if (code >= 0xd800 && code <= 0xdbff) { if (pos + 2 > text.Length || text[pos++] != '\\' || text[pos++] != 'u' || !Hex(out var low) || low < 0xdc00 || low > 0xdfff) return false; } else if (code >= 0xdc00 && code <= 0xdfff) return false; }
                    else if ("\"\\/bfnrt".IndexOf(escape) < 0) return false;
                }
                else if (char.IsHighSurrogate(ch)) { if (pos == text.Length || !char.IsLowSurrogate(text[pos++])) return false; }
                else if (char.IsLowSurrogate(ch)) return false;
            }
            return false;
        }
        private bool Digits() { var start = pos; while (pos < text.Length && text[pos] >= '0' && text[pos] <= '9') pos++; return pos != start; }
        private bool Value(int depth)
        {
            Space(); if (depth > 64 || ++nodes > 100000 || pos == text.Length) return false;
            if (text[pos] == '"') return String();
            if (Take('{')) { if (Take('}')) return true; do { if (!String() || !Take(':') || !Value(depth + 1)) return false; if (Take('}')) return true; } while (Take(',')); return false; }
            if (Take('[')) { if (Take(']')) return true; do { if (!Value(depth + 1)) return false; if (Take(']')) return true; } while (Take(',')); return false; }
            foreach (var word in new[] { "true", "false", "null" }) if (pos + word.Length <= text.Length && string.CompareOrdinal(text, pos, word, 0, word.Length) == 0) { pos += word.Length; return true; }
            if (text[pos] == '-') pos++;
            if (pos == text.Length) return false;
            if (text[pos] == '0') pos++; else if (text[pos] < '1' || text[pos] > '9' || !Digits()) return false;
            if (pos < text.Length && text[pos] == '.') { pos++; if (!Digits()) return false; }
            if (pos < text.Length && (text[pos] == 'e' || text[pos] == 'E')) { pos++; if (pos < text.Length && (text[pos] == '+' || text[pos] == '-')) pos++; if (!Digits()) return false; }
            return true;
        }
        public bool Valid() { if (!Value(0)) return false; Space(); return pos == text.Length; }
    }

    public sealed class SseFrame
    {
        public string Event, Id, Data;
    }
    public sealed class SseParser
    {
        private readonly Decoder decoder = new UTF8Encoding(false, true).GetDecoder();
        private readonly StringBuilder line = new StringBuilder(), data = new StringBuilder();
        private string name = "", id = "";
        private bool first = true, skipLf, hasData;
        private int size;
        private readonly int maximum;
        public SseParser(int maximumChars = 1048576) { if (maximumChars < 1) throw new ArgumentOutOfRangeException(nameof(maximumChars)); maximum = maximumChars; }
        public void Feed(byte[] bytes, int count, Action<SseFrame> receive)
        {
            var chars = new char[count + 2];
            int length;
            try { length = decoder.GetChars(bytes, 0, count, chars, 0, false); }
            catch (DecoderFallbackException) { throw new ProtocolException("invalid_utf8", "Invalid UTF-8 stream"); }
            for (var index = 0; index < length; index++)
            {
                var ch = chars[index];
                if (first) { first = false; if (ch == '\ufeff') continue; }
                if (skipLf) { skipLf = false; if (ch == '\n') continue; }
                if (++size > maximum) throw new ProtocolException("frame_too_large", "SSE frame exceeds configured limit");
                if (ch == '\r' || ch == '\n') { Consume(receive); skipLf = ch == '\r'; } else line.Append(ch);
            }
        }
        private void Consume(Action<SseFrame> receive)
        {
            var text = line.ToString(); line.Clear();
            if (text.Length == 0)
            {
                var frame = hasData ? new SseFrame { Event = name.Length == 0 ? "message" : name, Id = id, Data = data.ToString(0, data.Length - 1) } : null;
                hasData = false; data.Clear(); name = ""; size = 0;
                if (frame != null) receive(frame);
                return;
            }
            if (text[0] == ':') return;
            var colon = text.IndexOf(':');
            var field = colon < 0 ? text : text.Substring(0, colon);
            var value = colon < 0 ? "" : text.Substring(colon + 1);
            if (value.StartsWith(" ", StringComparison.Ordinal)) value = value.Substring(1);
            if (field == "data") { data.Append(value).Append('\n'); hasData = true; }
            else if (field == "event") name = value;
            else if (field == "id" && value.IndexOf('\0') < 0) id = value;
        }
        public void Finish()
        {
            try { decoder.GetChars(Array.Empty<byte>(), 0, 0, new char[2], 0, true); }
            catch (DecoderFallbackException) { throw new ProtocolException("invalid_utf8", "Truncated UTF-8 stream"); }
        }
    }

    public sealed class StreamEvent
    {
        public string Name, RequestId, Sequence;
        public JObject Payload;
    }
    public sealed class ProtocolSession
    {
        public bool Terminal { get; private set; }
        private readonly ProtocolContract contract;
        private readonly JObject operation;
        private readonly string requestId;
        private string sequence = "0";
        public ProtocolSession(ProtocolContract contract, JObject operation, string requestId)
        {
            this.contract = contract; this.operation = operation; this.requestId = requestId;
            if (!Regex.IsMatch(requestId ?? "", "\\A[A-Za-z0-9_.-]{1,128}\\z")) throw new ProtocolException("bad_request", "Invalid request ID");
        }
        private JObject Envelope(string json)
        {
            var body = Json.Parse(json) as JObject;
            if (body == null) throw new ProtocolException("invalid_frame", "Expected response object");
            if (Json.RequiredString(body, "protocolVersion") != contract.Version) throw new ProtocolException("version_mismatch", "Response protocol version differs");
            if (Json.RequiredString(body, "schemaHash") != contract.SchemaHash) throw new ProtocolException("schema_mismatch", "Response schema differs");
            if (Json.RequiredString(body, "requestId") != requestId) throw new ProtocolException("invalid_frame", "Response request ID differs");
            return body;
        }
        private static void RemoteError(JToken value)
        {
            var error = value as JObject;
            if (error == null || error["code"]?.Type != JTokenType.String || error["message"]?.Type != JTokenType.String || error["retryable"]?.Type != JTokenType.Boolean) throw new ProtocolException("invalid_frame", "Invalid transport error");
            throw new ProtocolException((string)error["code"], (string)error["message"], (bool)error["retryable"]);
        }
        public JToken Unary(string json, long status)
        {
            var body = Envelope(json);
            if (body.Property("error") != null) { if (body.Property("data") != null) throw new ProtocolException("invalid_frame", "Response contains data and error"); RemoteError(body["error"]); }
            if (status < 200 || status >= 300) throw new ProtocolException("http_error", "Unexpected HTTP status");
            if (body.Property("data") == null) throw new ProtocolException("invalid_frame", "Response has no data");
            contract.Validate((string)operation["responseType"], body["data"]);
            return body["data"];
        }
        public StreamEvent Accept(SseFrame frame)
        {
            if (Terminal) throw new ProtocolException("invalid_frame", "Event after terminal");
            var body = Envelope(frame.Data);
            var expected = NextSequence(sequence);
            if (body["sequence"]?.Type != JTokenType.String || (string)body["sequence"] != expected || frame.Id != expected) throw new ProtocolException("invalid_sequence", "Sequence must increment from 1");
            sequence = expected;
            if (frame.Event == "protocol.error") { if (body.Property("payload") != null) throw new ProtocolException("invalid_frame", "Error contains payload"); Terminal = true; RemoteError(body["error"]); }
            var descriptor = operation["events"]?[frame.Event] as JObject;
            if (descriptor == null || body.Property("error") != null) throw new ProtocolException("invalid_event", "Unknown or conflicting event");
            var payload = body["payload"] as JObject;
            if (payload == null || payload.Count != 1 || payload.Property((string)descriptor["field"]) == null) throw new ProtocolException("invalid_event", "Event differs from response oneof");
            contract.Validate((string)operation["responseType"], payload);
            Terminal = (bool)descriptor["terminal"];
            return new StreamEvent { Name = frame.Event, RequestId = requestId, Sequence = sequence, Payload = payload };
        }
        public void Finish() { if (!Terminal) throw new ProtocolException("incomplete_stream", "EOF before terminal event"); }
        public static string NextSequence(string current)
        {
            var digits = current.ToCharArray();
            for (var index = digits.Length - 1; index >= 0; index--) { if (digits[index] != '9') { digits[index]++; return new string(digits); } digits[index] = '0'; }
            return "1" + new string(digits);
        }
    }

    public sealed class WireValidator
    {
        private readonly JObject schema;
        private int nodes;
        public WireValidator(JObject schema) { this.schema = schema; }
        private static void Fail(string path, string expected) { throw new ProtocolException("invalid_message", path + ": expected " + expected); }
        private void Budget(string path, int depth) { if (depth > 64 || ++nodes > 100000) Fail(path, "bounded message depth/size"); }
        public void Validate(string type, JToken value) { nodes = 0; Message(type, value, "$", 0); }
        public static bool IntegerString(string value, bool signed, int bits = 64)
        {
            if (value == null || value == "-0") return false;
            var match = Regex.Match(value, "^-?(0|[1-9][0-9]*)$");
            if (!match.Success || match.Length != value.Length) return false;
            var negative = value.StartsWith("-", StringComparison.Ordinal);
            if (negative && !signed) return false;
            var digits = negative ? value.Substring(1) : value;
            var max = bits == 32 ? (signed ? (negative ? "2147483648" : "2147483647") : "4294967295") : (signed ? (negative ? "9223372036854775808" : "9223372036854775807") : "18446744073709551615");
            return digits.Length < max.Length || (digits.Length == max.Length && string.CompareOrdinal(digits, max) <= 0);
        }
        private void Message(string type, JToken value, string path, int depth)
        {
            Budget(path, depth);
            if (type == null) Fail(path, "message type");
            if (type.StartsWith("google.protobuf.", StringComparison.Ordinal))
            {
                var name = type.Substring(16);
                var wrappers = new Dictionary<string, string> { { "DoubleValue", "double" }, { "FloatValue", "float" }, { "Int64Value", "int64" }, { "UInt64Value", "uint64" }, { "Int32Value", "int32" }, { "UInt32Value", "uint32" }, { "BoolValue", "bool" }, { "StringValue", "string" }, { "BytesValue", "bytes" } };
                if (wrappers.TryGetValue(name, out var kind)) { Scalar(new JObject { ["kind"] = kind }, value, path, depth + 1); return; }
                if (name == "Timestamp")
                {
                    if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?Z\z") || !DateTime.TryParseExact(((string)value).Substring(0, 19), "yyyy-MM-dd'T'HH:mm:ss", CultureInfo.InvariantCulture, DateTimeStyles.None, out _)) Fail(path, "canonical UTC timestamp");
                    return;
                }
                if (name == "Duration")
                {
                    if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A-?(0|[1-9][0-9]*)(?:\.(?:[0-9]{3}|[0-9]{6}|[0-9]{9}))?s\z") || !decimal.TryParse(((string)value).TrimEnd('s'), NumberStyles.AllowLeadingSign | NumberStyles.AllowDecimalPoint, CultureInfo.InvariantCulture, out var duration) || Math.Abs(decimal.Truncate(duration)) > 315576000000m) Fail(path, "duration in range");
                    return;
                }
                if (name == "FieldMask") { if (value?.Type != JTokenType.String || !Regex.IsMatch((string)value, @"\A(?:[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)*)*)?\z")) Fail(path, "field mask"); return; }
                if (name == "Value") { JsonValue(value, path, depth + 1); return; }
                if (name == "Struct") { if (!(value is JObject)) Fail(path, "object"); foreach (var entry in ((JObject)value).Properties()) JsonValue(entry.Value, path + "." + entry.Name, depth + 1); return; }
                if (name == "ListValue") { if (!(value is JArray)) Fail(path, "array"); foreach (var entry in (JArray)value) JsonValue(entry, path, depth + 1); return; }
                if (name == "Any")
                {
                    var fields = value as JObject;
                    if (fields == null || fields["@type"]?.Type != JTokenType.String) Fail(path, "Any @type");
                    var nestedType = ((string)fields["@type"]).Split('/').Last();
                    if (nestedType == type || schema["messages"]?[nestedType] == null) Fail(path, "known Any type");
                    var nested = (JObject)fields.DeepClone(); nested.Remove("@type");
                    if (nestedType.StartsWith("google.protobuf.", StringComparison.Ordinal) && nestedType != "google.protobuf.Empty") { if (nested.Count != 1 || nested.Property("value") == null) Fail(path, "Any value"); Message(nestedType, nested["value"], path, depth + 1); }
                    else Message(nestedType, nested, path, depth + 1);
                    return;
                }
            }
            var shape = schema["messages"]?[type] as JObject;
            var record = value as JObject;
            if (shape == null || record == null) Fail(path, "known message object");
            foreach (var group in shape["oneofs"] as JArray ?? new JArray()) if (((JArray)group).Count(field => record.Property((string)field) != null) > 1) Fail(path, "one oneof member");
            var declared = (JObject)shape["fields"];
            foreach (var field in declared.Properties()) if ((bool?)field.Value["required"] == true && record.Property(field.Name) == null) Fail(path, "required " + field.Name);
            foreach (var entry in record.Properties())
            {
                var field = declared[entry.Name] as JObject;
                var next = path + "." + entry.Name;
                if (field == null) Fail(next, "declared field");
                if (field["mapKey"] != null)
                {
                    var entries = entry.Value as JObject; if (entries == null) Fail(next, "map");
                    var keyKind = (string)field["mapKey"];
                    foreach (var item in entries.Properties())
                    {
                        if (keyKind == "bool") { if (item.Name != "true" && item.Name != "false") Fail(next, "boolean key"); }
                        else if (keyKind != "string" && !IntegerString(item.Name, keyKind.StartsWith("s", StringComparison.Ordinal) || keyKind.StartsWith("int", StringComparison.Ordinal), keyKind.EndsWith("32", StringComparison.Ordinal) ? 32 : 64)) Fail(next, "integer key");
                        Scalar(field, item.Value, next, depth + 1);
                    }
                }
                else if ((bool?)field["list"] == true) { if (!(entry.Value is JArray)) Fail(next, "array"); foreach (var item in (JArray)entry.Value) Scalar(field, item, next, depth + 1); }
                else Scalar(field, entry.Value, next, depth + 1);
            }
        }
        private void Scalar(JObject field, JToken value, string path, int depth)
        {
            Budget(path, depth);
            var kind = (string)field["kind"];
            if (kind == "message" || kind == "group") { Message((string)field["type"], value, path, depth + 1); return; }
            if (kind == "enum")
            {
                if ((string)field["type"] == "google.protobuf.NullValue" && value?.Type == JTokenType.Null) return;
                if (Number(value, out var enumNumber) && Math.Truncate(enumNumber) == enumNumber && enumNumber >= int.MinValue && enumNumber <= int.MaxValue) return;
                if (value?.Type == JTokenType.String && (schema["enums"]?[(string)field["type"]] as JArray)?.Any(name => (string)name == (string)value) == true) return;
                Fail(path, "enum name/int32");
            }
            if (kind == "int64" || kind == "sint64" || kind == "sfixed64" || kind == "uint64" || kind == "fixed64") { if (value?.Type != JTokenType.String || !IntegerString((string)value, kind != "uint64" && kind != "fixed64")) Fail(path, "decimal " + kind); return; }
            if (kind == "int32" || kind == "sint32" || kind == "sfixed32" || kind == "uint32" || kind == "fixed32") { var signed = kind != "uint32" && kind != "fixed32"; if (!Number(value, out var integer) || Math.Truncate(integer) != integer || integer < (signed ? int.MinValue : 0d) || integer > (signed ? int.MaxValue : uint.MaxValue)) Fail(path, kind); return; }
            if (kind == "double" || kind == "float")
            {
                if (value?.Type == JTokenType.String && new[] { "NaN", "Infinity", "-Infinity" }.Contains((string)value)) return;
                if (!Number(value, out var floating) || (kind == "float" && Math.Abs(floating) > 3.4028234663852886e38)) Fail(path, kind);
                return;
            }
            if (kind == "bool") { if (value?.Type != JTokenType.Boolean) Fail(path, "boolean"); return; }
            if (kind == "string" || kind == "bytes") { if (value?.Type != JTokenType.String) Fail(path, "string"); if (kind == "bytes" && !Regex.IsMatch((string)value, @"\A(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?\z")) Fail(path, "Base64"); return; }
            Fail(path, "supported scalar");
        }
        private void JsonValue(JToken value, string path, int depth)
        {
            Budget(path, depth);
            if (value == null) Fail(path, "JSON value");
            if (value is JObject record) { foreach (var property in record.Properties()) JsonValue(property.Value, path, depth + 1); return; }
            if (value is JArray array) { foreach (var item in array) JsonValue(item, path, depth + 1); return; }
            if (value.Type == JTokenType.Null || value.Type == JTokenType.String || value.Type == JTokenType.Boolean) return;
            if (Number(value, out _)) return;
            Fail(path, "finite JSON value");
        }
        private static bool Number(JToken value, out double number)
        {
            number = 0;
            // Also handles Json.NET BigInteger without IConvertible exceptions.
            return value != null && (value.Type == JTokenType.Integer || value.Type == JTokenType.Float)
                && double.TryParse(value.ToString(Newtonsoft.Json.Formatting.None), NumberStyles.Float, CultureInfo.InvariantCulture, out number)
                && !double.IsNaN(number) && !double.IsInfinity(number);
        }
    }
}
