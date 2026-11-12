using System;
using System.Collections.Generic;

namespace TabForge.Protocol
{
    /// <summary>
    /// The KCP ARQ protocol (github.com/skywind3000/kcp) with its standard wire
    /// format: a straight port of protocol/realtime/internal/kcp (itself a port
    /// of ikcp.c 2.1.1 with IKCP_FASTACK_CONSERVE and default congestion
    /// control) without stream mode. Not thread-safe; the caller serialises
    /// Input, Send, Recv and Update.
    /// </summary>
    internal sealed class KcpCore
    {
        private const uint RtoNoDelay = 30, RtoMin = 100, RtoDefault = 200, RtoMax = 60000;
        private const byte CmdPush = 81, CmdAck = 82, CmdWask = 83, CmdWins = 84;
        private const uint AskSend = 1, AskTell = 2;
        private const uint WndSnd = 32, WndRcv = 128, MtuDefault = 1400, IntervalDefault = 100;
        /// <summary>Size of a segment header.</summary>
        public const int Overhead = 24;
        private const uint DeadLinkDefault = 20, ThreshInit = 2, ThreshMin = 2, ProbeInit = 7000, ProbeLimit = 120000;
        private const int FastackLimit = 5;

        private sealed class Segment
        {
            public uint Conv, Ts, Sn, Una, Resendts, Rto, Fastack, Xmit;
            public byte Cmd, Frg;
            public ushort Wnd;
            public byte[] Data;
        }

        private readonly uint conv;
        private uint mtu, mss, state;
        private uint sndUna, sndNxt, rcvNxt, ssthresh;
        private int rxRttval, rxSrtt, rxRto, rxMinrto;
        private uint sndWnd, rcvWnd, rmtWnd, cwnd, probe;
        private uint current, interval, tsFlush;
        private uint nodelay, updated, tsProbe, probeWait, deadLink, incr;
        private int fastresend, fastlimit, nocwnd;
        private readonly List<Segment> sndQueue = new List<Segment>(), rcvQueue = new List<Segment>(), sndBuf = new List<Segment>(), rcvBuf = new List<Segment>();
        private readonly List<uint> acklist = new List<uint>(); // sn, ts pairs
        private byte[] buffer;
        private int buffered;
        private readonly Action<byte[], int> output;

        /// <param name="output">Receives each datagram to send; the array is reused after it returns.</param>
        public KcpCore(uint conv, Action<byte[], int> output)
        {
            this.conv = conv;
            this.output = output;
            sndWnd = WndSnd;
            rcvWnd = rmtWnd = WndRcv;
            mtu = MtuDefault;
            mss = mtu - Overhead;
            rxRto = (int)RtoDefault;
            rxMinrto = (int)RtoMin;
            interval = tsFlush = IntervalDefault;
            ssthresh = ThreshInit;
            fastlimit = FastackLimit;
            deadLink = DeadLinkDefault;
            buffer = new byte[(mtu + Overhead) * 3];
        }

        private static int Diff(uint a, uint b) => (int)(a - b);

        /// <summary>A segment was retransmitted DeadLink times unacknowledged.</summary>
        public bool Dead => state != 0;

        public bool SetMtu(int value)
        {
            if (value < 50 || value < Overhead)
                return false;
            mtu = (uint)value;
            mss = mtu - Overhead;
            buffer = new byte[(mtu + Overhead) * 3];
            return true;
        }

        public int Mss => (int)mss;

        public void WndSize(int snd, int rcv)
        {
            if (snd > 0)
                sndWnd = (uint)snd;
            if (rcv > 0)
                rcvWnd = (uint)Math.Max(rcv, (int)WndRcv);
        }

        public void NoDelay(int nodelay, int interval, int resend, bool nc)
        {
            if (nodelay >= 0)
            {
                this.nodelay = (uint)nodelay;
                rxMinrto = (int)(nodelay != 0 ? RtoNoDelay : RtoMin);
            }
            if (interval >= 0)
                this.interval = (uint)Math.Min(Math.Max(interval, 10), 5000);
            if (resend >= 0)
                fastresend = resend;
            nocwnd = nc ? 1 : 0;
        }

        public void SetDeadLink(int n) => deadLink = (uint)n;

        /// <summary>Segments not yet acknowledged.</summary>
        public int WaitSnd => sndBuf.Count + sndQueue.Count;

        public int PeekSize()
        {
            if (rcvQueue.Count == 0)
                return -1;
            var seg = rcvQueue[0];
            if (seg.Frg == 0)
                return seg.Data.Length;
            if (rcvQueue.Count < seg.Frg + 1)
                return -1;
            var n = 0;
            foreach (var s in rcvQueue)
            {
                n += s.Data.Length;
                if (s.Frg == 0)
                    break;
            }
            return n;
        }

