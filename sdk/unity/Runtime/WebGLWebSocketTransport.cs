using System;
using System.Collections.Generic;
using System.IO;
#if UNITY_WEBGL && !UNITY_EDITOR
using System.Runtime.InteropServices;
#endif

namespace TabForge.Protocol
{
    /// <summary>
    /// Realtime socket for Unity WebGL builds, bridged to the browser WebSocket
    /// by Plugins/WebGL/TabForgeWebSocket.jslib. ClientWebSocketTransport.Factory
    /// selects it automatically in WebGL players, so RealtimeOptions.Socket can
    /// stay null. Browser events are queued and handled in RealtimeClient.Tick.
    /// </summary>
    public sealed class WebGLWebSocketTransport : IRealtimeSocket
    {
        public const int MaxMessageBytes = 1 << 20;

        /// <summary>The jslib functions; replaced by a fake in plain .NET tests.</summary>
        internal interface INative
        {
            void Init();
            int Open(string url, string protocol, int maxMessageBytes);
            bool Send(int id, byte[] data, bool text);
            void Close(int id);
        }

#if UNITY_WEBGL && !UNITY_EDITOR
        internal static INative Native = new BrowserNative();
#else
        internal static INative Native;
#endif

        private static readonly object gate = new object();
        private static readonly Dictionary<int, WebGLWebSocketTransport> sockets = new Dictionary<int, WebGLWebSocketTransport>();
        private static INative initialized;

        public static readonly RealtimeSocketFactory Factory = (url, subprotocol, events) =>
        {
            var native = Native ?? throw new PlatformNotSupportedException("The WebGL WebSocket bridge is only available in WebGL players");
            lock (gate)
            {
                if (initialized != native)
                {
                    native.Init();
                    initialized = native;
                }
                // The bridge reports every event asynchronously, so registering
                // after Open returns cannot miss one.
                var transport = new WebGLWebSocketTransport(native, events);
                transport.id = native.Open(url, subprotocol, MaxMessageBytes);
                sockets[transport.id] = transport;
                return transport;
            }
        };

        private readonly INative native;
        private readonly IRealtimeSocketEvents events;
        private int id;
        private bool closing;

        private WebGLWebSocketTransport(INative native, IRealtimeSocketEvents events)
        {
            this.native = native;
            this.events = events;
        }

        public void Send(byte[] data, bool text)
        {
            lock (gate)
            {
                if (closing || !sockets.ContainsKey(id))
                    throw new IOException("Socket is closed");
            }
            if (!native.Send(id, data, text))
                throw new IOException("Socket is not open");
        }

        /// <summary>Sends what is already queued, then closes; Closed follows within one second.</summary>
        public void Close()
        {
            lock (gate)
            {
                if (closing || !sockets.ContainsKey(id))
                    return;
                closing = true;
            }
            native.Close(id);
        }

        /// <summary>The socket for a browser event, or null once it is closing or closed.</summary>
        private static WebGLWebSocketTransport Active(int id)
        {
            lock (gate)
                return sockets.TryGetValue(id, out var socket) && !socket.closing ? socket : null;
        }

        internal static void OnOpen(int id, string protocol) => Active(id)?.events.Opened(protocol ?? "");

        internal static void OnMessage(int id, byte[] data, bool text) => Active(id)?.events.Received(data, text);

        internal static void OnClose(int id)
        {
            WebGLWebSocketTransport socket;
            lock (gate)
            {
                if (!sockets.TryGetValue(id, out socket))
                    return;
                sockets.Remove(id);
            }
            socket.events.Closed();
        }

#if UNITY_WEBGL && !UNITY_EDITOR
        private sealed class BrowserNative : INative
        {
            private delegate void OpenCallback(int id, IntPtr protocol);
            private delegate void MessageCallback(int id, IntPtr data, int length, int text);
            private delegate void CloseCallback(int id);

            [DllImport("__Internal")] private static extern void TabForgeWS_Init(OpenCallback onOpen, MessageCallback onMessage, CloseCallback onClose);
            [DllImport("__Internal")] private static extern int TabForgeWS_Open(string url, string protocol, int maxMessageBytes);
            [DllImport("__Internal")] private static extern int TabForgeWS_Send(int id, byte[] data, int length, int text);
            [DllImport("__Internal")] private static extern void TabForgeWS_Close(int id);

            public void Init() => TabForgeWS_Init(Opened, Received, Closed);
            public int Open(string url, string protocol, int maxMessageBytes) => TabForgeWS_Open(url, protocol, maxMessageBytes);
            public bool Send(int id, byte[] data, bool text) => TabForgeWS_Send(id, data, data.Length, text ? 1 : 0) != 0;
            public void Close(int id) => TabForgeWS_Close(id);

            // Called from JavaScript; exceptions must not unwind into the browser.
            [AOT.MonoPInvokeCallback(typeof(OpenCallback))]
            private static void Opened(int id, IntPtr protocol)
            {
                try { OnOpen(id, Marshal.PtrToStringAnsi(protocol)); }
                catch (Exception e) { UnityEngine.Debug.LogException(e); }
            }

            [AOT.MonoPInvokeCallback(typeof(MessageCallback))]
            private static void Received(int id, IntPtr data, int length, int text)
            {
                try
                {
                    var bytes = new byte[length];
                    if (length > 0)
                        Marshal.Copy(data, bytes, 0, length);
                    OnMessage(id, bytes, text != 0);
                }
                catch (Exception e) { UnityEngine.Debug.LogException(e); }
            }

            [AOT.MonoPInvokeCallback(typeof(CloseCallback))]
            private static void Closed(int id)
            {
                try { OnClose(id); }
                catch (Exception e) { UnityEngine.Debug.LogException(e); }
            }
        }
#endif
    }
}
