#if !UNITY_5_3_OR_NEWER
using System;
using System.Collections.Generic;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Newtonsoft.Json.Linq;
using TabForge.Protocol;

internal static class CoreTests
{
    private static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }
    private static void Rejected(Action action, string code)
    {
        try { action(); } catch (ProtocolException error) { Check(error.Code == code, "Expected " + code + " got " + error.Code); return; }
        throw new Exception("Expected " + code);
    }
    public static async Task<int> Main(string[] args)
    {
        var runtime = args.Length > 0 ? args[0] : "../../../examples/protocol/generated/runtime.json";
        var contract = new ProtocolContract(File.ReadAllText(runtime));
        var op = contract.Operation("chatStream", "http_sse");
        const string id = "csharp-core";
        Func<string, string, JObject, string> frame = (sequence, name, payload) => "id: " + sequence + "\nevent: " + name + "\ndata: " + new JObject { ["protocolVersion"] = contract.Version, ["schemaHash"] = contract.SchemaHash, ["requestId"] = id, ["sequence"] = sequence, ["payload"] = payload }.ToString(Newtonsoft.Json.Formatting.None) + "\n\n";
        var first = frame("1", "text.delta", new JObject { ["delta"] = new JObject { ["text"] = "你好😀" } });
        var bytes = Encoding.UTF8.GetBytes("\ufeff: heartbeat\r\n\r\n" + first.Replace("\n", "\r\n") + frame("2", "completed", new JObject { ["completed"] = new JObject() }));
        for (var split = 0; split <= bytes.Length; split++)
        {
            var parser = new SseParser(); var session = new ProtocolSession(contract, op, id); var events = new List<StreamEvent>();
            parser.Feed(bytes.AsSpan(0, split).ToArray(), split, f => events.Add(session.Accept(f)));
            parser.Feed(bytes.AsSpan(split).ToArray(), bytes.Length - split, f => events.Add(session.Accept(f)));
            parser.Finish(); session.Finish();
            Check(events.Count == 2 && (string)events[0].Payload["delta"]["text"] == "你好😀", "Unicode or frame boundary lost");
        }
        Rejected(() => new SseParser().Feed(new byte[] { 0xed, 0xa0, 0x80 }, 3, _ => { }), "invalid_utf8");
        var truncated = new SseParser(); truncated.Feed(new byte[] { 0xe4 }, 1, _ => { }); Rejected(truncated.Finish, "invalid_utf8");
        Rejected(() => new SseParser(2).Feed(Encoding.UTF8.GetBytes("data: x\n\n"), 9, _ => { }), "frame_too_large");
        var validator = new WireValidator(contract.WireSchema);
        var numbers = new WireValidator(JObject.Parse("{\"messages\":{\"Numbers\":{\"fields\":{\"i\":{\"kind\":\"int32\"},\"u\":{\"kind\":\"uint32\"},\"e\":{\"kind\":\"enum\",\"type\":\"Role\"},\"d\":{\"kind\":\"double\"}}}},\"enums\":{\"Role\":[\"NONE\"]}}"));
        numbers.Validate("Numbers", JObject.Parse("{\"i\":42.0,\"u\":4294967295.0,\"e\":0.0}"));
        Rejected(() => numbers.Validate("Numbers", JObject.Parse("{\"i\":42.5}")), "invalid_message");
        Rejected(() => numbers.Validate("Numbers", JObject.Parse("{\"e\":2147483648}")), "invalid_message");
        Rejected(() => numbers.Validate("Numbers", new JObject { ["d"] = new JValue(System.Numerics.BigInteger.Pow(10, 500)) }), "invalid_message");
        validator.Validate("tabforge.example.ChatRequest", JObject.Parse("{\"conversationId\":\"18446744073709551615\"}"));
        Rejected(() => validator.Validate("tabforge.example.ChatRequest", JObject.Parse("{\"conversationId\":18446744073709551615}")), "invalid_message");
        Rejected(() => validator.Validate("tabforge.example.ChatRequest", JObject.Parse("{\"conversationId\":\"18446744073709551616\"}")), "invalid_message");
        Rejected(() => validator.Validate("tabforge.example.ChatRequest", JObject.Parse("{\"unknown\":1}")), "invalid_message");
        Rejected(() => validator.Validate("google.protobuf.Timestamp", new JValue("2025-02-30T00:00:00Z")), "invalid_message");
        validator.Validate("google.protobuf.Timestamp", new JValue("2024-02-29T00:00:00.123456789Z"));
        Rejected(() => Json.Parse("{\"x\":1,\"x\":2}"), "invalid_json");
        foreach (var invalid in new[] { "{'x':1}", "{\"x\":1,}", "[1,]", "{\"x\":NaN}", "{} []", "{\"x\":\"\\ud800\"}" }) Rejected(() => Json.Parse(invalid), "invalid_json");
        Rejected(() => validator.Validate("tabforge.example.ChatRequest", JObject.Parse("{\"conversationId\":\"1\\n\"}")), "invalid_message");
        Rejected(() => new ProtocolSession(contract, op, id).Accept(new SseFrame { Id = "2", Event = "text.delta", Data = first.Substring(first.IndexOf("data: ") + 6).Trim() }), "invalid_sequence");
        Rejected(() => new ProtocolSession(contract, op, id).Finish(), "incomplete_stream");
        Rejected(() => new ProtocolSession(contract, op, "id\n"), "bad_request");
        Rejected(() => new ProtocolSession(contract, op, id).Unary("{\"protocolVersion\":[]}", 200), "invalid_frame");
        foreach (var pair in new[] { new[] { "Timestamp", "2024-02-29T12:34:56Z\n" }, new[] { "FieldMask", "userId\n" }, new[] { "BytesValue", "YWJj\n" } }) Rejected(() => validator.Validate("google.protobuf." + pair[0], new JValue(pair[1])), "invalid_message");
        Check(ProtocolSession.NextSequence("18446744073709551615") == "18446744073709551616", "Sequence precision lost");
        Console.WriteLine("C# protocol core units passed");
        if (args.Length > 1) await Integration(contract, args[1]);
        return 0;
    }
    private static async Task Integration(ProtocolContract contract, string url)
    {
        using var http = new HttpClient();
        var request = JObject.Parse("{\"prompt\":\"你好😀\",\"conversationId\":\"18446744073709551615\"}");
        foreach (var id in new[] { "chatComplete", "chatStream" })
        {
            var stream = id == "chatStream"; var op = contract.Operation(id, stream ? "http_sse" : "http_json");
            using var message = new HttpRequestMessage(HttpMethod.Post, url + (string)op["path"]);
            message.Headers.Add("X-Protocol-Version", contract.Version); message.Headers.Add("X-Protocol-Schema", contract.SchemaHash); message.Headers.Add("X-Request-ID", "csharp-http");
            message.Content = new StringContent(request.ToString(Newtonsoft.Json.Formatting.None), Encoding.UTF8, "application/json");
            using var cancel = new CancellationTokenSource(TimeSpan.FromSeconds(10));
            using var response = await http.SendAsync(message, HttpCompletionOption.ResponseHeadersRead, cancel.Token);
            var session = new ProtocolSession(contract, op, "csharp-http");
            if (!stream)
            {
                var value = session.Unary(await response.Content.ReadAsStringAsync(), (int)response.StatusCode);
                Check((string)value["text"] == "你好😀" && (string)value["usage"]["outputTokens"] == "18446744073709551615", "Go to C# unary mismatch");
            }
            else
            {
                var parser = new SseParser(); var events = new List<StreamEvent>();
                using var body = await response.Content.ReadAsStreamAsync(); var one = new byte[1];
                while (!session.Terminal && await body.ReadAsync(one, 0, 1, cancel.Token) > 0) parser.Feed(one, 1, f => events.Add(session.Accept(f)));
                parser.Finish(); session.Finish();
                Check(events.Count == 2 && (string)events[0].Payload["delta"]["text"] == "你好😀", "Go to C# stream mismatch");
            }
        }
        Console.WriteLine("Go ↔ C# core HTTP/SSE integration passed (not Unity engine validation)");
    }
}
#endif
