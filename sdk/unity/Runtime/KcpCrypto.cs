using System;
using System.Security.Cryptography;
using System.Text;

namespace TabForge.Protocol
{
    /// <summary>
    /// Encrypted KCP, version 1 (see doc/realtime.md and
    /// protocol/realtime/kcpcrypto.go): X25519 handshake, HKDF-SHA256 keys and
    /// ChaCha20-Poly1305 datagrams. X25519 and ChaCha20-Poly1305 are written
    /// out here because netstandard2.1 has neither; both are constant-time
    /// (no secret-dependent branches or table lookups).
    /// </summary>
    internal static class KcpCrypto
    {
        public const byte Hello = 0xFD, Welcome = 0xFC, Sealed = 0xFB, PlainRefuse = 0xFA, Reset = 0xFF, Keepalive = 0xFE;
        public const byte Version = 1;
        public const int KeySize = 32, HelloSize = 64, KeyIdSize = 8, WelcomeSize = 5 + KeySize + 16, SealedHeader = 5 + 8, SealOverhead = SealedHeader + 16;

#pragma warning disable SYSLIB0023 // RNGCryptoServiceProvider: obsolete on .NET 6+, but a direct reference survives IL2CPP stripping, unlike RandomNumberGenerator.Create()
        private static readonly RandomNumberGenerator Random = new RNGCryptoServiceProvider();
#pragma warning restore SYSLIB0023

        public static byte[] RandomBytes(int n)
        {
            var b = new byte[n];
            lock (Random)
                Random.GetBytes(b);
            return b;
        }

        // ---- X25519 (RFC 7748), after TweetNaCl: 16 limbs of 16 bits ----

        private static void Car(long[] o)
        {
            for (var i = 0; i < 16; i++)
            {
                o[i] += 1L << 16;
                var c = o[i] >> 16;
                if (i < 15)
                    o[i + 1] += c - 1;
                else
                    o[0] += 38 * (c - 1);
                o[i] -= c << 16;
            }
        }

        private static void Sel(long[] p, long[] q, long b)
        {
            var c = ~(b - 1);
            for (var i = 0; i < 16; i++)
            {
                var t = c & (p[i] ^ q[i]);
                p[i] ^= t;
                q[i] ^= t;
            }
        }

        private static void Pack(byte[] o, long[] n)
        {
            long[] m = new long[16], t = (long[])n.Clone();
            Car(t);
            Car(t);
            Car(t);
            for (var j = 0; j < 2; j++)
            {
                m[0] = t[0] - 0xffed;
                for (var i = 1; i < 15; i++)
                {
                    m[i] = t[i] - 0xffff - ((m[i - 1] >> 16) & 1);
                    m[i - 1] &= 0xffff;
                }
                m[15] = t[15] - 0x7fff - ((m[14] >> 16) & 1);
                var b = (m[15] >> 16) & 1;
                m[14] &= 0xffff;
                Sel(t, m, 1 - b);
            }
            for (var i = 0; i < 16; i++)
            {
                o[2 * i] = (byte)t[i];
                o[2 * i + 1] = (byte)(t[i] >> 8);
            }
        }

        private static void Unpack(long[] o, byte[] n)
        {
            for (var i = 0; i < 16; i++)
                o[i] = n[2 * i] + ((long)n[2 * i + 1] << 8);
            o[15] &= 0x7fff;
        }

        private static void Add(long[] o, long[] a, long[] b) { for (var i = 0; i < 16; i++) o[i] = a[i] + b[i]; }

        private static void Sub(long[] o, long[] a, long[] b) { for (var i = 0; i < 16; i++) o[i] = a[i] - b[i]; }

        private static void Mul(long[] o, long[] a, long[] b)
        {
            var t = new long[31];
            for (var i = 0; i < 16; i++)
                for (var j = 0; j < 16; j++)
                    t[i + j] += a[i] * b[j];
            for (var i = 0; i < 15; i++)
                t[i] += 38 * t[i + 16];
            Array.Copy(t, o, 16);
            Car(o);
            Car(o);
        }

        private static void Inv(long[] o, long[] x)
        {
            var c = (long[])x.Clone();
            for (var a = 253; a >= 0; a--)
            {
                Mul(c, c, c);
                if (a != 2 && a != 4)
                    Mul(c, c, x);
            }
            Array.Copy(c, o, 16);
        }

