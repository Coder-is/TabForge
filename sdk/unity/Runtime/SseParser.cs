using System;
using System.Text;

namespace TabForge.Protocol
{
    public sealed class SseFrame
    {
        public string Event, Id, Data;
    }

    public sealed class SseParser
    {
        private readonly Decoder decoder = new UTF8Encoding(false, true).GetDecoder();
        private readonly StringBuilder line = new StringBuilder(), data = new StringBuilder();
        private string name = "", id = "";
        private bool first = true, skipLf, hasData;
        private int size;
        private readonly int maximum;
        public SseParser(int maximumChars = 1048576)
        {
            if (maximumChars < 1)
                throw new ArgumentOutOfRangeException(nameof(maximumChars));
            maximum = maximumChars;
        }

        public void Feed(byte[] bytes, int count, Action<SseFrame> receive)
        {
            var chars = new char[count + 2];
            int length;
            try
            {
                length = decoder.GetChars(bytes, 0, count, chars, 0, false);
            }
            catch (DecoderFallbackException)
            {
                throw new ProtocolException("invalid_utf8", "Invalid UTF-8 stream");
            }

            for (var index = 0; index < length; index++)
            {
                var ch = chars[index];
                if (first)
                {
                    first = false;
                    if (ch == '\ufeff')
                        continue;
                }

                if (skipLf)
                {
                    skipLf = false;
                    if (ch == '\n')
                        continue;
                }

                if (++size > maximum)
                    throw new ProtocolException("frame_too_large", "SSE frame exceeds configured limit");
                if (ch == '\r' || ch == '\n')
                {
                    Consume(receive);
                    skipLf = ch == '\r';
                }
                else
                    line.Append(ch);
            }
        }

        private void Consume(Action<SseFrame> receive)
        {
            var text = line.ToString();
            line.Clear();
            if (text.Length == 0)
            {
                var frame = hasData ? new SseFrame
                {
                    Event = name.Length == 0 ? "message" : name,
                    Id = id,
                    Data = data.ToString(0, data.Length - 1)
                }

                : null;
                hasData = false;
                data.Clear();
                name = "";
                size = 0;
                if (frame != null)
                    receive(frame);
                return;
            }

            if (text[0] == ':')
                return;
            var colon = text.IndexOf(':');
            var field = colon < 0 ? text : text.Substring(0, colon);
            var value = colon < 0 ? "" : text.Substring(colon + 1);
            if (value.StartsWith(" ", StringComparison.Ordinal))
                value = value.Substring(1);
            if (field == "data")
            {
                data.Append(value).Append('\n');
                hasData = true;
            }
            else if (field == "event")
                name = value;
            else if (field == "id" && value.IndexOf('\0') < 0)
                id = value;
        }

        public void Finish()
        {
            try
            {
                decoder.GetChars(Array.Empty<byte>(), 0, 0, new char[2], 0, true);
            }
            catch (DecoderFallbackException)
            {
                throw new ProtocolException("invalid_utf8", "Truncated UTF-8 stream");
            }
        }
    }
}
