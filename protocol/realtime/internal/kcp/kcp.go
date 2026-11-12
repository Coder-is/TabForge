// Package kcp implements the KCP ARQ protocol (github.com/skywind3000/kcp)
// with its standard wire format, so peers can use any conforming KCP
// library. It is a straight port of ikcp.c 2.1.1 (IKCP_FASTACK_CONSERVE,
// default congestion control) without stream mode, FEC or encryption.
//
// A KCP is not safe for concurrent use; the caller serialises Input, Send,
// Recv and Update.
package kcp

import "encoding/binary"

const (
	rtoNoDelay  = 30
	rtoMin      = 100
	rtoDefault  = 200
	rtoMax      = 60000
	cmdPush     = 81
	cmdAck      = 82
	cmdWask     = 83
	cmdWins     = 84
	askSend     = 1
	askTell     = 2
	wndSnd      = 32
	wndRcv      = 128
	mtuDefault  = 1400
	intervalDef = 100
	// Overhead is the size of a segment header.
	Overhead     = 24
	deadLinkDef  = 20
	threshInit   = 2
	threshMin    = 2
	probeInit    = 7000
	probeLimit   = 120000
	fastackLimit = 5
)

// MaxFragments bounds the segments of one message (ikcp rejects a message
// of IKCP_WND_RCV fragments or more).
const MaxFragments = wndRcv - 1

type segment struct {
	conv, ts, sn, una, resendts, rto, fastack, xmit uint32
	cmd, frg                                        uint8
	wnd                                             uint16
	data                                            []byte
}

// KCP is one end of a KCP conversation.
type KCP struct {
	conv, mtu, mss, state               uint32
	sndUna, sndNxt, rcvNxt              uint32
	ssthresh                            uint32
	rxRttval, rxSrtt, rxRto, rxMinrto   int32
	sndWnd, rcvWnd, rmtWnd, cwnd, probe uint32
	current, interval, tsFlush, xmit    uint32
	nodelay, updated                    uint32
	tsProbe, probeWait, deadLink, incr  uint32
	fastresend, fastlimit               int32
	nocwnd                              int32
	sndQueue, rcvQueue, sndBuf, rcvBuf  []*segment
	acklist                             []uint32 // sn, ts pairs
	buffer                              []byte
	output                              func([]byte)
}

// New creates a KCP for conv. output receives each datagram to send; the
// slice is reused after output returns.
func New(conv uint32, output func([]byte)) *KCP {
	k := &KCP{
		conv: conv, sndWnd: wndSnd, rcvWnd: wndRcv, rmtWnd: wndRcv, mtu: mtuDefault,
		rxRto: rtoDefault, rxMinrto: rtoMin, interval: intervalDef, tsFlush: intervalDef,
		ssthresh: threshInit, fastlimit: fastackLimit, deadLink: deadLinkDef, output: output,
	}
	k.mss = k.mtu - Overhead
	k.buffer = make([]byte, 0, (k.mtu+Overhead)*3)
	return k
}

func diff(a, b uint32) int32 { return int32(a - b) }

// Dead reports that a segment was retransmitted DeadLink times unacknowledged.
func (k *KCP) Dead() bool { return k.state != 0 }

// SetMTU sets the largest datagram KCP emits (default 1400).
func (k *KCP) SetMTU(mtu int) bool {
	if mtu < 50 || mtu < Overhead {
		return false
	}
	k.mtu = uint32(mtu)
	k.mss = k.mtu - Overhead
	k.buffer = make([]byte, 0, (k.mtu+Overhead)*3)
	return true
}

// MSS is the payload capacity of one segment.
func (k *KCP) MSS() int { return int(k.mss) }

// WndSize sets the send and receive windows in segments.
func (k *KCP) WndSize(snd, rcv int) {
	if snd > 0 {
		k.sndWnd = uint32(snd)
	}
	if rcv > 0 {
		k.rcvWnd = uint32(max(rcv, wndRcv))
	}
}

