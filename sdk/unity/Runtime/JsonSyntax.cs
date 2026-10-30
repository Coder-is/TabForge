using System;
using System.Collections.Generic;
using System.Globalization;
using System.Numerics;
using System.Text;
using Newtonsoft.Json.Linq;

namespace TabForge.Protocol
{
    // Strict RFC 8259 JSON: Json.NET accepts JavaScript extensions, so tokens are
    // built here. Duplicate members are rejected after unescaping. Depth counts
    // container nesting (root value = 0); nodes count every JSON value, not keys.
    internal sealed class JsonSyntax
    {
        private readonly string text;
        private readonly int maxDepth, maxNodes;
        private int pos, nodes;
        private bool build, rawNumbers;
        public JsonSyntax(string text, int maxDepth = WireValidator.MaxDepth, int maxNodes = WireValidator.ProtocolMaxNodes)
        {
            this.text = text ?? "";
            this.maxDepth = maxDepth;
            this.maxNodes = maxNodes;
        }

        // Syntax, duplicate and budget check only.
        public bool Valid()
        {
            pos = nodes = 0;
            build = false;
            if (!Value(0, out _))
                return false;
            Space();
            return pos == text.Length;
        }

        // Returns null for invalid JSON. With rawNumbers, integer literals that fit a
        // long become JValue(long) and every other number a JRaw holding its exact
        // literal, which WireValidator normalizes away; otherwise numbers are
        // materialized as long, BigInteger or double.
        public JToken Parse(bool rawNumbers)
        {
            pos = nodes = 0;
            build = true;
            this.rawNumbers = rawNumbers;
            if (!Value(0, out var result))
                return null;
            Space();
            return pos == text.Length ? result : null;
        }

        private void Space()
        {
            while (pos < text.Length && (text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\r' || text[pos] == '\n'))
                pos++;
        }

        private bool Take(char ch)
        {
            Space();
            if (pos == text.Length || text[pos] != ch)
                return false;
            pos++;
            return true;
        }

        private bool Hex(out int code)
        {
            code = 0;
            for (var i = 0; i < 4; i++)
            {
                if (pos == text.Length)
                    return false;
                var ch = text[pos++];
                var digit = ch >= '0' && ch <= '9' ? ch - '0' : ch >= 'a' && ch <= 'f' ? ch - 'a' + 10 : ch >= 'A' && ch <= 'F' ? ch - 'A' + 10 : -1;
                if (digit < 0)
                    return false;
                code = (code << 4) | digit;
            }

            return true;
        }

        private bool String(out string value)
        {
            value = null;
            if (!Take('"'))
                return false;
            var start = pos;
            StringBuilder builder = null;
            while (pos < text.Length)
            {
                var ch = text[pos++];
                if (ch == '"')
                {
                    value = builder == null ? text.Substring(start, pos - 1 - start) : builder.ToString();
                    return true;
                }

                if (ch < 32)
                    return false;
                if (ch == '\\')
                {
                    if (builder == null)
                        builder = new StringBuilder().Append(text, start, pos - 1 - start);
                    if (pos == text.Length)
                        return false;
                    var escape = text[pos++];
                    if (escape == 'u')
                    {
                        if (!Hex(out var code))
                            return false;
                        if (code >= 0xd800 && code <= 0xdbff)
                        {
                            if (pos + 2 > text.Length || text[pos++] != '\\' || text[pos++] != 'u' || !Hex(out var low) || low < 0xdc00 || low > 0xdfff)
                                return false;
                            builder.Append((char)code).Append((char)low);
                        }
                        else if (code >= 0xdc00 && code <= 0xdfff)
                            return false;
                        else
                            builder.Append((char)code);
                    }
                    else
                    {
                        var index = "\"\\/bfnrt".IndexOf(escape);
                        if (index < 0)
                            return false;
                        builder.Append("\"\\/\b\f\n\r\t"[index]);
                    }

                    continue;
                }

                if (char.IsHighSurrogate(ch))
                {
                    if (pos == text.Length || !char.IsLowSurrogate(text[pos++]))
                        return false;
                    builder?.Append(ch).Append(text[pos - 1]);
                    continue;
                }

                if (char.IsLowSurrogate(ch))
                    return false;
                builder?.Append(ch);
            }

            return false;
        }

        private bool Digits()
        {
            var start = pos;
            while (pos < text.Length && text[pos] >= '0' && text[pos] <= '9')
                pos++;
            return pos != start;
        }

        private bool Value(int depth, out JToken result)
        {
            result = null;
            Space();
            if (depth > maxDepth || ++nodes > maxNodes || pos == text.Length)
                return false;
            if (text[pos] == '"')
            {
                if (!String(out var value))
                    return false;
                if (build)
                    result = new JValue(value);
                return true;
            }

            if (Take('{'))
            {
                var record = build ? new JObject() : null;
                result = record;
                if (Take('}'))
                    return true;
                var keys = new HashSet<string>(StringComparer.Ordinal);
                do
                {
                    if (!String(out var key) || !keys.Add(key) || !Take(':') || !Value(depth + 1, out var item))
                        return false;
                    record?.Add(new JProperty(key, item));
                    if (Take('}'))
                        return true;
                }
                while (Take(','));
                return false;
            }

            if (Take('['))
            {
                var array = build ? new JArray() : null;
                result = array;
                if (Take(']'))
                    return true;
                do
                {
                    if (!Value(depth + 1, out var item))
                        return false;
                    array?.Add(item);
                    if (Take(']'))
                        return true;
                }
                while (Take(','));
                return false;
            }

            foreach (var word in new[] { "true", "false", "null" })
                if (pos + word.Length <= text.Length && string.CompareOrdinal(text, pos, word, 0, word.Length) == 0)
                {
                    pos += word.Length;
                    if (build)
                        result = word == "null" ? JValue.CreateNull() : new JValue(word == "true");
                    return true;
                }

            var start = pos;
            var integral = true;
            if (text[pos] == '-')
                pos++;
            if (pos == text.Length)
                return false;
            if (text[pos] == '0')
                pos++;
            else if (text[pos] < '1' || text[pos] > '9' || !Digits())
                return false;
            if (pos < text.Length && text[pos] == '.')
            {
                pos++;
                integral = false;
                if (!Digits())
                    return false;
            }

            if (pos < text.Length && (text[pos] == 'e' || text[pos] == 'E'))
            {
                pos++;
                integral = false;
                if (pos < text.Length && (text[pos] == '+' || text[pos] == '-'))
                    pos++;
                if (!Digits())
                    return false;
            }

            if (build)
            {
                var literal = text.Substring(start, pos - start);
                if (integral && long.TryParse(literal, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out var whole))
                    result = new JValue(whole);
                else if (rawNumbers)
                    result = new JRaw(literal);
                else if (integral)
                    result = new JValue(BigInteger.Parse(literal, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture));
                else
                    result = new JValue(double.TryParse(literal, NumberStyles.Float, CultureInfo.InvariantCulture, out var number) ? number : literal.StartsWith("-", StringComparison.Ordinal) ? double.NegativeInfinity : double.PositiveInfinity);
            }

            return true;
        }
    }
}
