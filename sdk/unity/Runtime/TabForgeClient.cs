using System;
using System.Collections;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Text;
using Newtonsoft.Json;
using Newtonsoft.Json.Linq;
using UnityEngine;
using UnityEngine.Networking;

namespace TabForge.Protocol
{
    public sealed class RequestHandle
    {
        public event Action<StreamEvent> EventReceived;
        public event Action<JToken> Completed;
        public event Action<ProtocolException> Failed;
        public bool Done { get; private set; }
        public JToken Result { get; private set; }
        public ProtocolException Error { get; private set; }
        public string RequestId { get; internal set; }
        internal volatile bool Cancelled;
        internal UnityWebRequest Network;
        private bool disposed;
        /// May be called from another thread; Unity API cleanup stays on the main thread.
        public void Cancel() { Cancelled = true; }
        internal void Emit(StreamEvent value) { if (!Done) EventReceived?.Invoke(value); }
        internal void Finish(JToken value) { if (Done) return; Done = true; Result = value; Completed?.Invoke(value); }
        internal void Fail(ProtocolException error) { if (Done) return; Done = true; Error = error; Failed?.Invoke(error); }
        internal void DisposeNetwork()
        {
            if (disposed || Network == null) return;
            disposed = true;
            var request = Network; Network = null;
            request.Abort(); request.Dispose();
        }
    }

    /// Configure on Unity's main thread; callbacks run there. Native HTTP streaming
    /// uses DownloadHandlerScript, not DownloadHandlerBuffer or a completed body.
    public sealed class TabForgeClient : MonoBehaviour
    {
        public int MaxResponseBytes = 1048576;
        public int MaxFrameChars = 1048576;
        public int MaxConcurrentRequests = 16;
        private string baseUrl;
        private ProtocolContract contract;
        private readonly List<RequestHandle> active = new List<RequestHandle>();

        public void Configure(string url, string runtimeJson)
        {
            if (active.Count != 0) throw new ProtocolException("busy", "Cannot reconfigure active requests");
            if (!Uri.TryCreate(url, UriKind.Absolute, out var uri) || (uri.Scheme != "https" && uri.Scheme != "http") || uri.UserInfo.Length != 0 || uri.Query.Length != 0 || uri.Fragment.Length != 0) throw new ProtocolException("bad_request", "Expected HTTP(S) base URL");
            contract = new ProtocolContract(runtimeJson); baseUrl = url.TrimEnd('/');
        }
        public RequestHandle Request(string operationId, JToken data, string token = "", string requestId = null) { return Create(operationId, data, token, requestId, false); }
        public RequestHandle Stream(string operationId, JToken data, string token = "", string requestId = null) { return Create(operationId, data, token, requestId, true); }
        private RequestHandle Create(string id, JToken data, string token, string requestId, bool stream)
        {
            if (!isActiveAndEnabled) throw new ProtocolException("not_ready", "Client must be active in the scene");
            var handle = new RequestHandle { RequestId = requestId ?? Guid.NewGuid().ToString("N") };
            var snapshot = data?.DeepClone();
            var overLimit = active.Count >= MaxConcurrentRequests;
            active.Add(handle);
            StartCoroutine(Run(handle, id, snapshot, token, stream, overLimit));
            return handle;
        }
        private IEnumerator Run(RequestHandle handle, string id, JToken data, string token, bool stream, bool overLimit)
        {
            // Let callers attach handlers even when configuration is invalid.
            yield return null;
            ProtocolException failure = null;
            var watch = Stopwatch.StartNew();
            UnityWebRequest request = null;
            StreamingDownload download = null;
            int deadline = 0;
            try
            {
                if (handle.Cancelled) throw new ProtocolException("cancelled", "Request cancelled");
                if (contract == null) throw new ProtocolException("not_ready", "Configure generated runtime.json first");
                if (MaxResponseBytes < 1 || MaxFrameChars < 1 || MaxConcurrentRequests < 1) throw new ProtocolException("bad_request", "Invalid limits");
                if (overLimit) throw new ProtocolException("busy", "Concurrent request limit reached");
#if UNITY_WEBGL && !UNITY_EDITOR
                if (stream) throw new ProtocolException("unsupported_transport", "Unity WebGL SSE requires a JavaScript fetch bridge");
#endif
                var operation = contract.Operation(id, stream ? "http_sse" : "http_json");
                contract.Validate((string)operation["requestType"], data);
                if ((string)operation["auth"] == "bearer" && string.IsNullOrEmpty(token)) throw new ProtocolException("unauthorized", "Bearer token required");
                if (token != null && (token.Contains("\r") || token.Contains("\n"))) throw new ProtocolException("bad_request", "Invalid bearer token");
                var session = new ProtocolSession(contract, operation, handle.RequestId);
                deadline = (int)operation["timeoutMS"];
                if (deadline <= 0) throw new ProtocolException("bad_request", "Invalid timeout");
                request = new UnityWebRequest(baseUrl + (string)operation["path"], "POST");
                handle.Network = request;
                request.redirectLimit = 0; // Never forward bearer tokens across redirects.
                request.timeout = Math.Max(1, (int)Math.Ceiling(deadline / 1000d));
                request.uploadHandler = new UploadHandlerRaw(new UTF8Encoding(false, true).GetBytes(data.ToString(Formatting.None)));
                download = new StreamingDownload(request, handle, session, stream, MaxResponseBytes, MaxFrameChars);
                request.downloadHandler = download;
                request.SetRequestHeader("Content-Type", "application/json");
                request.SetRequestHeader("Accept", stream ? "text/event-stream" : "application/json");
                request.SetRequestHeader("X-Protocol-Version", contract.Version);
                request.SetRequestHeader("X-Protocol-Schema", contract.SchemaHash);
                request.SetRequestHeader("X-Request-ID", handle.RequestId);
                if (!string.IsNullOrEmpty(token)) request.SetRequestHeader("Authorization", "Bearer " + token);
                request.SendWebRequest();
            }
            catch (ProtocolException exception) { failure = exception; }
            catch (Exception) { failure = new ProtocolException("transport_error", "Cannot start UnityWebRequest"); }

            try
            {
                while (failure == null && !handle.Done && !request.isDone)
                {
                    if (handle.Cancelled) { failure = new ProtocolException("cancelled", "Request cancelled"); break; }
                    if (watch.ElapsedMilliseconds >= deadline) { failure = new ProtocolException("timeout", "Request deadline exceeded", true); break; }
                    if (download.Error != null) { failure = download.Error; break; }
                    yield return null;
                }
                if (!handle.Done && failure == null)
                {
                    if (handle.Cancelled) failure = new ProtocolException("cancelled", "Request cancelled");
                    else if (watch.ElapsedMilliseconds >= deadline) failure = new ProtocolException("timeout", "Request deadline exceeded", true);
                    else if (download.Error != null) failure = download.Error;
                    else if (request.result == UnityWebRequest.Result.ConnectionError || request.result == UnityWebRequest.Result.DataProcessingError) failure = new ProtocolException("transport_error", "UnityWebRequest failed");
                    else
                    {
                        try { download.CompleteResponse(); }
                        catch (ProtocolException exception) { failure = exception; }
                        catch (Exception) { failure = new ProtocolException("transport_error", "Cannot complete response"); }
                    }
                }
                if (!handle.Done && failure != null) handle.Fail(failure);
            }
            finally { handle.DisposeNetwork(); active.Remove(handle); }
        }
        private void OnDisable()
        {
            foreach (var handle in active.ToArray())
            {
                try { handle.Fail(new ProtocolException("cancelled", "Client left the scene")); }
                catch (Exception exception) { UnityEngine.Debug.LogException(exception); }
                finally { handle.DisposeNetwork(); }
            }
            active.Clear(); StopAllCoroutines();
        }

