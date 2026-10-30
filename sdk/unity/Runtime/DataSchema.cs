using System;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    [AttributeUsage(AttributeTargets.Class)]
    public sealed class ProtoMessageAttribute : Attribute
    {
        public string Name { get; }
        public ProtoMessageAttribute(string name) { Name = name; }
    }

    // Loads ProtoJSON configuration with the data bundle budget shared by every
    // TabForge runtime (depth 64, 10,000,000 JSON values). Accepted input is
    // returned as canonical ProtoJSON: 64-bit integers as decimal strings, other
    // numbers as numbers, padded standard Base64, null members removed.
    public sealed class DataSchema
    {
        private readonly WireValidator validator;
        public DataSchema(string wireSchemaJson)
        {
            var schema = Parse(wireSchemaJson) as JObject;
            if (schema == null || !(schema["messages"] is JObject) || !(schema["enums"] is JObject))
                throw new ProtocolException("invalid_schema", "Load generated wire_schema.json");
            validator = new WireValidator(schema, WireValidator.MaxDepth, WireValidator.DataMaxNodes, true);
        }

        public JToken Decode(string message, string json)
        {
            return validator.Validate(message, Parse(json));
        }

        public T Decode<T>(string json) where T : class
        {
            var attr = (ProtoMessageAttribute)Attribute.GetCustomAttribute(typeof(T), typeof(ProtoMessageAttribute));
            if (attr == null) throw new ArgumentException("Use a generated ProtoJSON message class");
            return Decode(attr.Name, json).ToObject<T>(JsonSerializer.Create(new JsonSerializerSettings { DateParseHandling = DateParseHandling.None }));
        }

        // Strict JSON without duplicate members; exact number literals are kept
        // until the validator normalizes them.
        private static JToken Parse(string json)
        {
            var token = json == null ? null : new JsonSyntax(json, WireValidator.MaxDepth, WireValidator.DataMaxNodes).Parse(true);
            if (token == null)
                throw new ProtocolException("invalid_json", "Expected valid JSON without duplicate fields within depth 64 and 10000000 values");
            return token;
        }
    }
}