// NoDelay configures ikcp_nodelay: nodelay 1 lowers the minimum RTO,
// interval is the flush period in ms, resend enables fast retransmit after
// that many skipping ACKs, and nc disables congestion control.
func (k *KCP) NoDelay(nodelay, interval, resend int, nc bool) {
	if nodelay >= 0 {
		k.nodelay = uint32(nodelay)
		if nodelay != 0 {
			k.rxMinrto = rtoNoDelay
		} else {
			k.rxMinrto = rtoMin
		}
	}
	if interval >= 0 {
		k.interval = uint32(min(max(interval, 10), 5000))
	}
	if resend >= 0 {
		k.fastresend = int32(resend)
	}
	if nc {
		k.nocwnd = 1
	} else {
		k.nocwnd = 0
	}
}

// SetDeadLink sets how many retransmissions of one segment mark the link dead.
func (k *KCP) SetDeadLink(n int) { k.deadLink = uint32(n) }

// WaitSnd is the number of segments not yet acknowledged.
func (k *KCP) WaitSnd() int { return len(k.sndBuf) + len(k.sndQueue) }

// PeekSize returns the size of the next complete message, or -1.
func (k *KCP) PeekSize() int {
	if len(k.rcvQueue) == 0 {
		return -1
	}
	seg := k.rcvQueue[0]
	if seg.frg == 0 {
		return len(seg.data)
	}
	if len(k.rcvQueue) < int(seg.frg)+1 {
		return -1
	}
	n := 0
	for _, s := range k.rcvQueue {
		n += len(s.data)
		if s.frg == 0 {
			break
		}
	}
	return n
}

// Recv returns the next complete message, or nil if none is ready.
func (k *KCP) Recv() []byte {
	size := k.PeekSize()
	if size < 0 {
		return nil
	}
	recover := uint32(len(k.rcvQueue)) >= k.rcvWnd
	out := make([]byte, 0, size)
	n := 0
	for _, seg := range k.rcvQueue {
		out = append(out, seg.data...)
		n++
		if seg.frg == 0 {
			break
		}
	}
	k.rcvQueue = k.rcvQueue[n:]
	k.moveReceived()
	if uint32(len(k.rcvQueue)) < k.rcvWnd && recover {
		k.probe |= askTell
	}
	return out
}

// moveReceived moves in-order segments from rcvBuf to rcvQueue.
func (k *KCP) moveReceived() {
	n := 0
	for _, seg := range k.rcvBuf {
		if seg.sn != k.rcvNxt || uint32(len(k.rcvQueue)) >= k.rcvWnd {
			break
		}
		k.rcvQueue = append(k.rcvQueue, seg)
		k.rcvNxt++
		n++
	}
	k.rcvBuf = k.rcvBuf[n:]
}

// Send queues a message. It returns false if the message needs too many
// fragments (more than MaxFragments segments).
func (k *KCP) Send(data []byte) bool {
	count := 1
	if len(data) > int(k.mss) {
		count = (len(data) + int(k.mss) - 1) / int(k.mss)
	}
	if count >= wndRcv {
		return false
	}
	for i := 0; i < count; i++ {
		size := min(len(data), int(k.mss))
		seg := &segment{data: append([]byte(nil), data[:size]...), frg: uint8(count - i - 1)}
		k.sndQueue = append(k.sndQueue, seg)
		data = data[size:]
	}
	return true
}

func (k *KCP) updateAck(rtt int32) {
	if k.rxSrtt == 0 {
		k.rxSrtt = rtt
		k.rxRttval = rtt / 2
	} else {
		delta := rtt - k.rxSrtt
		if delta < 0 {
			delta = -delta
		}
		k.rxRttval = (3*k.rxRttval + delta) / 4
		k.rxSrtt = (7*k.rxSrtt + rtt) / 8
		if k.rxSrtt < 1 {
			k.rxSrtt = 1
		}
	}
	rto := k.rxSrtt + max(int32(k.interval), 4*k.rxRttval)
	k.rxRto = min(max(k.rxMinrto, rto), rtoMax)
}

