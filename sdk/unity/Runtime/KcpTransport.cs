using System;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;

namespace TabForge.Protocol
{
    /// <summary>
    /// Realtime socket over KCP on UDP (kcp://host:port/path), for the Go
    /// server's ServeKCP: the same frames as WebSocket without TCP's
    /// head-of-line blocking on lossy mobile networks. With a server key it
    /// runs encrypted KCP, otherwise clear text. Unity standalone, Android and
    /// iOS; WebGL players have no UDP.
    /// </summary>
    public sealed class KcpTransport : IRealtimeSocket
    {
        private const byte TypeOpen = 0, TypeBinary = 1, TypeText = 2, TypeClose = 3, More = 0x80;
        private const int Chunk = 16 << 10, Mtu = 1200, Window = 256, IntervalMS = 10, DeadLink = 40;
        private const int QuietMS = 1000, HelloRetryMS = 250, HandshakeResets = 3, HandshakeMS = 10000, CloseLingerMS = 1000;
        public const int MaxMessageBytes = 1 << 20;

        private readonly IRealtimeSocketEvents events;
        private readonly string path, subprotocol;
        private readonly byte[] serverKey;
        private readonly object gate = new object();
        private readonly Stopwatch clock = Stopwatch.StartNew();
        private Socket udp;
        private uint conv;
        private KcpCore kcp;
        private KcpCipher crypt;
        private bool opened, closing, closed;
        private long lastRecv, lastAlive, closeDeadline;
        private byte[] partial = Array.Empty<byte>();
        private int partialLength = -1, partialType;

        private KcpTransport(IRealtimeSocketEvents events, string path, string subprotocol, byte[] serverKey)
        {
            this.events = events;
            this.path = path;
            this.subprotocol = subprotocol;
            this.serverKey = serverKey;
        }

        /// <summary>
        /// Starts connecting to kcp://host:port/path. serverKey is the server's
        /// 32-byte X25519 public key (Server.KCPKey) for encrypted KCP, or null.
        /// </summary>
        public static IRealtimeSocket Connect(string url, string subprotocol, byte[] serverKey, IRealtimeSocketEvents events)
        {
            var uri = new Uri(url);
            if (uri.Scheme != "kcp" || uri.Port <= 0)
                throw new ArgumentException("KCP URLs look like kcp://host:port/path");
            if (serverKey != null && serverKey.Length != KcpCrypto.KeySize)
                throw new ArgumentException("The KCP server key is a 32-byte X25519 public key");
            var path = string.IsNullOrEmpty(uri.AbsolutePath) ? "/" : uri.AbsolutePath;
            var t = new KcpTransport(events, path, subprotocol, serverKey);
            var host = uri.Host;
            var port = uri.Port;
            new Thread(() => t.Run(host, port)) { IsBackground = true, Name = "TabForge KCP" }.Start();
            return t;
        }

        private long Now => clock.ElapsedMilliseconds;

