using System;
using System.Collections.Concurrent;
using System.IO;
using System.Net.WebSockets;
using System.Threading;
using System.Threading.Tasks;

namespace TabForge.Protocol
{
    /// <summary>
    /// Default realtime socket on System.Net.WebSockets.ClientWebSocket
    /// (Unity standalone, Android and iOS; .NET). WebGL has no ClientWebSocket:
    /// supply a RealtimeSocketFactory that bridges to the browser WebSocket.
    /// </summary>
    public sealed class ClientWebSocketTransport : IRealtimeSocket
    {
        public const int MaxMessageBytes = 1 << 20;

        /// <summary>Optional hook to set proxies, certificates or headers before connecting.</summary>
        public static Action<ClientWebSocketOptions> Configure;

        public static readonly RealtimeSocketFactory Factory = (url, subprotocol, events) =>
        {
#if UNITY_WEBGL && !UNITY_EDITOR
            throw new PlatformNotSupportedException("ClientWebSocket is not available on WebGL; provide RealtimeOptions.Socket");
#else
            var transport = new ClientWebSocketTransport(events);
            transport.Start(new Uri(url), subprotocol);
            return transport;
#endif
        };

        private readonly IRealtimeSocketEvents events;
        private readonly ClientWebSocket socket = new ClientWebSocket();
        private readonly CancellationTokenSource cancel = new CancellationTokenSource();
        private readonly ConcurrentQueue<(byte[], bool)> outgoing = new ConcurrentQueue<(byte[], bool)>();
        private readonly SemaphoreSlim signal = new SemaphoreSlim(0);
        private int closed, closing;

        private ClientWebSocketTransport(IRealtimeSocketEvents events)
        {
            this.events = events;
        }

        private void Start(Uri uri, string subprotocol)
        {
            socket.Options.AddSubProtocol(subprotocol);
            Configure?.Invoke(socket.Options);
            Task.Run(() => Run(uri));
        }

        private async Task Run(Uri uri)
        {
            try
            {
                await socket.ConnectAsync(uri, cancel.Token).ConfigureAwait(false);
                events.Opened(socket.SubProtocol ?? "");
                _ = Task.Run(SendLoop);
                var buffer = new byte[16 * 1024];
                using (var message = new MemoryStream())
                {
                    while (!cancel.IsCancellationRequested)
                    {
                        var result = await socket.ReceiveAsync(new ArraySegment<byte>(buffer), cancel.Token).ConfigureAwait(false);
                        if (result.MessageType == WebSocketMessageType.Close)
                            break;
                        if (message.Length + result.Count > MaxMessageBytes)
                            break;
                        message.Write(buffer, 0, result.Count);
                        if (!result.EndOfMessage)
                            continue;
                        events.Received(message.ToArray(), result.MessageType == WebSocketMessageType.Text);
                        message.SetLength(0);
                    }
                }
            }
            catch (Exception)
            {
                // Connection failures surface as Closed.
            }
            Shutdown();
        }

        private async Task SendLoop()
        {
            try
            {
                while (!cancel.IsCancellationRequested)
                {
                    await signal.WaitAsync(cancel.Token).ConfigureAwait(false);
                    if (!outgoing.TryDequeue(out var item))
                    {
                        if (Volatile.Read(ref closing) != 0)
                            break; // Everything queued before Close() has been sent.
                        continue;
                    }
                    var (data, text) = item;
                    await socket.SendAsync(new ArraySegment<byte>(data), text ? WebSocketMessageType.Text : WebSocketMessageType.Binary, true, cancel.Token).ConfigureAwait(false);
                }
            }
            catch (Exception)
            {
                // Fall through to shutdown.
            }
            Shutdown();
        }

        public void Send(byte[] data, bool text)
        {
            if (Volatile.Read(ref closed) != 0 || Volatile.Read(ref closing) != 0)
                throw new IOException("Socket is closed");
            outgoing.Enqueue((data, text));
            signal.Release();
        }

        /// <summary>Sends what is already queued (such as a close frame), then closes; at most one second.</summary>
        public void Close()
        {
            if (Interlocked.Exchange(ref closing, 1) != 0)
                return;
            signal.Release();
            Task.Delay(1000).ContinueWith(_ => Shutdown());
        }

        private void Shutdown()
        {
            if (Interlocked.Exchange(ref closed, 1) != 0)
                return;
            cancel.Cancel();
            try { socket.Abort(); } catch (Exception) { /* already closed */ }
            socket.Dispose();
            events.Closed();
        }
    }
}
