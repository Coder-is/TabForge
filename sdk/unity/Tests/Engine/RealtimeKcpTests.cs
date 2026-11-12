using System;
using System.Collections;
using System.IO;
using System.Threading.Tasks;
using Newtonsoft.Json.Linq;
using NUnit.Framework;
using TabForge.Protocol;
using UnityEngine;
using UnityEngine.TestTools;

/// <summary>
/// KCP (UDP) realtime against examples/realtime/server started with -kcp
/// (and -kcp-key for encrypted KCP). Runs in the editor and in players, so
/// it can check IL2CPP builds and devices:
///   editor:  TABFORGE_KCP_URL (kcp://host:port/ws/game), TABFORGE_KCP_KEY
///            (the server's base64 public key; omit for clear text) and
///            TABFORGE_REALTIME_RUNTIME_JSON (path to its runtime.json);
///   player:  Resources/tabforge-kcp.txt ("url" or "url key" on one line)
///            and Resources/tabforge-realtime-runtime.json (TextAsset).
/// Without them the test is ignored, which is not a pass.
/// </summary>
public sealed class TabForgeRealtimeKcpTests
{
    private const int TimeoutSeconds = 20;

    [UnityTest]
    public IEnumerator KcpLoginRoomChatAndPurchases()
    {
        var url = Environment.GetEnvironmentVariable("TABFORGE_KCP_URL");
        var key = Environment.GetEnvironmentVariable("TABFORGE_KCP_KEY");
        var runtimePath = Environment.GetEnvironmentVariable("TABFORGE_REALTIME_RUNTIME_JSON");
        var runtime = !string.IsNullOrEmpty(runtimePath) ? File.ReadAllText(runtimePath) : Resources.Load<TextAsset>("tabforge-realtime-runtime")?.text;
        if (string.IsNullOrEmpty(url))
        {
            var config = Resources.Load<TextAsset>("tabforge-kcp")?.text?.Trim().Split(new[] { ' ', '\t' }, StringSplitOptions.RemoveEmptyEntries);
            if (config != null && config.Length > 0)
            {
                url = config[0];
                key = config.Length > 1 ? config[1] : null;
            }
        }
        if (string.IsNullOrEmpty(url) || string.IsNullOrEmpty(runtime))
            Assert.Ignore("Start examples/realtime/server -kcp and set TABFORGE_KCP_URL/TABFORGE_REALTIME_RUNTIME_JSON (editor) or Resources/tabforge-kcp.txt and tabforge-realtime-runtime.json (player)");

        var player = (ulong)(DateTime.UtcNow.Ticks % 1000000000) + 1; // the server allows one session per user
        var gameObject = new GameObject("TabForge KCP acceptance");
        var realtime = gameObject.AddComponent<TabForgeRealtime>();
        var client = realtime.Configure(runtime, new RealtimeOptions
        {
            Url = url,
            Channel = "game",
            Token = () => "user-" + player,
            KcpServerKey = string.IsNullOrEmpty(key) ? null : Convert.FromBase64String(key),
        });
        RealtimeState last = RealtimeState.Idle;
        ProtocolException failure = null;
        client.StateChanged += (state, error) => { last = state; failure = error ?? failure; };
        JObject chat = null;
        client.On("chatMessage", m => chat = m);
        try
        {
            client.Connect();
            var deadline = Time.realtimeSinceStartup + TimeoutSeconds;
            while (last != RealtimeState.Connected && last != RealtimeState.Closed && Time.realtimeSinceStartup < deadline)
                yield return null;
            Assert.AreEqual(RealtimeState.Connected, last, "connect: " + failure?.Code + " " + failure?.Message);

            var login = client.Request("login", new JObject { ["nickname"] = "勇者😀" });
            yield return Wait(login.Task);
            Assert.AreEqual(player.ToString(), (string)login.Task.Result["playerId"]);
            Assert.AreEqual("1000", (string)login.Task.Result["gold"]);

            var join = client.Request("joinRoom", new JObject { ["room"] = "kcp-" + player });
            yield return Wait(join.Task);

            // Larger than one 16 KiB KCP chunk, with multi-byte characters across chunk edges.
            var text = new string('长', 12000) + "😀";
            var sent = client.Request("chat", new JObject { ["text"] = text });
            yield return Wait(sent.Task);
            deadline = Time.realtimeSinceStartup + TimeoutSeconds;
            while (chat == null && Time.realtimeSinceStartup < deadline)
                yield return null;
            Assert.IsNotNull(chat, "chatMessage push");
            Assert.AreEqual((string)sent.Task.Result["messageId"], (string)chat["messageId"]);
            Assert.AreEqual(text, (string)chat["text"]);

            for (var i = 1; i <= 3; i++)
            {
                var buy = client.Request("buy", new JObject { ["itemId"] = 4294967295L, ["count"] = 1 });
                yield return Wait(buy.Task);
                Assert.AreEqual((1000 - 10 * i).ToString(), (string)buy.Task.Result["gold"]);
                Assert.AreEqual(i, (int)buy.Task.Result["inventory"]["4294967295"]);
            }
        }
        finally
        {
            UnityEngine.Object.Destroy(gameObject); // closes the session
        }
    }

    private static IEnumerator Wait(Task<JObject> task)
    {
        var deadline = Time.realtimeSinceStartup + TimeoutSeconds;
        while (!task.IsCompleted && Time.realtimeSinceStartup < deadline)
            yield return null;
        Assert.IsTrue(task.IsCompleted, "request timed out");
        if (task.IsFaulted)
            Assert.Fail(task.Exception?.InnerException?.Message);
    }
}
