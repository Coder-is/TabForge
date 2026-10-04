using System;
using System.IO;
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

    // Loads ProtoJSON configuration; 64-bit integers stay decimal strings.
    public sealed class DataSchema
    {
        private readonly WireValidator validator;
        public DataSchema(string wireSchemaJson)
        {
            var schema = Parse(wireSchemaJson) as JObject;
            if (schema == null || !(schema["messages"] is JObject) || !(schema["enums"] is JObject))
                throw new ProtocolException("invalid_schema", "Load generated wire_schema.json");
            validator = new WireValidator(schema);
        }

        public JToken Decode(string message, string json)
        {
            var data = Parse(json);
            validator.Validate(message, data);
            return data;
        }

        public T Decode<T>(string json) where T : class
        {
            var attr = (ProtoMessageAttribute)Attribute.GetCustomAttribute(typeof(T), typeof(ProtoMessageAttribute));
            if (attr == null) throw new ArgumentException("Use a generated ProtoJSON message class");
            return Decode(attr.Name, json).ToObject<T>(JsonSerializer.Create(new JsonSerializerSettings { DateParseHandling = DateParseHandling.None }));
        }

        private static JToken Parse(string json)
        {
            if (json == null || !new JsonSyntax(json).Valid())
                throw new ProtocolException("invalid_json", "Expected valid JSON");
            try
            {
                using (var reader = new JsonTextReader(new StringReader(json)) { DateParseHandling = DateParseHandling.None, MaxDepth = 64 })
                {
                    return JToken.Load(reader, new JsonLoadSettings { DuplicatePropertyNameHandling = DuplicatePropertyNameHandling.Error });
                }
            }
            catch (JsonException) { throw new ProtocolException("invalid_json", "Expected valid JSON without duplicate fields"); }
        }
    }
}
