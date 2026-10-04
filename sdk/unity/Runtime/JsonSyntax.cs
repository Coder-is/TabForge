using System;

namespace TabForge.Protocol
{
    // Json.NET accepts JavaScript extensions; reject those before loading tokens.
    internal sealed class JsonSyntax
    {
        private readonly string text;
        private int pos, nodes;
        public JsonSyntax(string text)
        {
            this.text = text ?? "";
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

        private bool String()
        {
            if (!Take('"'))
                return false;
            while (pos < text.Length)
            {
                var ch = text[pos++];
                if (ch == '"')
                    return true;
                if (ch < 32)
                    return false;
                if (ch == '\\')
                {
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
                        }
                        else if (code >= 0xdc00 && code <= 0xdfff)
                            return false;
                    }
                    else if ("\"\\/bfnrt".IndexOf(escape) < 0)
                        return false;
                }
                else if (char.IsHighSurrogate(ch))
                {
                    if (pos == text.Length || !char.IsLowSurrogate(text[pos++]))
                        return false;
                }
                else if (char.IsLowSurrogate(ch))
                    return false;
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

        private bool Value(int depth)
        {
            Space();
            if (depth > 64 || ++nodes > 100000 || pos == text.Length)
                return false;
            if (text[pos] == '"')
                return String();
            if (Take('{'))
            {
                if (Take('}'))
                    return true;
                do
                {
                    if (!String() || !Take(':') || !Value(depth + 1))
                        return false;
                    if (Take('}'))
                        return true;
                }
                while (Take(','));
                return false;
            }

            if (Take('['))
            {
                if (Take(']'))
                    return true;
                do
                {
                    if (!Value(depth + 1))
                        return false;
                    if (Take(']'))
                        return true;
                }
                while (Take(','));
                return false;
            }

            foreach (var word in new[]
            {
                "true",
                "false",
                "null"
            }

            )
                if (pos + word.Length <= text.Length && string.CompareOrdinal(text, pos, word, 0, word.Length) == 0)
                {
                    pos += word.Length;
                    return true;
                }

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
                if (!Digits())
                    return false;
            }

            if (pos < text.Length && (text[pos] == 'e' || text[pos] == 'E'))
            {
                pos++;
                if (pos < text.Length && (text[pos] == '+' || text[pos] == '-'))
                    pos++;
                if (!Digits())
                    return false;
            }

            return true;
        }

        public bool Valid()
        {
            if (!Value(0))
                return false;
            Space();
            return pos == text.Length;
        }
    }
}
