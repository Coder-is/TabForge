#if !UNITY_5_3_OR_NEWER
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;
using TabForge.Protocol;

/// <summary>
/// Realtime client tests in plain .NET (no Unity engine):
///   unit &lt;runtime.json&gt;                         scripted fake server
///   codec &lt;runtime.json&gt; &lt;cases&gt; &lt;out&gt;           Go/C# codec equivalence (driven by Go tests)
///   chaos &lt;runtime.json&gt; &lt;url&gt; &lt;proto|json&gt; &lt;s&gt;   against a Go server behind a cutting proxy
/// </summary>
internal static class RealtimeTests
{
    private static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

    public static int Main(string[] args)
    {
        var runtime = File.ReadAllText(args[1]);
        switch (args[0])
        {
            case "unit": Unit(runtime); break;
            case "codec": Codec(runtime, args[2], args[3]); break;
            case "chaos": Chaos(runtime, args[2], args[3] == "json" ? RealtimeEncoding.Json : RealtimeEncoding.Proto, int.Parse(args[4])).GetAwaiter().GetResult(); break;
            default: throw new ArgumentException(args[0]);
        }
        return 0;
    }

    // ---- scripted fake server (JSON subprotocol) ----

    private sealed class FakeSocket : IRealtimeSocket
    {
        public IRealtimeSocketEvents Events;
        public string Protocol;
        public readonly List<JObject> Sent = new List<JObject>();
        public bool Closed;
        public void Send(byte[] data, bool text) { Check(text, "JSON frames are text"); Sent.Add(JObject.Parse(Encoding.UTF8.GetString(data))); }
        public void Close() { Closed = true; }
        public void Reply(object frame) => Events.Received(Encoding.UTF8.GetBytes(JsonConvert.SerializeObject(frame)), true);
        public void Welcome(bool resumed = false, string session = "s1") => Reply(new { kind = "welcome", welcome = new { sessionId = session, resumeToken = "t", heartbeatMS = 5000, resumed } });
        public void Cut() { Closed = true; Events.Closed(); }
    }

    private sealed class Fake
    {
        public readonly List<FakeSocket> Sockets = new List<FakeSocket>();
        public FakeSocket Last => Sockets[Sockets.Count - 1];
        public IRealtimeSocket Open(string url, string protocol, IRealtimeSocketEvents events)
        {
            var socket = new FakeSocket { Events = events, Protocol = protocol };
            Sockets.Add(socket);
            events.Opened(protocol);
            return socket;
        }
    }

    private static void Until(RealtimeClient client, Func<bool> condition, string what)
    {
        for (var i = 0; i < 2000; i++)
        {
            client.Tick();
            if (condition())
                return;
            Thread.Sleep(1);
        }
        throw new Exception("Timed out waiting for " + what);
    }

    private static (RealtimeClient, Fake, List<string>) Start(string runtime, int maxUnacked = 1024)
    {
        var fake = new Fake();
        var states = new List<string>();
        var client = new RealtimeClient(runtime, new RealtimeOptions { Url = "ws://x/ws/game", Channel = "game", Encoding = RealtimeEncoding.Json, Token = () => "user-1", Socket = fake.Open, MinBackoffMS = 1, MaxBackoffMS = 2, MaxUnacked = maxUnacked });
        client.StateChanged += (state, error) => states.Add(error == null ? state.ToString() : state + ":" + error.Code);
        client.Connect();
        Until(client, () => fake.Sockets.Count == 1 && fake.Last.Sent.Count == 1, "hello");
        return (client, fake, states);
    }

    private static ProtocolException Failure(RealtimeClient client, RealtimeRequest request)
    {
        Until(client, () => request.Task.IsCompleted, "request");
        Check(request.Task.IsFaulted, "request should fail");
        return (ProtocolException)request.Task.Exception.InnerException;
    }

    private static JObject Result(RealtimeClient client, RealtimeRequest request)
    {
        Until(client, () => request.Task.IsCompleted, "request");
        return request.Task.Result;
    }