        /// <summary>The next complete message, or null.</summary>
        public byte[] Recv()
        {
            var size = PeekSize();
            if (size < 0)
                return null;
            var recover = (uint)rcvQueue.Count >= rcvWnd;
            var result = new byte[size];
            int n = 0, at = 0;
            foreach (var seg in rcvQueue)
            {
                Buffer.BlockCopy(seg.Data, 0, result, at, seg.Data.Length);
                at += seg.Data.Length;
                n++;
                if (seg.Frg == 0)
                    break;
            }
            rcvQueue.RemoveRange(0, n);
            MoveReceived();
            if ((uint)rcvQueue.Count < rcvWnd && recover)
                probe |= AskTell;
            return result;
        }

        private void MoveReceived()
        {
            var n = 0;
            foreach (var seg in rcvBuf)
            {
                if (seg.Sn != rcvNxt || (uint)rcvQueue.Count >= rcvWnd)
                    break;
                rcvQueue.Add(seg);
                rcvNxt++;
                n++;
            }
            rcvBuf.RemoveRange(0, n);
        }

        /// <summary>Queues a message; false if it needs WndRcv fragments or more.</summary>
        public bool Send(byte[] data, int offset, int length)
        {
            var count = length > mss ? (int)((length + mss - 1) / mss) : 1;
            if (count >= WndRcv)
                return false;
            for (var i = 0; i < count; i++)
            {
                var size = Math.Min(length, (int)mss);
                var copy = new byte[size];
                Buffer.BlockCopy(data, offset, copy, 0, size);
                sndQueue.Add(new Segment { Data = copy, Frg = (byte)(count - i - 1) });
                offset += size;
                length -= size;
            }
            return true;
        }

        private void UpdateAck(int rtt)
        {
            if (rxSrtt == 0)
            {
                rxSrtt = rtt;
                rxRttval = rtt / 2;
            }
            else
            {
                var delta = Math.Abs(rtt - rxSrtt);
                rxRttval = (3 * rxRttval + delta) / 4;
                rxSrtt = (7 * rxSrtt + rtt) / 8;
                if (rxSrtt < 1)
                    rxSrtt = 1;
            }
            var rto = rxSrtt + Math.Max((int)interval, 4 * rxRttval);
            rxRto = Math.Min(Math.Max(rxMinrto, rto), (int)RtoMax);
        }

        private void ShrinkBuf() => sndUna = sndBuf.Count > 0 ? sndBuf[0].Sn : sndNxt;

        private void ParseAck(uint sn)
        {
            if (Diff(sn, sndUna) < 0 || Diff(sn, sndNxt) >= 0)
                return;
            for (var i = 0; i < sndBuf.Count; i++)
            {
                if (sn == sndBuf[i].Sn)
                {
                    sndBuf.RemoveAt(i);
                    break;
                }
                if (Diff(sn, sndBuf[i].Sn) < 0)
                    break;
            }
        }

        private void ParseUna(uint una)
        {
            var n = 0;
            foreach (var seg in sndBuf)
            {
                if (Diff(una, seg.Sn) > 0)
                    n++;
                else
                    break;
            }
            sndBuf.RemoveRange(0, n);
        }

        private void ParseFastack(uint sn, uint ts)
        {
            if (Diff(sn, sndUna) < 0 || Diff(sn, sndNxt) >= 0)
                return;
            foreach (var seg in sndBuf)
            {
                if (Diff(sn, seg.Sn) < 0)
                    break;
                if (sn != seg.Sn && Diff(ts, seg.Ts) >= 0)
                    seg.Fastack++; // IKCP_FASTACK_CONSERVE
            }
        }

        private void ParseData(Segment newseg)
        {
            var sn = newseg.Sn;
            if (Diff(sn, rcvNxt + rcvWnd) >= 0 || Diff(sn, rcvNxt) < 0)
                return;
            var insert = rcvBuf.Count;
            for (var i = rcvBuf.Count - 1; i >= 0; i--)
            {
                if (rcvBuf[i].Sn == sn)
                    return; // duplicate
                if (Diff(sn, rcvBuf[i].Sn) > 0)
                    break;
                insert = i;
            }
            rcvBuf.Insert(insert, newseg);
            MoveReceived();
        }

        private static uint U32(byte[] b, int i) => (uint)(b[i] | b[i + 1] << 8 | b[i + 2] << 16 | b[i + 3] << 24);

