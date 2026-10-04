using System;
using System.IO;
using System.Text.RegularExpressions;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    public sealed class ProtocolException : Exception
    {
        public string Code { get; }
        public bool Retryable { get; }

        public ProtocolException(string code, string message, bool retryable = false) : base(message)
        {
            Code = code;
            Retryable = retryable;
        }
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
            if (body == null || body["version"]?.Type != JTokenType.String || body["schemaHash"]?.Type != JTokenType.String || !(body["operations"] is JObject) || !(body["wireSchema"] is JObject))
                throw new ProtocolException("bad_request", "Load generated runtime.json");
            Version = (string)body["version"];
            SchemaHash = (string)body["schemaHash"];
            Operations = (JObject)body["operations"];
            WireSchema = (JObject)body["wireSchema"];
        }

        public JObject Operation(string id, string transport)
        {
            var operation = Operations[id] as JObject;
            if (operation == null || (string)operation["transport"] != transport)
                throw new ProtocolException("invalid_operation", "Unknown operation or wrong transport");
            return operation;
        }

        public void Validate(string type, JToken value)
        {
            new WireValidator(WireSchema).Validate(type, value);
        }
    }

    public static class Json
    {
        public static JToken Parse(string text)
        {
            try
            {
                if (!new JsonSyntax(text).Valid())
                    throw new ProtocolException("invalid_json", "Invalid JSON syntax");
                using (var reader = new JsonTextReader(new StringReader(text))
                {
                    DateParseHandling = DateParseHandling.None,
                    MaxDepth = 64
                }

                )
                    return JToken.Load(reader, new JsonLoadSettings { DuplicatePropertyNameHandling = DuplicatePropertyNameHandling.Error });
            }
            catch (ProtocolException)
            {
                throw;
            }
            catch (Exception exception)when (exception is JsonException || exception is ArgumentException)
            {
                throw new ProtocolException("invalid_json", "Invalid JSON response");
            }
        }

        public static string RequiredString(JObject body, string name)
        {
            if (body[name]?.Type != JTokenType.String)
                throw new ProtocolException("invalid_frame", "Expected string " + name);
            return (string)body[name];
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
            this.contract = contract;
            this.operation = operation;
            this.requestId = requestId;
            if (!Regex.IsMatch(requestId ?? "", "\\A[A-Za-z0-9_.-]{1,128}\\z"))
                throw new ProtocolException("bad_request", "Invalid request ID");
        }

        private JObject Envelope(string json)
        {
            var body = Json.Parse(json) as JObject;
            if (body == null)
                throw new ProtocolException("invalid_frame", "Expected response object");
            if (Json.RequiredString(body, "protocolVersion") != contract.Version)
                throw new ProtocolException("version_mismatch", "Response protocol version differs");
            if (Json.RequiredString(body, "schemaHash") != contract.SchemaHash)
                throw new ProtocolException("schema_mismatch", "Response schema differs");
            if (Json.RequiredString(body, "requestId") != requestId)
                throw new ProtocolException("invalid_frame", "Response request ID differs");
            return body;
        }

        private static void RemoteError(JToken value)
        {
            var error = value as JObject;
            if (error == null || error["code"]?.Type != JTokenType.String || error["message"]?.Type != JTokenType.String || error["retryable"]?.Type != JTokenType.Boolean)
                throw new ProtocolException("invalid_frame", "Invalid transport error");
            throw new ProtocolException((string)error["code"], (string)error["message"], (bool)error["retryable"]);
        }

        public JToken Unary(string json, long status)
        {
            var body = Envelope(json);
            if (body.Property("error") != null)
            {
                if (body.Property("data") != null)
                    throw new ProtocolException("invalid_frame", "Response contains data and error");
                RemoteError(body["error"]);
            }

            if (status < 200 || status >= 300)
                throw new ProtocolException("http_error", "Unexpected HTTP status");
            if (body.Property("data") == null)
                throw new ProtocolException("invalid_frame", "Response has no data");
            contract.Validate((string)operation["responseType"], body["data"]);
            return body["data"];
        }

        public StreamEvent Accept(SseFrame frame)
        {
            if (Terminal)
                throw new ProtocolException("invalid_frame", "Event after terminal");
            var body = Envelope(frame.Data);
            var expected = NextSequence(sequence);
            if (body["sequence"]?.Type != JTokenType.String || (string)body["sequence"] != expected || frame.Id != expected)
                throw new ProtocolException("invalid_sequence", "Sequence must increment from 1");
            sequence = expected;
            if (frame.Event == "protocol.error")
            {
                if (body.Property("payload") != null)
                    throw new ProtocolException("invalid_frame", "Error contains payload");
                Terminal = true;
                RemoteError(body["error"]);
            }

            var descriptor = operation["events"]?[frame.Event] as JObject;
            if (descriptor == null || body.Property("error") != null)
                throw new ProtocolException("invalid_event", "Unknown or conflicting event");
            var payload = body["payload"] as JObject;
            if (payload == null || payload.Count != 1 || payload.Property((string)descriptor["field"]) == null)
                throw new ProtocolException("invalid_event", "Event differs from response oneof");
            contract.Validate((string)operation["responseType"], payload);
            Terminal = (bool)descriptor["terminal"];
            return new StreamEvent
            {
                Name = frame.Event,
                RequestId = requestId,
                Sequence = sequence,
                Payload = payload
            };
        }

        public void Finish()
        {
            if (!Terminal)
                throw new ProtocolException("incomplete_stream", "EOF before terminal event");
        }

        public static string NextSequence(string current)
        {
            var digits = current.ToCharArray();
            for (var index = digits.Length - 1; index >= 0; index--)
            {
                if (digits[index] != '9')
                {
                    digits[index]++;
                    return new string (digits);
                }

                digits[index] = '0';
            }

            return "1" + new string (digits);
        }
    }
}