    private static void Unit(string runtime)
    {
        {
            var (c, fake, states) = Start(runtime);
            var hello = fake.Last.Sent[0];
            Check(fake.Last.Protocol == RealtimeClient.SubprotocolJson, "subprotocol");
            Check((string)hello["kind"] == "hello" && (string)hello["hello"]["token"] == "user-1" && hello["hello"]["sessionId"] == null, "hello " + hello);
            fake.Last.Welcome();
            Until(c, () => c.State == RealtimeState.Connected, "welcome");
            JObject callbackValue = null;
            var login = c.Request("login", new JObject { ["nickname"] = "勇者😀" });
            login.Completed += v => callbackValue = v;
            var sent = fake.Last.Sent.Last();
            Check((string)sent["kind"] == "request" && (string)sent["seq"] == "1" && (int)sent["message"] == 1, "request " + sent);
            fake.Last.Reply(new { kind = "response", seq = "1", ack = "1", message = 1, replyTo = "1", payload = new { playerId = "7" } });
            Check((string)Result(c, login)["playerId"] == "7" && callbackValue != null, "response");
            // Validation and normalization before sending.
            var bad = Catch(() => c.Request("chat", new JObject { ["text"] = 1 }));
            Check(bad.Code == "invalid_message", "invalid request " + bad.Code);
            var original = new JObject { ["itemId"] = 1.0, ["count"] = "2" };
            c.Request("buy", original);
            Check(JToken.DeepEquals(fake.Last.Sent.Last()["payload"], JObject.Parse("{\"itemId\":1,\"count\":2}")), "normalized " + fake.Last.Sent.Last());
            Check(original["count"].Type == JTokenType.String, "caller object modified");
            // Pushes: duplicate dropped, unknown ignored, handler errors isolated.
            var pushes = new List<string>();
            var errors = 0;
            c.HandlerError += _ => errors++;
            c.On("chatMessage", m => { pushes.Add((string)m["messageId"]); if ((string)m["messageId"] == "2") throw new Exception("handler bug"); });
            fake.Last.Reply(new { kind = "push", seq = "2", message = 201, payload = new { messageId = "1" } });
            fake.Last.Reply(new { kind = "push", seq = "2", message = 201, payload = new { messageId = "1" } });
            fake.Last.Reply(new { kind = "push", seq = "3", message = 999, payload = new { } });
            fake.Last.Reply(new { kind = "push", seq = "4", message = 201, payload = new { messageId = "2" } });
            fake.Last.Reply(new { kind = "push", seq = "5", message = 201, payload = new { messageId = "3" } });
            Until(c, () => pushes.Count == 3, "pushes");
            Check(string.Join(",", pushes) == "1,2,3" && errors == 1, "pushes " + string.Join(",", pushes) + " errors " + errors);
            Until(c, () => fake.Last.Sent.Any(f => (string)f["kind"] == "ack" && (string)f["ack"] == "5"), "ack");
            c.Close();
            Check((string)fake.Last.Sent.Last()["kind"] == "close" && fake.Last.Closed, "close frame");
            Check(states.Last() == "Closed:closed", "closed state");
        }
        {
            // Resume retransmits; expiry fails only requests that may have run.
            var (c, fake, states) = Start(runtime);
            fake.Last.Welcome();
            Until(c, () => c.State == RealtimeState.Connected, "welcome");
            var first = c.Request("chat", new JObject { ["text"] = "one" });
            fake.Last.Cut();
            Until(c, () => fake.Sockets.Count == 2 && fake.Last.Sent.Count == 1, "reconnect");
            var hello = fake.Last.Sent[0]["hello"];
            Check((string)hello["sessionId"] == "s1" && hello["token"] == null, "resume hello " + hello);
            fake.Last.Welcome(true);
            Until(c, () => c.State == RealtimeState.Resumed, "resumed");
            Check((string)fake.Last.Sent.Last()["seq"] == "1", "retransmitted");
            fake.Last.Reply(new { kind = "response", seq = "1", ack = "1", message = 3, replyTo = "1", payload = new { messageId = "9" } });
            Check((string)Result(c, first)["messageId"] == "9", "resumed response");
            var uncertain = c.Request("chat", new JObject { ["text"] = "two" });
            fake.Last.Cut();
            c.Tick();
            var queued = c.Request("chat", new JObject { ["text"] = "three" }); // never written
            Until(c, () => fake.Sockets.Count == 3 && fake.Last.Sent.Count == 1, "third");
            fake.Last.Reply(new { kind = "close", error = new { code = "session_expired", message = "gone", retryable = true } });
            Check(Failure(c, uncertain).Code == "session_lost", "session_lost");
            Until(c, () => fake.Sockets.Count == 4 && fake.Last.Sent.Count == 1, "new session");
            Check((string)fake.Last.Sent[0]["hello"]["token"] == "user-1", "token for new session");
            fake.Last.Welcome(false, "s2");
            Until(c, () => c.SessionId == "s2", "s2");
            var carried = fake.Last.Sent.Last();
            Check((string)carried["seq"] == "1" && (string)carried["payload"]["text"] == "three", "carried " + carried);
            fake.Last.Reply(new { kind = "response", seq = "1", ack = "1", message = 3, replyTo = "1", payload = new { } });
            Result(c, queued);
            Check(states.Count(s => s == "Connected") == 2 && states.Contains("Resumed"), "states " + string.Join(",", states));
            c.Close();
        }
        {
            // Protocol errors reconnect; permanent closes stop.
            var (c, fake, states) = Start(runtime);
            fake.Last.Welcome();
            Until(c, () => c.State == RealtimeState.Connected, "welcome");
            var pending = c.Request("chat", new JObject { ["text"] = "x" });
            fake.Last.Reply(new { kind = "push", seq = "5", message = 201, payload = new { } }); // gap
            Until(c, () => fake.Sockets.Count == 2 && fake.Last.Sent.Count >= 1, "reconnect after gap");
            fake.Last.Welcome(true);
            fake.Last.Events.Received(Encoding.UTF8.GetBytes("{\"kind\":\"push\",\"kind\":\"push\"}"), true);
            Until(c, () => fake.Sockets.Count == 3 && fake.Last.Sent.Count >= 1, "reconnect after garbage");
            fake.Last.Welcome(true);
            fake.Last.Reply(new { kind = "close", error = new { code = "duplicate_login", message = "elsewhere", retryable = false } });
            Check(Failure(c, pending).Code == "duplicate_login", "pending rejected");
            Check(c.State == RealtimeState.Closed, "closed");
            Check(Catch(() => c.Request("chat", new JObject())).Code == "duplicate_login", "closed client rejects");
            for (var i = 0; i < 20; i++) { c.Tick(); Thread.Sleep(1); }
            Check(fake.Sockets.Count == 3, "no reconnect after permanent close");
        }
        {
            // Timeouts, backpressure, volatile drops.
            var (c, fake, _) = Start(runtime, 3);
            fake.Last.Welcome();
            Until(c, () => c.State == RealtimeState.Connected, "welcome");
            Check(Failure(c, c.Request("chat", new JObject { ["text"] = "a" }, 5)).Code == "timeout", "timeout");
            c.Request("chat", new JObject { ["text"] = "b" });
            c.Request("chat", new JObject { ["text"] = "c" });
            Check(Catch(() => c.Request("chat", new JObject { ["text"] = "d" })).Code == "overloaded", "backpressure");
            fake.Last.Reply(new { kind = "response", seq = "1", ack = "3", message = 3, replyTo = "1", payload = new { } }); // late answer ignored
            c.Tick();
            Check(c.Notify("move", new JObject { ["tick"] = 1 }), "volatile sent");
            var notify = fake.Last.Sent.Last();
            Check((string)notify["kind"] == "notify" && notify["seq"] == null, "volatile frame " + notify);
            fake.Last.Cut();
            c.Tick();
            Check(!c.Notify("move", new JObject { ["tick"] = 2 }), "volatile dropped offline");
            c.Close();
        }
        // Binary codec spot checks.
        var codec = new ProtobufCodec((JObject)JObject.Parse(runtime)["codecSchema"]);
        var message = (JObject)Json.Parse("{\"messageId\":\"18446744073709551615\",\"from\":\"1\",\"text\":\"hi\",\"sentAt\":\"2023-11-14T22:13:20.000000005Z\"}");
        Check(JToken.DeepEquals(codec.Decode("tabforge.game.ChatMessage", codec.Encode("tabforge.game.ChatMessage", message)), message), "ChatMessage round trip");
        Check((string)codec.Decode("tabforge.game.ChatMessage", codec.Encode("tabforge.game.ChatMessage", Json.Parse("{\"sentAt\":\"2024-01-01T08:00:00+08:00\"}")))["sentAt"] == "2024-01-01T00:00:00Z", "timestamp offset");
        Check(JToken.DeepEquals(codec.Decode("tabforge.game.Move", new byte[] { 0x10, 1, 0x10, 2 }), JObject.Parse("{\"tick\":2}")), "last scalar wins");
        Check(JToken.DeepEquals(codec.Decode("tabforge.game.Move", new byte[] { 0x78, 1, 0x10, 3 }), JObject.Parse("{\"tick\":3}")), "unknown skipped");
        Check(CatchAny(() => codec.Decode("tabforge.game.Move", new byte[] { 0x12, 5, 1 })), "truncated");
        Check(CatchAny(() => codec.Decode("tabforge.game.Move", new byte[] { 0x12, 1 })), "wrong wire type");
        var welcome = RealtimeFrame.DecodeBinary(new byte[] { 0x08, 0x02, 0x4a, 0x09, 0x0a, 0x01, 0x73, 0x12, 0x01, 0x74, 0x20, 0x88, 0x27 });
        welcome.ValidateFromServer();
        Check(welcome.Kind == RealtimeFrame.Welcome && welcome.WelcomeSessionId == "s" && welcome.WelcomeHeartbeatMS == 5000, "binary welcome");
        Console.WriteLine("C# realtime units passed");
    }