        private static void PutU32(byte[] b, int i, uint v)
        {
            b[i] = (byte)v;
            b[i + 1] = (byte)(v >> 8);
            b[i + 2] = (byte)(v >> 16);
            b[i + 3] = (byte)(v >> 24);
        }

        /// <summary>Processes one datagram; false if it is malformed or for another conversation.</summary>
        public bool Input(byte[] data, int offset, int length)
        {
            var prevUna = sndUna;
            uint maxack = 0, latestTs = 0;
            var flag = false;
            if (length < Overhead)
                return false;
            while (length >= Overhead)
            {
                var c = U32(data, offset);
                if (c != conv)
                    return false;
                byte cmd = data[offset + 4], frg = data[offset + 5];
                var wnd = (ushort)(data[offset + 6] | data[offset + 7] << 8);
                uint ts = U32(data, offset + 8), sn = U32(data, offset + 12), una = U32(data, offset + 16), len = U32(data, offset + 20);
                offset += Overhead;
                length -= Overhead;
                if ((uint)length < len)
                    return false;
                if (cmd != CmdPush && cmd != CmdAck && cmd != CmdWask && cmd != CmdWins)
                    return false;
                rmtWnd = wnd;
                ParseUna(una);
                ShrinkBuf();
                switch (cmd)
                {
                    case CmdAck:
                        if (Diff(current, ts) >= 0)
                            UpdateAck(Diff(current, ts));
                        ParseAck(sn);
                        ShrinkBuf();
                        if (!flag)
                        {
                            flag = true;
                            maxack = sn;
                            latestTs = ts;
                        }
                        else if (Diff(sn, maxack) > 0 && Diff(ts, latestTs) > 0)
                        {
                            maxack = sn;
                            latestTs = ts;
                        }
                        break;
                    case CmdPush:
                        if (Diff(sn, rcvNxt + rcvWnd) < 0)
                        {
                            acklist.Add(sn);
                            acklist.Add(ts);
                            if (Diff(sn, rcvNxt) >= 0)
                            {
                                var copy = new byte[len];
                                Buffer.BlockCopy(data, offset, copy, 0, (int)len);
                                ParseData(new Segment { Conv = c, Cmd = cmd, Frg = frg, Wnd = wnd, Ts = ts, Sn = sn, Una = una, Data = copy });
                            }
                        }
                        break;
                    case CmdWask:
                        probe |= AskTell;
                        break;
                }
                offset += (int)len;
                length -= (int)len;
            }
            if (flag)
                ParseFastack(maxack, latestTs);
            if (Diff(sndUna, prevUna) > 0 && cwnd < rmtWnd)
            {
                if (cwnd < ssthresh)
                {
                    cwnd++;
                    incr += mss;
                }
                else
                {
                    if (incr < mss)
                        incr = mss;
                    incr += mss * mss / incr + mss / 16;
                    if ((cwnd + 1) * mss <= incr)
                        cwnd = (incr + mss - 1) / mss;
                }
                if (cwnd > rmtWnd)
                {
                    cwnd = rmtWnd;
                    incr = rmtWnd * mss;
                }
            }
            return true;
        }

        private ushort WndUnused() => (uint)rcvQueue.Count < rcvWnd ? (ushort)(rcvWnd - (uint)rcvQueue.Count) : (ushort)0;

        private void Encode(Segment seg)
        {
            var b = buffer;
            var i = buffered;
            PutU32(b, i, seg.Conv);
            b[i + 4] = seg.Cmd;
            b[i + 5] = seg.Frg;
            b[i + 6] = (byte)seg.Wnd;
            b[i + 7] = (byte)(seg.Wnd >> 8);
            PutU32(b, i + 8, seg.Ts);
            PutU32(b, i + 12, seg.Sn);
            PutU32(b, i + 16, seg.Una);
            var n = seg.Data?.Length ?? 0;
            PutU32(b, i + 20, (uint)n);
            if (n > 0)
                Buffer.BlockCopy(seg.Data, 0, b, i + Overhead, n);
            buffered = i + Overhead + n;
        }

        private void MakeSpace(int size)
        {
            if (buffered + size > mtu)
                FlushBuffer();
        }

        private void FlushBuffer()
        {
            if (buffered > 0)
            {
                output(buffer, buffered);
                buffered = 0;
            }
        }