func (k *KCP) shrinkBuf() {
	if len(k.sndBuf) > 0 {
		k.sndUna = k.sndBuf[0].sn
	} else {
		k.sndUna = k.sndNxt
	}
}

func (k *KCP) parseAck(sn uint32) {
	if diff(sn, k.sndUna) < 0 || diff(sn, k.sndNxt) >= 0 {
		return
	}
	for i, seg := range k.sndBuf {
		if sn == seg.sn {
			k.sndBuf = append(k.sndBuf[:i], k.sndBuf[i+1:]...)
			break
		}
		if diff(sn, seg.sn) < 0 {
			break
		}
	}
}

func (k *KCP) parseUna(una uint32) {
	n := 0
	for _, seg := range k.sndBuf {
		if diff(una, seg.sn) > 0 {
			n++
		} else {
			break
		}
	}
	k.sndBuf = k.sndBuf[n:]
}

func (k *KCP) parseFastack(sn, ts uint32) {
	if diff(sn, k.sndUna) < 0 || diff(sn, k.sndNxt) >= 0 {
		return
	}
	for _, seg := range k.sndBuf {
		if diff(sn, seg.sn) < 0 {
			break
		} else if sn != seg.sn && diff(ts, seg.ts) >= 0 {
			// IKCP_FASTACK_CONSERVE (upstream default): only ACKs sent after
			// this segment was last transmitted count towards fast resend.
			seg.fastack++
		}
	}
}

func (k *KCP) parseData(newseg *segment) {
	sn := newseg.sn
	if diff(sn, k.rcvNxt+k.rcvWnd) >= 0 || diff(sn, k.rcvNxt) < 0 {
		return
	}
	insert := len(k.rcvBuf)
	for i := len(k.rcvBuf) - 1; i >= 0; i-- {
		seg := k.rcvBuf[i]
		if seg.sn == sn {
			return // duplicate
		}
		if diff(sn, seg.sn) > 0 {
			break
		}
		insert = i
	}
	k.rcvBuf = append(k.rcvBuf, nil)
	copy(k.rcvBuf[insert+1:], k.rcvBuf[insert:])
	k.rcvBuf[insert] = newseg
	k.moveReceived()
}

// Input processes one received datagram. It returns false if the datagram
// is malformed or belongs to another conversation.
func (k *KCP) Input(data []byte) bool {
	prevUna := k.sndUna
	var maxack, latestTs uint32
	flag := false
	if len(data) < Overhead {
		return false
	}
	for len(data) >= Overhead {
		conv := binary.LittleEndian.Uint32(data)
		if conv != k.conv {
			return false
		}
		cmd, frg := data[4], data[5]
		wnd := binary.LittleEndian.Uint16(data[6:])
		ts := binary.LittleEndian.Uint32(data[8:])
		sn := binary.LittleEndian.Uint32(data[12:])
		una := binary.LittleEndian.Uint32(data[16:])
		length := binary.LittleEndian.Uint32(data[20:])
		data = data[Overhead:]
		if uint32(len(data)) < length {
			return false
		}
		if cmd != cmdPush && cmd != cmdAck && cmd != cmdWask && cmd != cmdWins {
			return false
		}
		k.rmtWnd = uint32(wnd)
		k.parseUna(una)
		k.shrinkBuf()
		switch cmd {
		case cmdAck:
			if diff(k.current, ts) >= 0 {
				k.updateAck(diff(k.current, ts))
			}
			k.parseAck(sn)
			k.shrinkBuf()
			if !flag {
				flag, maxack, latestTs = true, sn, ts
			} else if diff(sn, maxack) > 0 && diff(ts, latestTs) > 0 {
				maxack, latestTs = sn, ts
			}
		case cmdPush:
			if diff(sn, k.rcvNxt+k.rcvWnd) < 0 {
				k.acklist = append(k.acklist, sn, ts)
				if diff(sn, k.rcvNxt) >= 0 {
					k.parseData(&segment{conv: conv, cmd: cmd, frg: frg, wnd: wnd, ts: ts, sn: sn, una: una, data: append([]byte(nil), data[:length]...)})
				}
			}
		case cmdWask:
			k.probe |= askTell
		}
		data = data[length:]
	}
	if flag {
		k.parseFastack(maxack, latestTs)
	}
	if diff(k.sndUna, prevUna) > 0 && k.cwnd < k.rmtWnd {
		mss := k.mss
		if k.cwnd < k.ssthresh {
			k.cwnd++
			k.incr += mss
		} else {
			if k.incr < mss {
				k.incr = mss
			}
			k.incr += (mss*mss)/k.incr + mss/16
			if (k.cwnd+1)*mss <= k.incr {
				k.cwnd = (k.incr + mss - 1) / mss
			}
		}
		if k.cwnd > k.rmtWnd {
			k.cwnd = k.rmtWnd
			k.incr = k.rmtWnd * mss
		}
	}
	return true
}

