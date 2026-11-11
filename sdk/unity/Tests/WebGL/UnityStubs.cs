// Minimal stand-ins for the Unity types the WebGL branch references.
namespace AOT
{
    [System.AttributeUsage(System.AttributeTargets.Method)]
    public sealed class MonoPInvokeCallbackAttribute : System.Attribute
    {
        public MonoPInvokeCallbackAttribute(System.Type type) { }
    }
}

namespace UnityEngine
{
    public static class Debug
    {
        public static void LogException(System.Exception exception) { }
    }
}