        /// <summary>Sends pending ACKs, probes and due segments now.</summary>
        public void Flush()
        {
            if (updated == 0)
                return;
            var now = current;
            var seg = new Segment { Conv = conv, Cmd = CmdAck, Wnd = WndUnused(), Una = rcvNxt };
            for (var i = 0; i + 1 < acklist.Count; i += 2)
            {
                MakeSpace(Overhead);
                seg.Sn = acklist[i];
                seg.Ts = acklist[i + 1];
                Encode(seg);
            }
            acklist.Clear();

            if (rmtWnd == 0)
            {
                if (probeWait == 0)
                {
                    probeWait = ProbeInit;
                    tsProbe = now + probeWait;
                }
                else if (Diff(now, tsProbe) >= 0)
                {
                    if (probeWait < ProbeInit)
                        probeWait = ProbeInit;
                    probeWait += probeWait / 2;
                    if (probeWait > ProbeLimit)
                        probeWait = ProbeLimit;
                    tsProbe = now + probeWait;
                    probe |= AskSend;
                }
            }
            else
            {
                tsProbe = probeWait = 0;
            }
            seg.Sn = seg.Ts = 0;
            if ((probe & AskSend) != 0)
            {
                seg.Cmd = CmdWask;
                MakeSpace(Overhead);
                Encode(seg);
            }
            if ((probe & AskTell) != 0)
            {
                seg.Cmd = CmdWins;
                MakeSpace(Overhead);
                Encode(seg);
            }
            probe = 0;

            var window = Math.Min(sndWnd, rmtWnd);
            if (nocwnd == 0)
                window = Math.Min(cwnd, window);
            while (Diff(sndNxt, sndUna + window) < 0 && sndQueue.Count > 0)
            {
                var newseg = sndQueue[0];
                sndQueue.RemoveAt(0);
                newseg.Conv = conv;
                newseg.Cmd = CmdPush;
                newseg.Wnd = seg.Wnd;
                newseg.Ts = now;
                newseg.Sn = sndNxt;
                newseg.Una = rcvNxt;
                newseg.Resendts = now;
                newseg.Rto = (uint)rxRto;
                newseg.Fastack = newseg.Xmit = 0;
                sndNxt++;
                sndBuf.Add(newseg);
            }

            var resent = fastresend > 0 ? (uint)fastresend : 0xffffffff;
            var rtomin = nodelay == 0 ? (uint)(rxRto >> 3) : 0;
            bool change = false, lost = false;
            foreach (var segment in sndBuf)
            {
                var needsend = false;
                if (segment.Xmit == 0)
                {
                    needsend = true;
                    segment.Xmit++;
                    segment.Rto = (uint)rxRto;
                    segment.Resendts = now + segment.Rto + rtomin;
                }
                else if (Diff(now, segment.Resendts) >= 0)
                {
                    needsend = true;
                    segment.Xmit++;
                    if (nodelay == 0)
                        segment.Rto += Math.Max(segment.Rto, (uint)rxRto);
                    else
                        segment.Rto += (nodelay >= 2 ? (uint)rxRto : segment.Rto) / 2;
                    segment.Resendts = now + segment.Rto;
                    lost = true;
                }
                else if (segment.Fastack >= resent)
                {
                    if ((int)segment.Xmit <= fastlimit || fastlimit <= 0)
                    {
                        needsend = true;
                        segment.Xmit++;
                        segment.Fastack = 0;
                        segment.Resendts = now + segment.Rto;
                        change = true;
                    }
                }
                if (needsend)
                {
                    segment.Ts = now;
                    segment.Wnd = seg.Wnd;
                    segment.Una = rcvNxt;
                    MakeSpace(Overhead + segment.Data.Length);
                    Encode(segment);
                    if (segment.Xmit >= deadLink)
                        state = 0xffffffff;
                }
            }
            FlushBuffer();

            if (change)
            {
                var inflight = sndNxt - sndUna;
                ssthresh = Math.Max(inflight / 2, ThreshMin);
                cwnd = ssthresh + resent;
                incr = cwnd * mss;
            }
            if (lost)
            {
                ssthresh = Math.Max(window / 2, ThreshMin);
                cwnd = 1;
                incr = mss;
            }
            if (cwnd < 1)
            {
                cwnd = 1;
                incr = mss;
            }
        }

        /// <summary>Advances the clock (milliseconds, any origin) and flushes when the interval has passed.</summary>
        public void Update(uint now)
        {
            current = now;
            if (updated == 0)
            {
                updated = 1;
                tsFlush = now;
            }
            var slap = Diff(now, tsFlush);
            if (slap >= 10000 || slap < -10000)
            {
                tsFlush = now;
                slap = 0;
            }
            if (slap >= 0)
            {
                tsFlush += interval;
                if (Diff(now, tsFlush) >= 0)
                    tsFlush = now + interval;
                Flush();
            }
        }
    }
}