        private static readonly long[] A24 = { 0xDB41, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0 };

        /// <summary>X25519(scalar, point). Throws for a low-order point (all-zero result), as Go's crypto/ecdh does.</summary>
        public static byte[] X25519(byte[] scalar, byte[] point)
        {
            if (scalar.Length != KeySize || point.Length != KeySize)
                throw new ArgumentException("X25519 keys are 32 bytes");
            var z = (byte[])scalar.Clone();
            z[31] = (byte)((z[31] & 127) | 64);
            z[0] &= 248;
            long[] x = new long[16], a = new long[16], b = new long[16], c = new long[16], d = new long[16], e = new long[16], f = new long[16];
            Unpack(x, point);
            Array.Copy(x, b, 16);
            a[0] = d[0] = 1;
            for (var i = 254; i >= 0; i--)
            {
                long r = (z[i >> 3] >> (i & 7)) & 1;
                Sel(a, b, r);
                Sel(c, d, r);
                Add(e, a, c);
                Sub(a, a, c);
                Add(c, b, d);
                Sub(b, b, d);
                Mul(d, e, e);
                Mul(f, a, a);
                Mul(a, c, a);
                Mul(c, b, e);
                Add(e, a, c);
                Sub(a, a, c);
                Mul(b, a, a);
                Sub(c, d, f);
                Mul(a, c, A24);
                Add(a, a, d);
                Mul(c, c, a); // z2 = E * (AA + a24 * E)
                Mul(a, d, f);
                Mul(d, b, x);
                Mul(b, e, e);
                Sel(a, b, r);
                Sel(c, d, r);
            }
            Inv(c, c);
            Mul(a, a, c);
            var q = new byte[KeySize];
            Pack(q, a);
            var zero = 0;
            foreach (var v in q)
                zero |= v;
            if (zero == 0)
                throw new CryptographicException("X25519 produced the all-zero value (low-order point)");
            return q;
        }

        private static readonly byte[] BasePoint = Basepoint();

        private static byte[] Basepoint()
        {
            var b = new byte[KeySize];
            b[0] = 9;
            return b;
        }

        public static byte[] X25519Public(byte[] privateKey) => X25519(privateKey, BasePoint);

        // ---- ChaCha20 (RFC 8439) ----

        private static uint Rotl(uint v, int n) => (v << n) | (v >> (32 - n));

        private static uint Le32(byte[] b, int i) => (uint)(b[i] | b[i + 1] << 8 | b[i + 2] << 16 | b[i + 3] << 24);

        private static void PutLe32(byte[] b, int i, uint v)
        {
            b[i] = (byte)v;
            b[i + 1] = (byte)(v >> 8);
            b[i + 2] = (byte)(v >> 16);
            b[i + 3] = (byte)(v >> 24);
        }

        private static void Quarter(uint[] x, int a, int b, int c, int d)
        {
            x[a] += x[b]; x[d] = Rotl(x[d] ^ x[a], 16);
            x[c] += x[d]; x[b] = Rotl(x[b] ^ x[c], 12);
            x[a] += x[b]; x[d] = Rotl(x[d] ^ x[a], 8);
            x[c] += x[d]; x[b] = Rotl(x[b] ^ x[c], 7);
        }

        /// <summary>One 64-byte ChaCha20 block.</summary>
        public static void ChaChaBlock(byte[] key, uint counter, byte[] nonce, byte[] output)
        {
            var s = new uint[16];
            s[0] = 0x61707865; s[1] = 0x3320646e; s[2] = 0x79622d32; s[3] = 0x6b206574;
            for (var i = 0; i < 8; i++)
                s[4 + i] = Le32(key, 4 * i);
            s[12] = counter;
            s[13] = Le32(nonce, 0); s[14] = Le32(nonce, 4); s[15] = Le32(nonce, 8);
            var x = (uint[])s.Clone();
            for (var i = 0; i < 10; i++)
            {
                Quarter(x, 0, 4, 8, 12); Quarter(x, 1, 5, 9, 13); Quarter(x, 2, 6, 10, 14); Quarter(x, 3, 7, 11, 15);
                Quarter(x, 0, 5, 10, 15); Quarter(x, 1, 6, 11, 12); Quarter(x, 2, 7, 8, 13); Quarter(x, 3, 4, 9, 14);
            }
            for (var i = 0; i < 16; i++)
                PutLe32(output, 4 * i, x[i] + s[i]);
        }