    private static ProtocolException Catch(Action action)
    {
        try { action(); } catch (ProtocolException error) { return error; }
        throw new Exception("expected ProtocolException");
    }

    private static bool CatchAny(Action action)
    {
        try { action(); } catch (ProtocolException) { return true; }
        return false;
    }

    // ---- Go/C# codec equivalence ----

    private static bool Same(JToken a, JToken b)
    {
        if ((a.Type == JTokenType.Float || a.Type == JTokenType.Integer) && (b.Type == JTokenType.Float || b.Type == JTokenType.Integer))
        {
            var x = (double)a;
            var y = (double)b;
            return x == y || (float)x == (float)y;
        }
        if (a is JObject oa && b is JObject ob)
            return oa.Count == ob.Count && oa.Properties().All(p => ob[p.Name] != null && Same(p.Value, ob[p.Name]));
        if (a is JArray la && b is JArray lb)
            return la.Count == lb.Count && la.Zip(lb, Same).All(x => x);
        return JToken.DeepEquals(a, b);
    }

    private static void Codec(string runtime, string input, string output)
    {
        var body = JObject.Parse(runtime);
        var codec = new ProtobufCodec((JObject)body["codecSchema"]);
        var normalizer = new WireValidator((JObject)body["wireSchema"], WireValidator.MaxDepth, WireValidator.ProtocolMaxNodes, true);
        var validator = new WireValidator((JObject)body["wireSchema"]);
        var results = new JArray();
        var cases = (JArray)Json.Parse(File.ReadAllText(input));
        foreach (JObject c in cases)
        {
            var type = (string)c["type"];
            var expected = Json.Parse((string)c["json"]);
            var decoded = codec.Decode(type, Convert.FromBase64String((string)c["binary"]));
            if (!Same(decoded, expected))
                throw new Exception(type + " decoded differently\nGo: " + c["json"] + "\nC#: " + decoded.ToString(Formatting.None));
            validator.Validate(type, decoded);
            var fromJson = codec.Encode(type, normalizer.Validate(type, Json.Parse((string)c["json"])));
            var fromDecoded = codec.Encode(type, decoded);
            results.Add(new JObject { ["FromJSON"] = Convert.ToBase64String(fromJson), ["FromDecoded"] = Convert.ToBase64String(fromDecoded) });
        }
        File.WriteAllText(output, results.ToString(Formatting.None));
        Console.WriteLine("codec: " + cases.Count + " messages match Go");
    }

