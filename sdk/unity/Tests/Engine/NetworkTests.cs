using System;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using Newtonsoft.Json.Linq;
using NUnit.Framework;
using TabForge.Protocol;
using UnityEngine;
using UnityEngine.TestTools;

public sealed class TabForgeNetworkTests
{
    [UnityTest]
    public IEnumerator NativeHttpAndStreaming()
    {
        var url = Environment.GetEnvironmentVariable("TABFORGE_TEST_URL");
        if (string.IsNullOrEmpty(url)) Assert.Ignore("Start examples/platforms/server and set TABFORGE_TEST_URL");
        var path = Environment.GetEnvironmentVariable("TABFORGE_RUNTIME_JSON");
        var metadata = !string.IsNullOrEmpty(path) ? File.ReadAllText(path) : Resources.Load<TextAsset>("tabforge-runtime")?.text;
        Assert.IsNotNull(metadata, "Run examples/platforms/build.mjs or set TABFORGE_RUNTIME_JSON");
        var root = JObject.Parse(metadata);
        var gameObject = new GameObject("TabForge acceptance");
        var client = gameObject.AddComponent<TabForgeClient>();
        client.Configure(url, metadata);
        var request = JObject.Parse("{\"prompt\":\"你好😀\",\"conversationId\":\"18446744073709551615\"}");
        try
        {
            var unary = client.Request("chatComplete", request);
            yield return Wait(unary);
            Assert.IsNull(unary.Error, unary.Error?.Message);
            Assert.AreEqual("你好😀", (string)unary.Result["text"]);
            Assert.AreEqual("18446744073709551615", (string)unary.Result["usage"]["outputTokens"]);
            var events = new List<StreamEvent>();
            var stream = client.Stream("chatStream", request);
            stream.EventReceived += events.Add;
            yield return Wait(stream);
            Assert.IsNull(stream.Error, stream.Error?.Message);
            Assert.AreEqual(2, events.Count);
            Assert.AreEqual("你好😀", (string)events[0].Payload["delta"]["text"]);
            Assert.AreEqual("completed", events[1].Name);
            Assert.AreEqual("2", events[1].Sequence);

            var cancelled = client.Stream("chatStream", new JObject { ["prompt"] = "cancel" });
            var incremental = false;
            cancelled.EventReceived += value => { incremental = true; cancelled.Cancel(); };
            yield return Wait(cancelled);
            Assert.IsTrue(incremental, "No event before server EOF");
            Assert.AreEqual("cancelled", cancelled.Error?.Code);

            root["operations"]["chatStream"]["timeoutMS"] = 150;
            client.Configure(url, root.ToString());
            var timeout = client.Stream("chatStream", new JObject { ["prompt"] = "timeout" });
            yield return Wait(timeout);
            Assert.AreEqual("timeout", timeout.Error?.Code);
            root = JObject.Parse(metadata);
            foreach (var fault in new[] { "gap", "schema", "type", "eof", "large", "utf8" })
            {
                root["operations"]["chatStream"]["path"] = "/_platform/fault/" + fault;
                client.Configure(url, root.ToString());
                var handle = client.Stream("chatStream", request);
                yield return Wait(handle);
                var codes = new Dictionary<string, string> { ["gap"] = "invalid_sequence", ["schema"] = "schema_mismatch", ["type"] = "invalid_message", ["eof"] = "incomplete_stream", ["large"] = "frame_too_large", ["utf8"] = "invalid_utf8" };
                Assert.AreEqual(codes[fault], handle.Error?.Code, fault);
            }
            client.Configure(url, metadata);
            var disabled = client.Stream("chatStream", new JObject { ["prompt"] = "cancel" });
            client.enabled = false;
            Assert.IsTrue(disabled.Done);
            Assert.AreEqual("cancelled", disabled.Error?.Code);
        }
        finally { UnityEngine.Object.DestroyImmediate(gameObject); }
    }
    private static IEnumerator Wait(RequestHandle handle)
    {
        var until = Time.realtimeSinceStartup + 10;
        while (!handle.Done && Time.realtimeSinceStartup < until) yield return null;
        if (!handle.Done) { handle.Cancel(); Assert.Fail("Native request did not complete within test deadline"); }
        yield return null; // Coroutine releases its active request before reconfiguration.
    }
}