        private static void ChaChaXor(byte[] key, byte[] nonce, uint counter, byte[] data, int offset, int length, byte[] dest, int destOffset)
        {
            var block = new byte[64];
            for (var done = 0; done < length; done += 64, counter++)
            {
                ChaChaBlock(key, counter, nonce, block);
                var n = Math.Min(64, length - done);
                for (var i = 0; i < n; i++)
                    dest[destOffset + done + i] = (byte)(data[offset + done + i] ^ block[i]);
            }
        }

        // ---- Poly1305 (RFC 8439), after poly1305-donna 32-bit ----

        public static byte[] Poly1305(byte[] key, byte[] m, int offset, int length)
        {
            uint r0 = Le32(key, 0) & 0x3ffffff, r1 = (Le32(key, 3) >> 2) & 0x3ffff03, r2 = (Le32(key, 6) >> 4) & 0x3ffc0ff, r3 = (Le32(key, 9) >> 6) & 0x3f03fff, r4 = (Le32(key, 12) >> 8) & 0x00fffff;
            uint s1 = r1 * 5, s2 = r2 * 5, s3 = r3 * 5, s4 = r4 * 5;
            uint h0 = 0, h1 = 0, h2 = 0, h3 = 0, h4 = 0;
            var block = new byte[16];
            for (var at = 0; at < length; at += 16)
            {
                var n = Math.Min(16, length - at);
                uint hibit = 1 << 24;
                Buffer.BlockCopy(m, offset + at, block, 0, n);
                if (n < 16)
                {
                    block[n] = 1;
                    for (var i = n + 1; i < 16; i++)
                        block[i] = 0;
                    hibit = 0;
                }
                h0 += Le32(block, 0) & 0x3ffffff;
                h1 += (Le32(block, 3) >> 2) & 0x3ffffff;
                h2 += (Le32(block, 6) >> 4) & 0x3ffffff;
                h3 += (Le32(block, 9) >> 6) & 0x3ffffff;
                h4 += (Le32(block, 12) >> 8) | hibit;
                ulong d0 = (ulong)h0 * r0 + (ulong)h1 * s4 + (ulong)h2 * s3 + (ulong)h3 * s2 + (ulong)h4 * s1;
                ulong d1 = (ulong)h0 * r1 + (ulong)h1 * r0 + (ulong)h2 * s4 + (ulong)h3 * s3 + (ulong)h4 * s2;
                ulong d2 = (ulong)h0 * r2 + (ulong)h1 * r1 + (ulong)h2 * r0 + (ulong)h3 * s4 + (ulong)h4 * s3;
                ulong d3 = (ulong)h0 * r3 + (ulong)h1 * r2 + (ulong)h2 * r1 + (ulong)h3 * r0 + (ulong)h4 * s4;
                ulong d4 = (ulong)h0 * r4 + (ulong)h1 * r3 + (ulong)h2 * r2 + (ulong)h3 * r1 + (ulong)h4 * r0;
                uint c = (uint)(d0 >> 26); h0 = (uint)d0 & 0x3ffffff;
                d1 += c; c = (uint)(d1 >> 26); h1 = (uint)d1 & 0x3ffffff;
                d2 += c; c = (uint)(d2 >> 26); h2 = (uint)d2 & 0x3ffffff;
                d3 += c; c = (uint)(d3 >> 26); h3 = (uint)d3 & 0x3ffffff;
                d4 += c; c = (uint)(d4 >> 26); h4 = (uint)d4 & 0x3ffffff;
                h0 += c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
                h1 += c;
            }
            {
                uint c = h1 >> 26; h1 &= 0x3ffffff;
                h2 += c; c = h2 >> 26; h2 &= 0x3ffffff;
                h3 += c; c = h3 >> 26; h3 &= 0x3ffffff;
                h4 += c; c = h4 >> 26; h4 &= 0x3ffffff;
                h0 += c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
                h1 += c;
                uint g0 = h0 + 5; c = g0 >> 26; g0 &= 0x3ffffff;
                uint g1 = h1 + c; c = g1 >> 26; g1 &= 0x3ffffff;
                uint g2 = h2 + c; c = g2 >> 26; g2 &= 0x3ffffff;
                uint g3 = h3 + c; c = g3 >> 26; g3 &= 0x3ffffff;
                uint g4 = h4 + c - (1u << 26);
                uint mask = (g4 >> 31) - 1;
                g0 &= mask; g1 &= mask; g2 &= mask; g3 &= mask; g4 &= mask;
                mask = ~mask;
                h0 = (h0 & mask) | g0; h1 = (h1 & mask) | g1; h2 = (h2 & mask) | g2; h3 = (h3 & mask) | g3; h4 = (h4 & mask) | g4;
                h0 = h0 | (h1 << 26);
                h1 = (h1 >> 6) | (h2 << 20);
                h2 = (h2 >> 12) | (h3 << 14);
                h3 = (h3 >> 18) | (h4 << 8);
                ulong f = (ulong)h0 + Le32(key, 16); h0 = (uint)f;
                f = (ulong)h1 + Le32(key, 20) + (f >> 32); h1 = (uint)f;
                f = (ulong)h2 + Le32(key, 24) + (f >> 32); h2 = (uint)f;
                f = (ulong)h3 + Le32(key, 28) + (f >> 32); h3 = (uint)f;
            }
            var tag = new byte[16];
            PutLe32(tag, 0, h0); PutLe32(tag, 4, h1); PutLe32(tag, 8, h2); PutLe32(tag, 12, h3);
            return tag;
        }

