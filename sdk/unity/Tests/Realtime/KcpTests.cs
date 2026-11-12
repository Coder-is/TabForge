#if !UNITY_5_3_OR_NEWER
using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using Newtonsoft.Json.Linq;
using TabForge.Protocol;

/// <summary>
/// KCP core and encrypted-KCP crypto. Reference values come from Go
/// (crypto/ecdh, x/crypto/chacha20poly1305 and the fixed-key vectors in
/// protocol/realtime/testdata), not from this implementation.
/// </summary>
internal static class KcpTests
{
    private static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }

    private static byte[] Hex(string s)
    {
        var b = new byte[s.Length / 2];
        for (var i = 0; i < b.Length; i++)
            b[i] = Convert.ToByte(s.Substring(2 * i, 2), 16);
        return b;
    }

    private static string Hex(byte[] b) => string.Concat(b.Select(x => x.ToString("x2")));

    public static void Run(string casesPath, string vectorsPath)
    {
        var cases = JObject.Parse(File.ReadAllText(casesPath));
        Sha256AndHmac();
        X25519((JArray)cases["x25519"]);
        Aead((JArray)cases["aead"]);
        Vectors(JObject.Parse(File.ReadAllText(vectorsPath)));
        CoreUnderLoss();
        Console.WriteLine("kcp: core and crypto match Go");
    }

    /// <summary>The written-out SHA-256 and HMAC against .NET's, across block boundaries and long keys.</summary>
    private static void Sha256AndHmac()
    {
        Check(Hex(KcpCrypto.Sha256(System.Text.Encoding.ASCII.GetBytes("abc"))) == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", "SHA-256 of abc (FIPS 180-4)");
        var r = new Random(11);
        var hmac = typeof(KcpCrypto).GetMethod("Hmac", System.Reflection.BindingFlags.NonPublic | System.Reflection.BindingFlags.Static);
        using (var sha = SHA256.Create())
        {
            for (var n = 0; n < 300; n++)
            {
                var data = new byte[n];
                r.NextBytes(data);
                Check(Hex(KcpCrypto.Sha256(data)) == Hex(sha.ComputeHash(data)), "SHA-256 of " + n + " bytes");
                var key = new byte[r.Next(0, 150)];
                r.NextBytes(key);
                using (var reference = new HMACSHA256(key))
                    Check(Hex((byte[])hmac.Invoke(null, new object[] { key, data })) == Hex(reference.ComputeHash(data)), "HMAC with a " + key.Length + "-byte key");
            }
        }
        Console.WriteLine("sha256, hmac: 300 lengths match .NET");
    }

    private static void X25519(JArray cases)
    {
        foreach (JObject c in cases)
        {
            var got = KcpCrypto.X25519(Hex((string)c["scalar"]), Hex((string)c["point"]));
            Check(Hex(got) == (string)c["out"], "X25519 " + c);
        }
        // A low-order point gives the all-zero value, which must be refused.
        var threw = false;
        try { KcpCrypto.X25519(KcpCrypto.RandomBytes(32), new byte[32]); } catch (CryptographicException) { threw = true; }
        Check(threw, "X25519 accepted a low-order point");
        Console.WriteLine("x25519: " + cases.Count + " cases");
    }

    private static void Aead(JArray cases)
    {
        var native = 0;
        foreach (JObject c in cases)
        {
            byte[] key = Hex((string)c["key"]), nonce = Hex((string)c["nonce"]), aad = Hex((string)c["aad"]), plain = Hex((string)c["plain"]), want = Hex((string)c["sealed"]);
            var got = new byte[plain.Length + 16];
            KcpCrypto.Seal(key, nonce, aad, 0, aad.Length, plain, 0, plain.Length, got, 0);
            Check(Hex(got) == (string)c["sealed"], "seal " + c);
            var opened = KcpCrypto.Open(key, nonce, aad, 0, aad.Length, want, 0, want.Length);
            Check(opened != null && Hex(opened) == (string)c["plain"], "open " + c);
            for (var i = 0; i < want.Length; i += Math.Max(1, want.Length / 7))
            {
                var bad = (byte[])want.Clone();
                bad[i] ^= 0x01;
                Check(KcpCrypto.Open(key, nonce, aad, 0, aad.Length, bad, 0, bad.Length) == null, "opened a damaged datagram at byte " + i);
            }
            if (aad.Length > 0)
            {
                var badAad = (byte[])aad.Clone();
                badAad[0] ^= 0x80;
                Check(KcpCrypto.Open(key, nonce, badAad, 0, badAad.Length, want, 0, want.Length) == null, "opened with different associated data");
            }
            if (ChaCha20Poly1305.IsSupported)
            {
                using (var aead = new ChaCha20Poly1305(key))
                {
                    var ct = new byte[plain.Length];
                    var tag = new byte[16];
                    aead.Encrypt(nonce, plain, ct, tag, aad);
                    Check(Hex(ct) + Hex(tag) == (string)c["sealed"], ".NET ChaCha20Poly1305 disagrees: " + c);
                }
                native++;
            }
        }
        Console.WriteLine("aead: " + cases.Count + " cases (" + native + " also against .NET's ChaCha20Poly1305)");
    }

    private static void Vectors(JObject v)
    {
        var conv = (uint)v["conv"];
        var serverPublic = Hex((string)v["server_static_public"]);
        Check(Hex(KcpCrypto.X25519Public(Hex((string)v["server_static_private"]))) == (string)v["server_static_public"], "server public key");
        var h = new KcpCrypto.ClientHandshake(conv, serverPublic, Hex((string)v["client_ephemeral_private"]));
        Check(Hex(h.Hello) == (string)v["hello"], "hello " + Hex(h.Hello));
        var welcome = Hex((string)v["welcome"]);
        var client = h.Finish(welcome, welcome.Length);
        Check(client != null, "the client rejected the genuine welcome");
        Check(Hex(client.SendKey) == (string)v["client_to_server_key"] && Hex(client.RecvKey) == (string)v["server_to_client_key"], "derived keys");
        var wrong = new KcpCrypto.ClientHandshake(conv, KcpCrypto.X25519Public(KcpCrypto.RandomBytes(32)), Hex((string)v["client_ephemeral_private"]));
        Check(wrong.Finish(welcome, welcome.Length) == null, "a client pinned to another key accepted the welcome");
        var damaged = (byte[])welcome.Clone();
        damaged[20] ^= 1;
        Check(h.Finish(damaged, damaged.Length) == null, "accepted a damaged welcome");

        var keys = Hex((string)v["client_to_server_key"] + (string)v["server_to_client_key"]);
        var server = new KcpCipher(conv, keys, false);
        foreach (JObject d in (JArray)v["datagrams"])
        {
            var fromClient = (string)d["from"] == "client";
            var plain = Hex((string)d["plaintext"]);
            var sealed_ = (fromClient ? client : server).Seal(plain, plain.Length);
            Check(Hex(sealed_) == (string)d["sealed"], "sealed " + d);
            var opened = (fromClient ? server : client).Open(sealed_, sealed_.Length);
            Check(opened != null && Hex(opened) == (string)d["plaintext"], "opened " + d);
        }
        // Counter 0 is the welcome's and never opens as a datagram.
        var zero = client.Seal(new byte[] { 1 }, 1);
        for (var i = 5; i < 13; i++)
            zero[i] = 0;
        Check(server.Open(zero, zero.Length) == null, "opened a datagram with counter 0");
    }

    /// <summary>Two KCP cores over a link that drops, duplicates and reorders, on a virtual clock.</summary>
    private static void CoreUnderLoss()
    {
        var r = new Random(7);
        var inFlight = new List<(uint at, bool toB, byte[] data)>();
        uint now = 0;
        KcpCore a = null, b = null;
        Action<byte[], int> link(bool toB) => (data, n) =>
        {
            for (var copy = 0; copy < (r.NextDouble() < 0.05 ? 2 : 1); copy++)
                if (r.NextDouble() >= 0.2)
                    inFlight.Add((now + (uint)r.Next(5, 60), toB, data.Take(n).ToArray()));
        };
        a = new KcpCore(9, link(true));
        b = new KcpCore(9, link(false));
        foreach (var k in new[] { a, b })
        {
            k.NoDelay(1, 10, 2, true);
            k.WndSize(256, 256);
            k.SetMtu(1200);
        }
        var sent = new List<byte[]>();
        for (var i = 0; i < 300; i++)
        {
            var m = new byte[r.Next(1, 20000)];
            r.NextBytes(m);
            sent.Add(m);
            Check(a.Send(m, 0, m.Length), "send " + i);
        }
        var got = new List<byte[]>();
        for (; now < 600000 && got.Count < sent.Count; now += 5)
        {
            a.Update(now);
            b.Update(now);
            var due = inFlight.Where(p => p.at <= now).ToList();
            inFlight.RemoveAll(p => p.at <= now);
            foreach (var p in due.OrderBy(_ => r.Next()))
                (p.toB ? b : a).Input(p.data, 0, p.data.Length);
            for (var m = b.Recv(); m != null; m = b.Recv())
                got.Add(m);
        }
        Check(got.Count == sent.Count, "delivered " + got.Count + " of " + sent.Count);
        for (var i = 0; i < sent.Count; i++)
            Check(got[i].SequenceEqual(sent[i]), "message " + i + " differs");
        Check(!a.Dead && !b.Dead, "dead link under 20% loss");
        Console.WriteLine("kcp core: " + sent.Count + " messages in order after " + now + " virtual ms");
    }
}
#endif
