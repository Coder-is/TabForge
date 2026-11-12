using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.Text;
using System.Threading.Tasks;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    /// <summary>A WebSocket-like transport. Send and Close may be called from the client's thread only.</summary>
    public interface IRealtimeSocket
    {
        void Send(byte[] data, bool text);
        void Close();
    }

    /// <summary>Socket callbacks; safe to call from any thread.</summary>
    public interface IRealtimeSocketEvents
    {
        /// <param name="protocol">Negotiated subprotocol, or "" if unknown.</param>
        void Opened(string protocol);
        void Received(byte[] data, bool text);
        /// <summary>Called once when the socket closes or fails.</summary>
        void Closed();
    }

    /// <summary>
    /// Optionally implemented by IRealtimeSocketEvents: a transport reports a
    /// refusal that reconnecting cannot fix (for example a KCP encryption
    /// mismatch). The client stops with protocol_error instead of retrying.
    /// </summary>
    public interface IRealtimeSocketRefusal
    {
        void Refused(string message);
    }

    public delegate IRealtimeSocket RealtimeSocketFactory(string url, string subprotocol, IRealtimeSocketEvents events);

    public enum RealtimeState
    {
        Idle,
        Connecting,
        /// <summary>A new session started: reload game state.</summary>
        Connected,
        /// <summary>The previous session continued; nothing was lost.</summary>
        Resumed,
        Disconnected,
        /// <summary>Permanent: kicked, duplicate login, unauthorized, version mismatch or Close().</summary>
        Closed
    }

    public sealed class RealtimeOptions
    {
        /// <summary>
        /// Full URL including the channel path: wss://host/ws/game, or
        /// kcp://host:7001/ws/game for the server's KCP (UDP) transport.
        /// </summary>
        public string Url;
        public string Channel;
        public RealtimeEncoding Encoding = RealtimeEncoding.Proto;
        /// <summary>Bearer token for a new session; resumes do not send it again.</summary>
        public Func<string> Token;
        /// <summary>Defaults to ClientWebSocket, or the browser WebSocket bridge in WebGL players; KcpTransport for kcp:// URLs.</summary>
        public RealtimeSocketFactory Socket;
        /// <summary>
        /// The server's 32-byte X25519 public key (Server.KCPKey) for encrypted
        /// kcp:// URLs; null talks clear-text KCP. Ignored for WebSocket URLs.
        /// </summary>
        public byte[] KcpServerKey;
        public int MinBackoffMS = 200;
        public int MaxBackoffMS = 10000;
        /// <summary>Frames kept until the server acknowledges them.</summary>
        public int MaxUnacked = 1024;
    }

    /// <summary>A pending request. Completed/Failed run on the thread calling Tick.</summary>
    public sealed class RealtimeRequest
    {
        private readonly TaskCompletionSource<JObject> source = new TaskCompletionSource<JObject>(TaskCreationOptions.RunContinuationsAsynchronously);
        internal string ResponseType;
        internal long Deadline;
        internal bool Done;
        public event Action<JObject> Completed;
        public event Action<ProtocolException> Failed;
        /// <summary>Completes with the response or faults with a ProtocolException.</summary>
        public Task<JObject> Task => source.Task;

        internal Action Resolve(JObject value) => () => { source.TrySetResult(value); Completed?.Invoke(value); };
        internal Action Reject(ProtocolException error) => () => { source.TrySetException(error); Failed?.Invoke(error); };
    }

    /// <summary>
    /// Realtime channel client. Call Tick() regularly (Unity: every Update) from
    /// one thread; all callbacks run inside Tick on that thread. Public methods
    /// are thread-safe. The session survives network drops: unacknowledged
    /// frames are retransmitted after a resume, so requests run at most once
    /// and pushes arrive once, in order.
    /// </summary>
    public sealed class RealtimeClient
    {
        public const string SubprotocolProto = "tabforge.realtime.v1.proto";
        public const string SubprotocolJson = "tabforge.realtime.v1.json";
        private const int MaxVolatile = 256;

        private sealed class Spec
        {
            public string Name, Kind, Type, ResponseType;
            public uint Id;
            public int TimeoutMS;
            public bool Volatile;
        }

        private sealed class OutFrame
        {
            public int Kind;
            public ulong Seq;
            public uint Message;
            public byte[] Payload;
            public JObject JsonPayload;
            public bool Written;
            public RealtimeRequest Call;
        }

        private sealed class Connection : IRealtimeSocketEvents, IRealtimeSocketRefusal
        {
            public RealtimeClient Client;
            public IRealtimeSocket Socket;
            public bool Ready;
            public long IdleDeadline, NextPing;
            public void Opened(string protocol) => Client.inbox.Enqueue(() => Client.OnOpened(this, protocol));
            public void Received(byte[] data, bool text) => Client.inbox.Enqueue(() => Client.OnReceived(this, data, text));
            public void Closed() => Client.inbox.Enqueue(() => Client.Drop(this, null));
            public void Refused(string message) => Client.inbox.Enqueue(() => Client.Drop(this, new ProtocolException("protocol_error", message)));
        }

        public event Action<RealtimeState, ProtocolException> StateChanged;
        /// <summary>Exceptions thrown by push handlers or callbacks.</summary>
        public event Action<Exception> HandlerError;

        private readonly object gate = new object();
        private readonly ConcurrentQueue<Action> inbox = new ConcurrentQueue<Action>();
        private readonly List<Action> callbacks = new List<Action>();
        private readonly RealtimeOptions options;
        private readonly string version, schemaHash;
        private readonly WireValidator validator, normalizer;
        private readonly ProtobufCodec codec;
        private readonly Dictionary<string, Spec> byName = new Dictionary<string, Spec>();
        private readonly Dictionary<uint, Spec> byId = new Dictionary<uint, Spec>();
        private readonly Dictionary<string, List<Action<JObject>>> handlers = new Dictionary<string, List<Action<JObject>>>();
        private readonly Stopwatch clock = Stopwatch.StartNew();
        private readonly Random random = new Random();

        private RealtimeState state = RealtimeState.Idle;
        private string sessionId = "", resumeToken = "";
        private long heartbeatMS;
        private ulong nextSeq, peerAck, received, ackSent;
        private List<OutFrame> outbox = new List<OutFrame>();
        private readonly Dictionary<ulong, RealtimeRequest> pending = new Dictionary<ulong, RealtimeRequest>();
        private Connection conn;
        private int failures;
        private long retryAt = -1, ackAt = -1;
        private ProtocolException closedError;

        /// <param name="runtimeJson">The generated runtime.json of the same contract as the server.</param>
        public RealtimeClient(string runtimeJson, RealtimeOptions options)
        {
            this.options = options ?? throw new ArgumentNullException(nameof(options));
            if (options.Socket == null && IsKcp(options.Url))
            {
#if UNITY_WEBGL && !UNITY_EDITOR
                throw new ProtocolException("bad_request", "WebGL players have no UDP: use a ws:// or wss:// URL instead of kcp://");
#else
                if (options.KcpServerKey != null && options.KcpServerKey.Length != 32)
                    throw new ProtocolException("bad_request", "KcpServerKey is the server's 32-byte X25519 public key");
#endif
            }
            var body = Json.Parse(runtimeJson) as JObject;
            if (body == null || body["version"]?.Type != JTokenType.String || body["schemaHash"]?.Type != JTokenType.String || !(body["wireSchema"] is JObject wire) || !(body["channels"]?[options.Channel ?? ""] is JObject channel))
                throw new ProtocolException("bad_request", "Load the generated runtime.json of a contract with channel " + options.Channel);
            version = (string)body["version"];
            schemaHash = (string)body["schemaHash"];
            validator = new WireValidator(wire);
            normalizer = new WireValidator(wire, WireValidator.MaxDepth, WireValidator.ProtocolMaxNodes, true);
            if (options.Encoding == RealtimeEncoding.Proto)
                codec = new ProtobufCodec(body["codecSchema"] as JObject);
            heartbeatMS = (long)channel["heartbeatMS"];
            foreach (var p in ((JObject)channel["messages"]).Properties())
            {
                var m = (JObject)p.Value;
                var spec = new Spec { Name = p.Name, Kind = (string)m["kind"], Type = (string)m["type"], ResponseType = (string)m["responseType"], Id = (uint)m["id"], TimeoutMS = m["timeoutMS"]?.Value<int>() ?? 0, Volatile = m["volatile"]?.Value<bool>() ?? false };
                byName[spec.Name] = spec;
                byId[spec.Id] = spec;
            }
        }

        private static bool IsKcp(string url) => url != null && url.StartsWith("kcp://", StringComparison.OrdinalIgnoreCase);

        public RealtimeState State { get { lock (gate) return state; } }
        public string SessionId { get { lock (gate) return sessionId; } }

        private long Now => clock.ElapsedMilliseconds;

        /// <summary>Starts connecting. Progress is reported through StateChanged.</summary>
        public void Connect()
        {
            lock (gate)
            {
                if (closedError != null)
                    throw closedError;
                if (state == RealtimeState.Idle)
                    Open();
            }
        }

        /// <summary>Processes network events, timers and callbacks.</summary>
        public void Tick()
        {
            Action[] run;
            lock (gate)
            {
                while (inbox.TryDequeue(out var action))
                    action();
                Timers(Now);
                run = callbacks.ToArray();
                callbacks.Clear();
            }
            foreach (var callback in run)
            {
                try
                {
                    callback();
                }
                catch (Exception error)
                {
                    if (HandlerError == null)
                        throw;
                    HandlerError(error);
                }
            }
        }

        /// <summary>Ends the session on the server and stops reconnecting.</summary>
        public void Close()
        {
            lock (gate)
                Shutdown(new ProtocolException("closed", "Closed by client"), true);
            Tick();
        }

        /// <summary>Subscribes to a push; returns an action that unsubscribes. Unknown pushes are ignored.</summary>
        public Action On(string name, Action<JObject> handler)
        {
            lock (gate)
            {
                if (!byName.TryGetValue(name, out var spec) || spec.Kind != "push" || handler == null)
                    throw new ProtocolException("invalid_message", "No push message " + name);
                if (!handlers.TryGetValue(name, out var list))
                    handlers[name] = list = new List<Action<JObject>>();
                list.Add(handler);
                return () => { lock (gate) list.Remove(handler); };
            }
        }

        /// <param name="timeoutMS">0 uses the contract timeout; a smaller value shortens it.</param>
        public RealtimeRequest Request(string name, JObject request, int timeoutMS = 0)
        {
            lock (gate)
            {
                if (!byName.TryGetValue(name, out var spec) || spec.Kind != "request")
                    throw new ProtocolException("invalid_message", "No request message " + name);
                if (closedError != null)
                    throw closedError;
                var call = new RealtimeRequest { ResponseType = spec.ResponseType };
                var frame = Encode(spec, request);
                frame.Kind = RealtimeFrame.Request;
                frame.Call = call;
                var timeout = timeoutMS > 0 ? Math.Min(timeoutMS, spec.TimeoutMS) : spec.TimeoutMS;
                call.Deadline = Now + timeout;
                Enqueue(frame);
                return call;
            }
        }

        /// <summary>Reliable notifies queue while offline; volatile ones return false when dropped.</summary>
        public bool Notify(string name, JObject message)
        {
            lock (gate)
            {
                if (!byName.TryGetValue(name, out var spec) || spec.Kind != "notify")
                    throw new ProtocolException("invalid_message", "No notify message " + name);
                if (closedError != null)
                    throw closedError;
                var frame = Encode(spec, message);
                frame.Kind = RealtimeFrame.Notify;
                if (!spec.Volatile)
                {
                    Enqueue(frame);
                    return true;
                }
                if (conn == null || !conn.Ready)
                    return false;
                Write(conn, frame);
                return true;
            }
        }

        private OutFrame Encode(Spec spec, JObject value)
        {
            JToken canonical;
            try
            {
                canonical = normalizer.Validate(spec.Type, value?.DeepClone());
            }
            catch (ProtocolException error)
            {
                throw new ProtocolException("invalid_message", error.Message);
            }
            var frame = new OutFrame { Message = spec.Id };
            if (codec != null)
                frame.Payload = codec.Encode(spec.Type, canonical);
            else
                frame.JsonPayload = (JObject)canonical;
            return frame;
        }

        private JObject DecodePayload(string type, RealtimeFrame f)
        {
            if (codec != null)
                return codec.Decode(type, f.Payload ?? new byte[0]);
            var value = f.JsonPayload ?? new JObject();
            validator.Validate(type, value);
            return value;
        }

        private void SetState(RealtimeState next, ProtocolException error = null)
        {
            state = next;
            var handler = StateChanged;
            if (handler != null)
                callbacks.Add(() => handler(next, error));
        }

        private void Enqueue(OutFrame f)
        {
            if (outbox.Count >= options.MaxUnacked)
                throw new ProtocolException("overloaded", "Too many unacknowledged messages", true);
            f.Seq = ++nextSeq;
            outbox.Add(f);
            if (f.Call != null)
                pending[f.Seq] = f.Call;
            if (conn != null && conn.Ready)
                Write(conn, f);
        }

        private void Write(Connection c, OutFrame f)
        {
            if (f.Seq != 0 && f.Seq <= peerAck)
                return;
            f.Written = true;
            ackSent = received;
            Send(c, new RealtimeFrame { Kind = f.Kind, Seq = f.Seq, AckSeq = received, Message = f.Message, Payload = f.Payload, JsonPayload = f.JsonPayload });
        }

        private void Send(Connection c, RealtimeFrame frame)
        {
            try
            {
                if (codec != null)
                    c.Socket.Send(frame.EncodeBinary(), false);
                else
                    c.Socket.Send(Encoding.UTF8.GetBytes(frame.EncodeJson()), true);
            }
            catch (Exception)
            {
                Drop(c, null);
            }
        }

        private void Open()
        {
            if (closedError != null)
                return;
            retryAt = -1;
            SetState(RealtimeState.Connecting);
            var c = new Connection { Client = this, IdleDeadline = Now + heartbeatMS * 2 + 1000 };
            conn = c;
            try
            {
                var subprotocol = codec != null ? SubprotocolProto : SubprotocolJson;
                if (options.Socket == null && IsKcp(options.Url))
                    c.Socket = KcpTransport.Connect(options.Url, subprotocol, options.KcpServerKey, c);
                else
                    c.Socket = (options.Socket ?? ClientWebSocketTransport.Factory)(options.Url, subprotocol, c);
            }
            catch (Exception)
            {
                conn = null;
                ScheduleRetry(false);
            }
        }

        private void OnOpened(Connection c, string protocol)
        {
            if (conn != c)
                return;
            var expected = codec != null ? SubprotocolProto : SubprotocolJson;
            if (!string.IsNullOrEmpty(protocol) && protocol != expected)
            {
                Shutdown(new ProtocolException("protocol_error", "Server did not accept " + expected), false);
                return;
            }
            string token = null;
            if (sessionId == "" && options.Token != null)
            {
                try
                {
                    token = options.Token();
                }
                catch (Exception)
                {
                    Drop(c, null);
                    return;
                }
            }
            Send(c, new RealtimeFrame
            {
                Kind = RealtimeFrame.Hello, AckSeq = received, HelloVersion = version, HelloSchemaHash = schemaHash, HelloToken = token,
                HelloSessionId = sessionId == "" ? null : sessionId, HelloResumeToken = resumeToken == "" ? null : resumeToken
            });
        }

        private void Drop(Connection c, ProtocolException error)
        {
            if (conn != c)
                return;
            conn = null;
            try { c.Socket?.Close(); } catch (Exception) { /* closed */ }
            if (closedError != null)
                return;
            if (error != null && !error.Retryable)
            {
                Shutdown(error, false);
                return;
            }
            if (c.Ready || state != RealtimeState.Disconnected)
                SetState(RealtimeState.Disconnected, error);
            ScheduleRetry(false);
        }

        private void ScheduleRetry(bool immediate)
        {
            if (closedError != null)
                return;
            long delay = 0;
            if (!immediate)
            {
                var bound = Math.Min(options.MaxBackoffMS, options.MinBackoffMS * Math.Pow(2, Math.Min(failures, 16)));
                delay = (long)(bound / 2 + random.NextDouble() * bound / 2);
                failures++;
            }
            if (state != RealtimeState.Disconnected)
                SetState(RealtimeState.Disconnected);
            retryAt = Now + delay;
        }

        private void Timers(long now)
        {
            if (closedError != null)
                return;
            if (retryAt >= 0 && now >= retryAt && conn == null)
                Open();
            var c = conn;
            if (c != null && now >= c.IdleDeadline)
            {
                Drop(c, null);
                c = null;
            }
            if (c != null && c.Ready && now >= c.NextPing)
            {
                c.NextPing = now + heartbeatMS;
                Send(c, new RealtimeFrame { Kind = RealtimeFrame.Ping, AckSeq = received });
            }
            if (ackAt >= 0 && now >= ackAt)
            {
                ackAt = -1;
                if (conn != null && conn.Ready && received > ackSent)
                {
                    ackSent = received;
                    Send(conn, new RealtimeFrame { Kind = RealtimeFrame.Ack, AckSeq = received });
                }
            }
            List<ulong> expired = null;
            foreach (var entry in pending)
                if (now >= entry.Value.Deadline)
                    (expired ?? (expired = new List<ulong>())).Add(entry.Key);
            if (expired != null)
                foreach (var seq in expired)
                {
                    var call = pending[seq];
                    pending.Remove(seq);
                    Finish(call, null, new ProtocolException("timeout", "Request deadline exceeded"));
                }
        }

        private void Finish(RealtimeRequest call, JObject value, ProtocolException error)
        {
            if (call.Done)
                return;
            call.Done = true;
            callbacks.Add(error == null ? call.Resolve(value) : call.Reject(error));
        }

        private void OnReceived(Connection c, byte[] data, bool text)
        {
            if (conn != c)
                return;
            if (data.Length == 0)
                return; // Empty messages are not frames.
            RealtimeFrame f;
            try
            {
                if (codec != null)
                {
                    if (text)
                        throw new ProtocolException("protocol_error", "Text frame on binary subprotocol", true);
                    f = RealtimeFrame.DecodeBinary(data);
                }
                else
                {
                    if (!text)
                        throw new ProtocolException("protocol_error", "Binary frame on JSON subprotocol", true);
                    string json;
                    try { json = new UTF8Encoding(false, true).GetString(data); }
                    catch (DecoderFallbackException) { throw new ProtocolException("protocol_error", "Invalid UTF-8", true); }
                    f = RealtimeFrame.DecodeJson(json);
                }
                f.ValidateFromServer();
            }
            catch (ProtocolException error)
            {
                Drop(c, new ProtocolException("protocol_error", error.Message, true));
                return;
            }
            c.IdleDeadline = Now + heartbeatMS * 2 + 1000;
            if (f.Kind == RealtimeFrame.Close)
            {
                ServerClosed(c, new ProtocolException(f.ErrorCode, f.ErrorMessage ?? "", f.ErrorRetryable));
                return;
            }
            if (!c.Ready)
            {
                if (f.Kind != RealtimeFrame.Welcome)
                    Drop(c, new ProtocolException("protocol_error", "Expected welcome", true));
                else
                    Welcome(c, f);
                return;
            }
            if (f.Kind == RealtimeFrame.Welcome)
            {
                Drop(c, new ProtocolException("protocol_error", "Unexpected welcome", true));
                return;
            }
            if (!ApplyAck(c, f.AckSeq) || f.Kind == RealtimeFrame.Pong || f.Kind == RealtimeFrame.Ack)
                return;
            if (f.Kind == RealtimeFrame.Push && f.Seq == 0)
            {
                DeliverPush(f);
                return;
            }
            if (f.Seq <= received)
            {
                ackSent = 0; // A retransmission: acknowledge again.
                ScheduleAck();
                return;
            }
            if (f.Seq != received + 1)
            {
                Drop(c, new ProtocolException("protocol_error", "Sequence gap from server", true));
                return;
            }
            received = f.Seq;
            ScheduleAck();
            if (f.Kind == RealtimeFrame.Push)
            {
                DeliverPush(f);
                return;
            }
            if (!pending.TryGetValue(f.ReplyTo, out var call))
                return; // Timed out; the late answer is discarded.
            pending.Remove(f.ReplyTo);
            if (f.HasError)
            {
                Finish(call, null, new ProtocolException(f.ErrorCode, f.ErrorMessage ?? "", f.ErrorRetryable));
                return;
            }
            try
            {
                Finish(call, DecodePayload(call.ResponseType, f), null);
            }
            catch (ProtocolException error)
            {
                Finish(call, null, new ProtocolException("protocol_error", "Invalid response: " + error.Message));
            }
        }

        private void Welcome(Connection c, RealtimeFrame f)
        {
            var resuming = sessionId != "";
            if (f.WelcomeResumed && f.WelcomeSessionId != sessionId)
            {
                Drop(c, new ProtocolException("protocol_error", "Server resumed a different session"));
                return;
            }
            if (!f.WelcomeResumed)
            {
                if (resuming)
                    ResetSession();
                received = 0;
                peerAck = 0;
            }
            sessionId = f.WelcomeSessionId;
            resumeToken = f.WelcomeResumeToken;
            heartbeatMS = f.WelcomeHeartbeatMS;
            c.Ready = true;
            failures = 0;
            if (!ApplyAck(c, f.AckSeq))
                return;
            ackSent = 0;
            foreach (var frame in outbox.ToArray())
                if (conn == c)
                    Write(c, frame);
            if (conn != c)
                return;
            c.NextPing = Now + heartbeatMS;
            c.IdleDeadline = Now + heartbeatMS * 2 + 1000;
            SetState(f.WelcomeResumed ? RealtimeState.Resumed : RealtimeState.Connected);
        }

        private bool ApplyAck(Connection c, ulong ack)
        {
            if (ack > nextSeq)
            {
                Drop(c, new ProtocolException("protocol_error", "Server acknowledged an unsent frame", true));
                return false;
            }
            if (ack > peerAck)
            {
                peerAck = ack;
                var n = 0;
                while (n < outbox.Count && outbox[n].Seq <= ack)
                    n++;
                if (n > 0)
                    outbox.RemoveRange(0, n);
            }
            return true;
        }

        private void ScheduleAck()
        {
            if (ackAt < 0)
                ackAt = Now + 50;
        }

        private void DeliverPush(RealtimeFrame f)
        {
            if (!byId.TryGetValue(f.Message, out var spec) || spec.Kind != "push")
                return; // Pushes added by a newer server are ignored.
            if (!handlers.TryGetValue(spec.Name, out var list) || list.Count == 0)
                return;
            JObject message;
            try
            {
                message = DecodePayload(spec.Type, f);
            }
            catch (ProtocolException error)
            {
                var handler = HandlerError;
                if (handler != null)
                    callbacks.Add(() => handler(error));
                return;
            }
            foreach (var h in list.ToArray())
                callbacks.Add(() => h(message));
        }

        private void ServerClosed(Connection c, ProtocolException error)
        {
            if (error.Code == "session_expired" && sessionId != "")
            {
                // The server forgot the session: start a new one right away.
                ResetSession();
                conn = null;
                try { c.Socket?.Close(); } catch (Exception) { /* closed */ }
                ScheduleRetry(true);
                return;
            }
            Drop(c, error);
        }

        /// <summary>
        /// Forget the session. Frames never written move to the next session;
        /// requests that may have reached the server fail with session_lost.
        /// </summary>
        private void ResetSession()
        {
            var kept = new List<OutFrame>();
            var lost = new List<RealtimeRequest>();
            foreach (var f in outbox)
            {
                if (!f.Written)
                    kept.Add(f);
                else if (f.Call != null && pending.TryGetValue(f.Seq, out var call) && call == f.Call)
                    lost.Add(f.Call);
            }
            var keptCalls = new HashSet<RealtimeRequest>();
            foreach (var f in kept)
                if (f.Call != null)
                    keptCalls.Add(f.Call);
            foreach (var call in pending.Values)
                if (!keptCalls.Contains(call) && !lost.Contains(call))
                    lost.Add(call);
            pending.Clear();
            for (var i = 0; i < kept.Count; i++)
            {
                kept[i].Seq = (ulong)(i + 1);
                if (kept[i].Call != null)
                    pending[kept[i].Seq] = kept[i].Call;
            }
            outbox = kept;
            nextSeq = (ulong)kept.Count;
            sessionId = resumeToken = "";
            received = peerAck = ackSent = 0;
            foreach (var call in lost)
                Finish(call, null, new ProtocolException("session_lost", "Connection lost; the request may or may not have run"));
        }

        private void Shutdown(ProtocolException error, bool notifyServer)
        {
            if (closedError != null)
                return;
            closedError = error;
            retryAt = ackAt = -1;
            var c = conn;
            conn = null;
            if (c != null)
            {
                if (notifyServer && c.Ready)
                    Send(c, new RealtimeFrame { Kind = RealtimeFrame.Close, AckSeq = received, HasError = true, ErrorCode = error.Code, ErrorMessage = error.Message });
                try { c.Socket?.Close(); } catch (Exception) { /* closed */ }
            }
            foreach (var call in pending.Values)
                Finish(call, null, error);
            pending.Clear();
            outbox.Clear();
            SetState(RealtimeState.Closed, error);
        }
    }
}