        // ---- AEAD_CHACHA20_POLY1305 (RFC 8439 2.8) ----

        private static byte[] AeadTag(byte[] key, byte[] nonce, byte[] aad, int aadOffset, int aadLength, byte[] ct, int ctOffset, int ctLength)
        {
            var block = new byte[64];
            ChaChaBlock(key, 0, nonce, block);
            var polyKey = new byte[32];
            Buffer.BlockCopy(block, 0, polyKey, 0, 32);
            int pa = (aadLength + 15) & ~15, pc = (ctLength + 15) & ~15;
            var mac = new byte[pa + pc + 16];
            Buffer.BlockCopy(aad, aadOffset, mac, 0, aadLength);
            Buffer.BlockCopy(ct, ctOffset, mac, pa, ctLength);
            PutLe32(mac, pa + pc, (uint)aadLength);
            PutLe32(mac, pa + pc + 8, (uint)ctLength);
            return Poly1305(polyKey, mac, 0, mac.Length);
        }

        /// <summary>Writes ciphertext and the 16-byte tag of plain[offset..] at dest[destOffset..].</summary>
        public static void Seal(byte[] key, byte[] nonce, byte[] aad, int aadOffset, int aadLength, byte[] plain, int offset, int length, byte[] dest, int destOffset)
        {
            ChaChaXor(key, nonce, 1, plain, offset, length, dest, destOffset);
            var tag = AeadTag(key, nonce, aad, aadOffset, aadLength, dest, destOffset, length);
            Buffer.BlockCopy(tag, 0, dest, destOffset + length, 16);
        }

        /// <summary>Returns the plaintext of sealed[offset..offset+length] (ciphertext and tag), or null if it does not authenticate.</summary>
        public static byte[] Open(byte[] key, byte[] nonce, byte[] aad, int aadOffset, int aadLength, byte[] sealed_, int offset, int length)
        {
            if (length < 16)
                return null;
            var n = length - 16;
            var tag = AeadTag(key, nonce, aad, aadOffset, aadLength, sealed_, offset, n);
            var diff = 0;
            for (var i = 0; i < 16; i++)
                diff |= tag[i] ^ sealed_[offset + n + i];
            if (diff != 0)
                return null;
            var plain = new byte[n];
            ChaChaXor(key, nonce, 1, sealed_, offset, n, plain, 0);
            return plain;
        }

        // ---- the KCP handshake ----

        public static byte[] Nonce(ulong counter)
        {
            var nonce = new byte[12];
            PutLe32(nonce, 4, (uint)counter);
            PutLe32(nonce, 8, (uint)(counter >> 32));
            return nonce;
        }

        // ---- SHA-256 (FIPS 180-4) and HMAC-SHA256 ----
        // Written out instead of SHA256.Create()/HMACSHA256: on Unity's Mono
        // those find their implementation by name through CryptoConfig, which
        // IL2CPP code stripping can break at runtime.

        private static readonly uint[] ShaK =
        {
            0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
            0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
            0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
            0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
        };

        private static uint Rotr(uint v, int n) => (v >> n) | (v << (32 - n));