        private void Run(string host, int port)
        {
            string refused = null;
            try
            {
                var addresses = Dns.GetHostAddresses(host);
                if (addresses.Length == 0)
                    throw new IOException("no address for " + host);
                var address = Array.Find(addresses, a => a.AddressFamily == AddressFamily.InterNetwork) ?? addresses[0];
                var socket = new Socket(address.AddressFamily, SocketType.Dgram, ProtocolType.Udp);
                socket.Connect(new IPEndPoint(address, port));
                lock (gate)
                {
                    if (closed)
                    {
                        socket.Close();
                        return;
                    }
                    udp = socket;
                    conv = 0;
                    while (conv == 0)
                        conv = BitConverter.ToUInt32(KcpCrypto.RandomBytes(4), 0);
                }
                if (serverKey != null)
                {
                    refused = Handshake();
                    if (refused != null || crypt == null)
                        return;
                }
                lock (gate)
                {
                    if (closed)
                        return;
                    kcp = new KcpCore(conv, Output);
                    kcp.NoDelay(1, IntervalMS, 2, true);
                    kcp.WndSize(Window, Window);
                    kcp.SetMtu(crypt != null ? Mtu - KcpCrypto.SealOverhead : Mtu);
                    kcp.SetDeadLink(DeadLink);
                    kcp.Update(0);
                    lastRecv = lastAlive = Now;
                    QueueLocked(TypeOpen, Encoding.UTF8.GetBytes(path + "\n" + subprotocol));
                }
                var buffer = new byte[64 << 10];
                while (true)
                {
                    if (udp.Poll(IntervalMS * 1000, SelectMode.SelectRead))
                    {
                        // Readable can also mean an error (an ICMP "port
                        // unreachable" on Windows) with nothing available:
                        // Receive then throws instead of the loop spinning.
                        do
                        {
                            var n = udp.Receive(buffer);
                            refused = Receive(buffer, n);
                            if (refused != null)
                                return;
                        } while (udp.Available > 0);
                    }
                    bool keepalive, done;
                    lock (gate)
                    {
                        if (closed)
                            return;
                        kcp.Update((uint)Now);
                        if (kcp.Dead)
                            return;
                        var now = Now;
                        keepalive = now - lastRecv > QuietMS && now - lastAlive > QuietMS;
                        if (keepalive)
                            lastAlive = now;
                        done = closing && (kcp.WaitSnd == 0 || now >= closeDeadline);
                    }
                    if (done)
                        return;
                    if (keepalive)
                        SendRaw(crypt != null ? crypt.Seal(null, 0) : Control(KcpCrypto.Keepalive));
                }
            }
            catch (Exception)
            {
                // Network failures surface as Closed and are retried.
            }
            finally
            {
                if (refused != null)
                    (events as IRealtimeSocketRefusal)?.Refused(refused);
                Shutdown();
            }
        }

        private byte[] Control(byte type)
        {
            var b = new byte[5];
            b[0] = (byte)conv; b[1] = (byte)(conv >> 8); b[2] = (byte)(conv >> 16); b[3] = (byte)(conv >> 24);
            b[4] = type;
            return b;
        }

        private void SendRaw(byte[] data)
        {
            try { udp.Send(data); } catch (Exception) { /* lost like any datagram */ }
        }

        private void Output(byte[] data, int length)
        {
            if (crypt != null)
            {
                SendRaw(crypt.Seal(data, length));
                return;
            }
            var copy = new byte[length];
            Buffer.BlockCopy(data, 0, copy, 0, length);
            SendRaw(copy);
        }

        /// <summary>The encrypted handshake; returns a permanent refusal, or sets crypt.</summary>
        private string Handshake()
        {
            var h = new KcpCrypto.ClientHandshake(conv, serverKey);
            var buffer = new byte[2048];
            var resets = 0;
            var deadline = Now + HandshakeMS;
            while (Now < deadline)
            {
                lock (gate)
                    if (closed)
                        return null;
                SendRaw(h.Hello);
                var retry = Now + HelloRetryMS;
                while (Now < retry)
                {
                    if (!udp.Poll((int)Math.Max(1, retry - Now) * 1000, SelectMode.SelectRead))
                        continue;
                    var n = udp.Receive(buffer);
                    if (n < 5 || BitConverter.ToUInt32(buffer, 0) != conv)
                        continue;
                    if (n == 5 && buffer[4] == KcpCrypto.Reset)
                    {
                        // A plain server resets every hello; one or two may be damage or forgery.
                        if (++resets >= HandshakeResets)
                            return "The server does not offer encrypted KCP; clear RealtimeOptions.KcpServerKey or set Server.KCPKey";
                        break;
                    }
                    var cipher = h.Finish(buffer, n);
                    if (cipher != null)
                    {
                        crypt = cipher;
                        return null;
                    }
                }
            }
            return null; // Timed out: retried like a network failure.
        }