func (k *KCP) wndUnused() uint16 {
	if uint32(len(k.rcvQueue)) < k.rcvWnd {
		return uint16(k.rcvWnd - uint32(len(k.rcvQueue)))
	}
	return 0
}

func (k *KCP) encode(seg *segment) {
	var h [Overhead]byte
	binary.LittleEndian.PutUint32(h[0:], seg.conv)
	h[4], h[5] = seg.cmd, seg.frg
	binary.LittleEndian.PutUint16(h[6:], seg.wnd)
	binary.LittleEndian.PutUint32(h[8:], seg.ts)
	binary.LittleEndian.PutUint32(h[12:], seg.sn)
	binary.LittleEndian.PutUint32(h[16:], seg.una)
	binary.LittleEndian.PutUint32(h[20:], uint32(len(seg.data)))
	k.buffer = append(k.buffer, h[:]...)
	k.buffer = append(k.buffer, seg.data...)
}

func (k *KCP) makeSpace(size int) {
	if len(k.buffer)+size > int(k.mtu) {
		k.flushBuffer()
	}
}

func (k *KCP) flushBuffer() {
	if len(k.buffer) > 0 {
		k.output(k.buffer)
		k.buffer = k.buffer[:0]
	}
}

// Flush sends pending ACKs, probes and due segments now.
func (k *KCP) Flush() {
	if k.updated == 0 {
		return
	}
	current := k.current
	seg := &segment{conv: k.conv, cmd: cmdAck, wnd: k.wndUnused(), una: k.rcvNxt}
	for i := 0; i+1 < len(k.acklist); i += 2 {
		k.makeSpace(Overhead)
		seg.sn, seg.ts = k.acklist[i], k.acklist[i+1]
		k.encode(seg)
	}
	k.acklist = k.acklist[:0]

	if k.rmtWnd == 0 {
		if k.probeWait == 0 {
			k.probeWait = probeInit
			k.tsProbe = current + k.probeWait
		} else if diff(current, k.tsProbe) >= 0 {
			if k.probeWait < probeInit {
				k.probeWait = probeInit
			}
			k.probeWait += k.probeWait / 2
			if k.probeWait > probeLimit {
				k.probeWait = probeLimit
			}
			k.tsProbe = current + k.probeWait
			k.probe |= askSend
		}
	} else {
		k.tsProbe, k.probeWait = 0, 0
	}
	seg.sn, seg.ts = 0, 0
	if k.probe&askSend != 0 {
		seg.cmd = cmdWask
		k.makeSpace(Overhead)
		k.encode(seg)
	}
	if k.probe&askTell != 0 {
		seg.cmd = cmdWins
		k.makeSpace(Overhead)
		k.encode(seg)
	}
	k.probe = 0

	cwnd := min(k.sndWnd, k.rmtWnd)
	if k.nocwnd == 0 {
		cwnd = min(k.cwnd, cwnd)
	}
	for diff(k.sndNxt, k.sndUna+cwnd) < 0 && len(k.sndQueue) > 0 {
		newseg := k.sndQueue[0]
		k.sndQueue = k.sndQueue[1:]
		newseg.conv, newseg.cmd, newseg.wnd = k.conv, cmdPush, seg.wnd
		newseg.ts, newseg.sn, newseg.una = current, k.sndNxt, k.rcvNxt
		newseg.resendts, newseg.rto, newseg.fastack, newseg.xmit = current, uint32(k.rxRto), 0, 0
		k.sndNxt++
		k.sndBuf = append(k.sndBuf, newseg)
	}

	resent := uint32(0xffffffff)
	if k.fastresend > 0 {
		resent = uint32(k.fastresend)
	}
	rtomin := uint32(0)
	if k.nodelay == 0 {
		rtomin = uint32(k.rxRto >> 3)
	}
	change, lost := false, false
	for _, segment := range k.sndBuf {
		needsend := false
		if segment.xmit == 0 {
			needsend = true
			segment.xmit++
			segment.rto = uint32(k.rxRto)
			segment.resendts = current + segment.rto + rtomin
		} else if diff(current, segment.resendts) >= 0 {
			needsend = true
			segment.xmit++
			k.xmit++
			if k.nodelay == 0 {
				segment.rto += max(segment.rto, uint32(k.rxRto))
			} else {
				step := segment.rto
				if k.nodelay >= 2 {
					step = uint32(k.rxRto)
				}
				segment.rto += step / 2
			}
			segment.resendts = current + segment.rto
			lost = true
		} else if segment.fastack >= resent {
			if int32(segment.xmit) <= k.fastlimit || k.fastlimit <= 0 {
				needsend = true
				segment.xmit++
				segment.fastack = 0
				segment.resendts = current + segment.rto
				change = true
			}
		}
		if needsend {
			segment.ts, segment.wnd, segment.una = current, seg.wnd, k.rcvNxt
			k.makeSpace(Overhead + len(segment.data))
			k.encode(segment)
			if segment.xmit >= k.deadLink {
				k.state = 0xffffffff
			}
		}
	}
	k.flushBuffer()

	if change {
		inflight := k.sndNxt - k.sndUna
		k.ssthresh = max(inflight/2, threshMin)
		k.cwnd = k.ssthresh + resent
		k.incr = k.cwnd * k.mss
	}
	if lost {
		k.ssthresh = max(cwnd/2, threshMin)
		k.cwnd = 1
		k.incr = k.mss
	}
	if k.cwnd < 1 {
		k.cwnd = 1
		k.incr = k.mss
	}
}