        public static byte[] Sha256(byte[] data)
        {
            uint[] h = { 0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19 };
            var padded = new byte[(data.Length + 9 + 63) / 64 * 64];
            Buffer.BlockCopy(data, 0, padded, 0, data.Length);
            padded[data.Length] = 0x80;
            var bits = (ulong)data.Length * 8;
            for (var i = 0; i < 8; i++)
                padded[padded.Length - 1 - i] = (byte)(bits >> (8 * i));
            var w = new uint[64];
            for (var block = 0; block < padded.Length; block += 64)
            {
                for (var i = 0; i < 16; i++)
                    w[i] = (uint)(padded[block + 4 * i] << 24 | padded[block + 4 * i + 1] << 16 | padded[block + 4 * i + 2] << 8 | padded[block + 4 * i + 3]);
                for (var i = 16; i < 64; i++)
                    w[i] = w[i - 16] + (Rotr(w[i - 15], 7) ^ Rotr(w[i - 15], 18) ^ (w[i - 15] >> 3)) + w[i - 7] + (Rotr(w[i - 2], 17) ^ Rotr(w[i - 2], 19) ^ (w[i - 2] >> 10));
                uint a = h[0], b = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
                for (var i = 0; i < 64; i++)
                {
                    var t1 = hh + (Rotr(e, 6) ^ Rotr(e, 11) ^ Rotr(e, 25)) + ((e & f) ^ (~e & g)) + ShaK[i] + w[i];
                    var t2 = (Rotr(a, 2) ^ Rotr(a, 13) ^ Rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c));
                    hh = g; g = f; f = e; e = d + t1; d = c; c = b; b = a; a = t1 + t2;
                }
                h[0] += a; h[1] += b; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
            }
            var o = new byte[32];
            for (var i = 0; i < 8; i++)
            {
                o[4 * i] = (byte)(h[i] >> 24);
                o[4 * i + 1] = (byte)(h[i] >> 16);
                o[4 * i + 2] = (byte)(h[i] >> 8);
                o[4 * i + 3] = (byte)h[i];
            }
            return o;
        }

        private static byte[] Hmac(byte[] key, byte[] data)
        {
            var k = new byte[64];
            var source = key.Length > 64 ? Sha256(key) : key;
            Buffer.BlockCopy(source, 0, k, 0, source.Length);
            var inner = new byte[64];
            var outer = new byte[64];
            for (var i = 0; i < 64; i++)
            {
                inner[i] = (byte)(k[i] ^ 0x36);
                outer[i] = (byte)(k[i] ^ 0x5c);
            }
            return Sha256(Concat(outer, Sha256(Concat(inner, data))));
        }

        private static byte[] Concat(params byte[][] parts)
        {
            var n = 0;
            foreach (var p in parts)
                n += p.Length;
            var o = new byte[n];
            n = 0;
            foreach (var p in parts)
            {
                Buffer.BlockCopy(p, 0, o, n, p.Length);
                n += p.Length;
            }
            return o;
        }

        /// <summary>Client-to-server key followed by server-to-client key (kcpKeyMaterial in Go).</summary>
        public static byte[] KeyMaterial(uint conv, byte[] clientEph, byte[] serverEph, byte[] serverStatic, byte[] ee, byte[] es)
        {
            var convBytes = new byte[4];
            PutLe32(convBytes, 0, conv);
            var salt = Sha256(Concat(Encoding.ASCII.GetBytes("tabforge-kcp-v1"), convBytes, clientEph, serverEph, serverStatic));
            var prk = Hmac(salt, Concat(ee, es));
            var info = Encoding.ASCII.GetBytes("keys");
            var t1 = Hmac(prk, Concat(info, new byte[] { 1 }));
            var t2 = Hmac(prk, Concat(t1, info, new byte[] { 2 }));
            return Concat(t1, t2);
        }

        /// <summary>conv | 0xFD | 1 | client ephemeral key | key id of the pinned server key | zero padding.</summary>
        public static byte[] HelloDatagram(uint conv, byte[] clientEphPublic, byte[] serverPublic)
        {
            var b = new byte[HelloSize];
            PutLe32(b, 0, conv);
            b[4] = Hello;
            b[5] = Version;
            Buffer.BlockCopy(clientEphPublic, 0, b, 6, KeySize);
            Buffer.BlockCopy(KeyId(serverPublic), 0, b, 6 + KeySize, KeyIdSize);
            return b;
        }