        private sealed class StreamingDownload : DownloadHandlerScript
        {
            public ProtocolException Error { get; private set; }
            private readonly UnityWebRequest request;
            private readonly RequestHandle handle;
            private readonly ProtocolSession session;
            private readonly bool streaming;
            private readonly int maximum;
            private readonly SseParser parser;
            private readonly MemoryStream body = new MemoryStream();
            private bool checkedHeaders;
            public StreamingDownload(UnityWebRequest request, RequestHandle handle, ProtocolSession session, bool streaming, int maximum, int maximumFrame) : base(new byte[16384])
            { this.request = request; this.handle = handle; this.session = session; this.streaming = streaming; this.maximum = maximum; parser = new SseParser(maximumFrame); }
            private bool IsStream => streaming && request.responseCode >= 200 && request.responseCode < 300;
            private void CheckHeaders()
            {
                if (checkedHeaders) return;
                var contentType = (request.GetResponseHeader("Content-Type") ?? "").Split(';')[0].Trim().ToLowerInvariant();
                if (contentType != (IsStream ? "text/event-stream" : "application/json")) throw new ProtocolException("unsupported_media_type", "Unexpected HTTP response content type");
                checkedHeaders = true;
            }
            protected override bool ReceiveData(byte[] bytes, int count)
            {
                if (handle.Done || handle.Cancelled) return false;
                if (count == 0) return true;
                try
                {
                    CheckHeaders();
                    if (IsStream)
                    {
                        parser.Feed(bytes, count, frame => {
                            if (handle.Done || handle.Cancelled) return;
                            var value = session.Accept(frame);
                            handle.Emit(value);
                            if (session.Terminal) handle.Finish(value.Payload);
                        });
                    }
                    else { if (body.Length + count > maximum) throw new ProtocolException("response_too_large", "Response exceeds configured limit"); body.Write(bytes, 0, count); }
                    return !handle.Done && !handle.Cancelled;
                }
                catch (ProtocolException exception) { Error = exception; return false; }
                catch (Exception) { Error = new ProtocolException("callback_error", "Response callback failed"); return false; }
            }
            public void CompleteResponse()
            {
                CheckHeaders();
                if (IsStream) { parser.Finish(); session.Finish(); }
                else
                {
                    string text;
                    try { text = new UTF8Encoding(false, true).GetString(body.ToArray()); }
                    catch (DecoderFallbackException) { throw new ProtocolException("invalid_utf8", "Invalid UTF-8 response"); }
                    handle.Finish(session.Unary(text, request.responseCode));
                }
            }
        }
    }
}