// Update advances the clock (milliseconds, any origin) and flushes when the
// interval has passed. Call it every few milliseconds or after Check.
func (k *KCP) Update(current uint32) {
	k.current = current
	if k.updated == 0 {
		k.updated = 1
		k.tsFlush = current
	}
	slap := diff(current, k.tsFlush)
	if slap >= 10000 || slap < -10000 {
		k.tsFlush = current
		slap = 0
	}
	if slap >= 0 {
		k.tsFlush += k.interval
		if diff(current, k.tsFlush) >= 0 {
			k.tsFlush = current + k.interval
		}
		k.Flush()
	}
}

// Check returns when Update should next be called.
func (k *KCP) Check(current uint32) uint32 {
	if k.updated == 0 {
		return current
	}
	tsFlush := k.tsFlush
	if diff(current, tsFlush) >= 10000 || diff(current, tsFlush) < -10000 {
		tsFlush = current
	}
	if diff(current, tsFlush) >= 0 {
		return current
	}
	tmFlush := diff(tsFlush, current)
	tmPacket := int32(0x7fffffff)
	for _, seg := range k.sndBuf {
		d := diff(seg.resendts, current)
		if d <= 0 {
			return current
		}
		tmPacket = min(tmPacket, d)
	}
	minimal := min(tmPacket, tmFlush, int32(k.interval))
	return current + uint32(minimal)
}

// Conv reads the conversation id of a datagram.
func Conv(data []byte) (uint32, bool) {
	if len(data) < Overhead {
		return 0, false
	}
	return binary.LittleEndian.Uint32(data), true
}
