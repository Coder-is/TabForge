#if UNITY_5_3_OR_NEWER
using UnityEngine;

namespace TabForge.Protocol
{
    /// <summary>
    /// Drives a RealtimeClient from Update so every callback runs on the main
    /// thread, and closes it with the GameObject. Keep the GameObject alive
    /// across scenes (DontDestroyOnLoad) to keep the session.
    /// </summary>
    public sealed class TabForgeRealtime : MonoBehaviour
    {
        public RealtimeClient Client { get; private set; }

        /// <param name="runtimeJson">Text of the generated runtime.json (for example a TextAsset).</param>
        public RealtimeClient Configure(string runtimeJson, RealtimeOptions options)
        {
            Client?.Close();
            Client = new RealtimeClient(runtimeJson, options);
            Client.HandlerError += Debug.LogException;
            return Client;
        }

        private void Update() => Client?.Tick();

        private void OnDestroy() => Client?.Close();

        private void OnApplicationQuit() => Client?.Close();
    }
}
#endif