    // ---- against the Go server behind a cutting proxy ----

    private static async Task Chaos(string runtime, string url, RealtimeEncoding encoding, int seconds)
    {
        var states = new List<string>();
        var client = new RealtimeClient(runtime, new RealtimeOptions { Url = url, Channel = "game", Encoding = encoding, Token = () => "user-31", MinBackoffMS = 5, MaxBackoffMS = 50 });
        client.StateChanged += (state, error) => { lock (states) states.Add(error == null ? state.ToString() : state + ":" + error.Code); };
        var pushes = new List<long>();
        client.On("chatMessage", m => pushes.Add(long.Parse((string)m["messageId"])));
        // Tick on a dedicated thread, as Unity's Update would.
        var stop = new CancellationTokenSource();
        var ticker = new Thread(() => { while (!stop.IsCancellationRequested) { client.Tick(); Thread.Sleep(1); } }) { IsBackground = true };
        ticker.Start();
        try
        {
            client.Connect();
            var deadline = DateTime.UtcNow.AddSeconds(15);
            while (client.State != RealtimeState.Connected && DateTime.UtcNow < deadline)
                await Task.Delay(5);
            var session = client.SessionId;
            var login = await client.Request("login", new JObject { ["nickname"] = "勇者😀" }).Task;
            Check((string)login["playerId"] == "31" && (string)login["nickname"] == "勇者😀" && (string)login["gold"] == "100", "login " + login);
            var golds = new List<long>();
            var end = DateTime.UtcNow.AddSeconds(seconds);
            await Task.WhenAll(Enumerable.Range(0, 8).Select(async _ =>
            {
                while (DateTime.UtcNow < end)
                {
                    var reply = await client.Request("buy", new JObject { ["itemId"] = 4294967295L, ["count"] = 2 }).Task;
                    Check((long)reply["inventory"]["4294967295"] == 2, "inventory " + reply);
                    lock (golds) golds.Add(long.Parse((string)reply["gold"]));
                }
            }));
            golds.Sort();
            for (var i = 0; i < golds.Count; i++)
                Check(golds[i] == i + 1, "a purchase ran twice or was lost at " + i);
            var wait = DateTime.UtcNow.AddSeconds(10);
            while (pushes.Count < golds.Count && DateTime.UtcNow < wait)
                await Task.Delay(10);
            Check(pushes.Count == golds.Count, "pushes " + pushes.Count + " of " + golds.Count);
            for (var i = 0; i < pushes.Count; i++)
                Check(pushes[i] == i + 1, "pushes lost, duplicated or reordered at " + i);
            Check(client.SessionId == session, "session was replaced instead of resumed");
            var error = await Assert(client.Request("chat", new JObject { ["text"] = "" }).Task);
            Check(error.Code == "empty_message" && error.Message == "say something", "business error " + error.Code);
            Check((await Assert(client.Request("chat", new JObject { ["text"] = "slow" }, 50).Task)).Code == "timeout", "timeout");
            JObject moved = null;
            client.On("playerMoved", m => moved = m);
            while (moved == null)
            {
                client.Notify("move", new JObject { ["position"] = new JObject { ["x"] = 1.5, ["y"] = -2 }, ["tick"] = 9 });
                await Task.Delay(20);
            }
            Check((int)moved["tick"] == 9 && (double)moved["position"]["x"] == 1.5, "moved " + moved);
            await client.Request("chat", new JObject { ["text"] = "kick" }).Task;
            wait = DateTime.UtcNow.AddSeconds(5);
            while (client.State != RealtimeState.Closed && DateTime.UtcNow < wait)
                await Task.Delay(10);
            lock (states)
                Check(states.Last() == "Closed:kicked", "kick: " + string.Join(",", states.Skip(Math.Max(0, states.Count - 10))));
            lock (states)
                Console.WriteLine(new JObject { ["requests"] = golds.Count, ["resumes"] = states.Count(s => s == "Resumed") }.ToString(Formatting.None));
        }
        finally
        {
            stop.Cancel();
            ticker.Join();
        }
    }

    private static async Task<ProtocolException> Assert(Task task)
    {
        try { await task; } catch (ProtocolException error) { return error; }
        throw new Exception("expected failure");
    }
}
#endif