        /// <summary>First 8 bytes of SHA-256(server public key): lets a rotating server pick the key.</summary>
        public static byte[] KeyId(byte[] serverPublic)
        {
            var id = new byte[KeyIdSize];
            Buffer.BlockCopy(Sha256(serverPublic), 0, id, 0, KeyIdSize);
            return id;
        }

        /// <summary>The client's half of the handshake, kept until a welcome arrives.</summary>
        public sealed class ClientHandshake
        {
            public readonly uint Conv;
            public readonly byte[] Hello;
            private readonly byte[] ephPrivate, ephPublic, server;

            public ClientHandshake(uint conv, byte[] serverPublic, byte[] ephemeralPrivate = null)
            {
                if (serverPublic == null || serverPublic.Length != KeySize)
                    throw new ArgumentException("The KCP server key is a 32-byte X25519 public key");
                Conv = conv;
                server = serverPublic;
                ephPrivate = ephemeralPrivate ?? RandomBytes(KeySize);
                ephPublic = X25519Public(ephPrivate);
                Hello = HelloDatagram(conv, ephPublic, server);
            }

            /// <summary>The ciphers if data is this conversation's genuine welcome, else null.</summary>
            public KcpCipher Finish(byte[] data, int length)
            {
                if (length != WelcomeSize || data[4] != Welcome || Le32(data, 0) != Conv)
                    return null;
                var serverEph = new byte[KeySize];
                Buffer.BlockCopy(data, 5, serverEph, 0, KeySize);
                byte[] keys;
                try
                {
                    var ee = X25519(ephPrivate, serverEph);
                    var es = X25519(ephPrivate, server);
                    keys = KeyMaterial(Conv, ephPublic, serverEph, server, ee, es);
                }
                catch (CryptographicException)
                {
                    return null;
                }
                var cipher = new KcpCipher(Conv, keys, true);
                return Open(cipher.RecvKey, Nonce(0), data, 0, 5 + KeySize, data, 5 + KeySize, 16) == null ? null : cipher;
            }
        }
    }

    /// <summary>Seals and opens the datagrams of one encrypted conversation.</summary>
    internal sealed class KcpCipher
    {
        public readonly byte[] SendKey, RecvKey;
        private readonly uint conv;
        private ulong sent; // last counter used; 0 is the welcome's
        private readonly object gate = new object();

        public KcpCipher(uint conv, byte[] keys, bool client)
        {
            this.conv = conv;
            var c2s = new byte[32];
            var s2c = new byte[32];
            Buffer.BlockCopy(keys, 0, c2s, 0, 32);
            Buffer.BlockCopy(keys, 32, s2c, 0, 32);
            SendKey = client ? c2s : s2c;
            RecvKey = client ? s2c : c2s;
        }

        /// <summary>conv | 0xFB | counter | ChaCha20-Poly1305(plain), the header as associated data.</summary>
        public byte[] Seal(byte[] plain, int length)
        {
            ulong counter;
            lock (gate)
                counter = ++sent;
            var o = new byte[KcpCrypto.SealOverhead + length];
            o[0] = (byte)conv; o[1] = (byte)(conv >> 8); o[2] = (byte)(conv >> 16); o[3] = (byte)(conv >> 24);
            o[4] = KcpCrypto.Sealed;
            for (var i = 0; i < 8; i++)
                o[5 + i] = (byte)(counter >> (8 * i));
            KcpCrypto.Seal(SendKey, KcpCrypto.Nonce(counter), o, 0, KcpCrypto.SealedHeader, plain ?? Array.Empty<byte>(), 0, length, o, KcpCrypto.SealedHeader);
            return o;
        }

        /// <summary>The plaintext of a sealed datagram, or null.</summary>
        public byte[] Open(byte[] data, int length)
        {
            if (length < KcpCrypto.SealOverhead || data[4] != KcpCrypto.Sealed)
                return null;
            ulong counter = 0;
            for (var i = 0; i < 8; i++)
                counter |= (ulong)data[5 + i] << (8 * i);
            if (counter == 0)
                return null; // reserved for the welcome
            return KcpCrypto.Open(RecvKey, KcpCrypto.Nonce(counter), data, 0, KcpCrypto.SealedHeader, data, KcpCrypto.SealedHeader, length - KcpCrypto.SealedHeader);
        }
    }
}