        /// <summary>Handles one datagram; returns a permanent refusal or null.</summary>
        private string Receive(byte[] data, int n)
        {
            if (n < 5 || BitConverter.ToUInt32(data, 0) != conv)
                return null;
            if (n == 5 && data[4] == KcpCrypto.Reset)
                throw new IOException("kcp conversation reset by peer");
            byte[] plain;
            int length;
            if (crypt == null)
            {
                if (n == 5 && data[4] == KcpCrypto.Keepalive)
                    return null;
                if (n == 5 && data[4] == KcpCrypto.PlainRefuse)
                    return "The server only accepts encrypted KCP; set RealtimeOptions.KcpServerKey";
                plain = data;
                length = n;
            }
            else
            {
                plain = crypt.Open(data, n);
                if (plain == null)
                    return null; // Damaged or forged: dropped like a lost datagram.
                length = plain.Length;
                if (length == 0)
                {
                    lock (gate)
                        lastRecv = Now;
                    return null; // Keepalive.
                }
            }
            lock (gate)
            {
                if (closed)
                    return null;
                lastRecv = Now;
                if (!kcp.Input(plain, 0, length))
                    return null;
                string refused;
                while (true)
                {
                    var m = kcp.Recv();
                    if (m == null)
                        break;
                    refused = Deliver(m);
                    if (refused != null)
                        return refused;
                }
                kcp.Flush(); // Acknowledge at once instead of on the next tick.
            }
            return null;
        }

        /// <summary>Handles one transport message (under gate).</summary>
        private string Deliver(byte[] m)
        {
            if (m.Length == 0)
                throw new IOException("empty kcp message");
            var type = m[0] & ~More;
            var more = (m[0] & More) != 0;
            switch (type)
            {
                case TypeOpen:
                    if (opened || Encoding.UTF8.GetString(m, 1, m.Length - 1) != subprotocol)
                        throw new IOException("unexpected kcp open");
                    opened = true;
                    events.Opened(subprotocol);
                    return null;
                case TypeClose:
                    var reason = Encoding.UTF8.GetString(m, 1, m.Length - 1);
                    if (!opened && reason.StartsWith("offer ", StringComparison.Ordinal))
                        return "Server did not accept " + subprotocol;
                    throw new IOException("kcp closed: " + reason);
                case TypeBinary:
                case TypeText:
                    if (!opened || (partialLength >= 0 && partialType != type))
                        throw new IOException("unexpected kcp data");
                    if (partialLength < 0)
                    {
                        partialLength = 0;
                        partialType = type;
                    }
                    var add = m.Length - 1;
                    if (partialLength + add > MaxMessageBytes)
                        throw new IOException("kcp message too large");
                    if (partial.Length < partialLength + add)
                        Array.Resize(ref partial, Math.Max(partialLength + add, partial.Length * 2));
                    Buffer.BlockCopy(m, 1, partial, partialLength, add);
                    partialLength += add;
                    if (!more)
                    {
                        var message = new byte[partialLength];
                        Buffer.BlockCopy(partial, 0, message, 0, partialLength);
                        partialLength = -1;
                        events.Received(message, type == TypeText);
                    }
                    return null;
                default:
                    throw new IOException("unknown kcp message type " + type);
            }
        }

        /// <summary>Splits one transport message into chunks and flushes (under gate).</summary>
        private void QueueLocked(byte type, byte[] p)
        {
            var at = 0;
            do
            {
                var n = Math.Min(p.Length - at, Chunk);
                var chunk = new byte[1 + n];
                chunk[0] = (byte)(type | (at + n < p.Length ? More : 0));
                Buffer.BlockCopy(p, at, chunk, 1, n);
                kcp.Send(chunk, 0, chunk.Length);
                at += n;
            } while (at < p.Length);
            kcp.Flush();
        }

        public void Send(byte[] data, bool text)
        {
            lock (gate)
            {
                if (closed || closing || !opened)
                    throw new IOException("Socket is not open");
                if (data.Length > MaxMessageBytes)
                    throw new IOException("Message exceeds " + MaxMessageBytes + " bytes");
                QueueLocked(text ? TypeText : TypeBinary, data);
            }
        }

        /// <summary>Sends what is already queued and a close message, then closes; at most one second.</summary>
        public void Close()
        {
            lock (gate)
            {
                if (closing || closed)
                    return;
                closing = true;
                closeDeadline = Now + CloseLingerMS;
                if (kcp == null || !opened)
                {
                    closed = true;
                    try { udp?.Close(); } catch (Exception) { /* closed */ }
                    return;
                }
                QueueLocked(TypeClose, Array.Empty<byte>());
            }
        }

        private void Shutdown()
        {
            lock (gate)
            {
                closed = true;
                try { udp?.Close(); } catch (Exception) { /* closed */ }
            }
            events.Closed();
        }
    }
}
