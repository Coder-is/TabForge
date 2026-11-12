extends RefCounted
## The KCP ARQ protocol (github.com/skywind3000/kcp) with its standard wire
## format: a straight port of protocol/realtime/internal/kcp (ikcp.c 2.1.1,
## IKCP_FASTACK_CONSERVE, default congestion control) without stream mode.
## GDScript integers are signed 64-bit, so every uint32 is kept masked.

const RTO_NODELAY := 30
const RTO_MIN := 100
const RTO_DEFAULT := 200
const RTO_MAX := 60000
const CMD_PUSH := 81
const CMD_ACK := 82
const CMD_WASK := 83
const CMD_WINS := 84
const ASK_SEND := 1
const ASK_TELL := 2
const WND_SND := 32
const WND_RCV := 128
const MTU_DEFAULT := 1400
const INTERVAL_DEFAULT := 100
const OVERHEAD := 24
const DEADLINK_DEFAULT := 20
const THRESH_INIT := 2
const THRESH_MIN := 2
const PROBE_INIT := 7000
const PROBE_LIMIT := 120000
const FASTACK_LIMIT := 5
const U32 := 0xFFFFFFFF

class Segment extends RefCounted:
	var conv := 0
	var cmd := 0
	var frg := 0
	var wnd := 0
	var ts := 0
	var sn := 0
	var una := 0
	var resendts := 0
	var rto := 0
	var fastack := 0
	var xmit := 0
	var data := PackedByteArray()

var conv := 0
var mtu := MTU_DEFAULT
var mss := MTU_DEFAULT - OVERHEAD
var state := 0
var snd_una := 0
var snd_nxt := 0
var rcv_nxt := 0
var ssthresh := THRESH_INIT
var rx_rttval := 0
var rx_srtt := 0
var rx_rto := RTO_DEFAULT
var rx_minrto := RTO_MIN
var snd_wnd := WND_SND
var rcv_wnd := WND_RCV
var rmt_wnd := WND_RCV
var cwnd := 0
var probe := 0
var current := 0
var interval := INTERVAL_DEFAULT
var ts_flush := INTERVAL_DEFAULT
var nodelay := 0
var updated := 0
var ts_probe := 0
var probe_wait := 0
var dead_link := DEADLINK_DEFAULT
var incr := 0
var fastresend := 0
var fastlimit := FASTACK_LIMIT
var nocwnd := 0
var snd_queue: Array = []
var rcv_queue: Array = []
var snd_buf: Array = []
var rcv_buf: Array = []
var acklist: Array = [] # sn, ts pairs
var buffer := PackedByteArray()
## Receives each datagram (PackedByteArray) to send.
var output: Callable

func _init(conversation: int, out: Callable) -> void:
	conv = conversation & U32
	output = out

## Signed difference of two uint32 values.
static func diff(a: int, b: int) -> int:
	var d := (a - b) & U32
	return d - 0x100000000 if d >= 0x80000000 else d

## A segment was retransmitted dead_link times unacknowledged.
func dead() -> bool:
	return state != 0

func set_mtu(value: int) -> bool:
	if value < 50 or value < OVERHEAD:
		return false
	mtu = value
	mss = mtu - OVERHEAD
	return true

func wnd_size(snd: int, rcv: int) -> void:
	if snd > 0: snd_wnd = snd
	if rcv > 0: rcv_wnd = maxi(rcv, WND_RCV)

func no_delay(nd: int, iv: int, resend: int, nc: bool) -> void:
	if nd >= 0:
		nodelay = nd
		rx_minrto = RTO_NODELAY if nd != 0 else RTO_MIN
	if iv >= 0:
		interval = mini(maxi(iv, 10), 5000)
	if resend >= 0:
		fastresend = resend
	nocwnd = 1 if nc else 0

func set_dead_link(n: int) -> void:
	dead_link = n

## Segments not yet acknowledged.
func wait_snd() -> int:
	return snd_buf.size() + snd_queue.size()

func peek_size() -> int:
	if rcv_queue.is_empty(): return -1
	var seg: Segment = rcv_queue[0]
	if seg.frg == 0: return seg.data.size()
	if rcv_queue.size() < seg.frg + 1: return -1
	var n := 0
	for s in rcv_queue:
		n += s.data.size()
		if s.frg == 0: break
	return n

## The next complete message, or an empty array with ok == false.
func recv() -> Array:
	if peek_size() < 0:
		return []
	var recover := rcv_queue.size() >= rcv_wnd
	var out := PackedByteArray()
	var n := 0
	for seg in rcv_queue:
		out.append_array(seg.data)
		n += 1
		if seg.frg == 0: break
	rcv_queue = rcv_queue.slice(n)
	_move_received()
	if rcv_queue.size() < rcv_wnd and recover:
		probe |= ASK_TELL
	return [out]

func _move_received() -> void:
	var n := 0
	for seg in rcv_buf:
		if seg.sn != rcv_nxt or rcv_queue.size() >= rcv_wnd: break
		rcv_queue.append(seg)
		rcv_nxt = (rcv_nxt + 1) & U32
		n += 1
	if n > 0:
		rcv_buf = rcv_buf.slice(n)

## Queues a message; false if it needs WND_RCV fragments or more.
func send(data: PackedByteArray) -> bool:
	var count := 1
	if data.size() > mss:
		count = (data.size() + mss - 1) / mss
	if count >= WND_RCV:
		return false
	var at := 0
	for i in count:
		var size := mini(data.size() - at, mss)
		var seg := Segment.new()
		seg.data = data.slice(at, at + size)
		seg.frg = count - i - 1
		snd_queue.append(seg)
		at += size
	return true

func _update_ack(rtt: int) -> void:
	if rx_srtt == 0:
		rx_srtt = rtt
		rx_rttval = rtt / 2
	else:
		var delta := absi(rtt - rx_srtt)
		rx_rttval = (3 * rx_rttval + delta) / 4
		rx_srtt = (7 * rx_srtt + rtt) / 8
		if rx_srtt < 1: rx_srtt = 1
	var rto := rx_srtt + maxi(interval, 4 * rx_rttval)
	rx_rto = mini(maxi(rx_minrto, rto), RTO_MAX)

func _shrink_buf() -> void:
	snd_una = snd_buf[0].sn if not snd_buf.is_empty() else snd_nxt

func _parse_ack(sn: int) -> void:
	if diff(sn, snd_una) < 0 or diff(sn, snd_nxt) >= 0: return
	for i in snd_buf.size():
		var seg: Segment = snd_buf[i]
		if sn == seg.sn:
			snd_buf.remove_at(i)
			break
		if diff(sn, seg.sn) < 0: break

func _parse_una(una: int) -> void:
	var n := 0
	for seg in snd_buf:
		if diff(una, seg.sn) > 0: n += 1
		else: break
	if n > 0:
		snd_buf = snd_buf.slice(n)

func _parse_fastack(sn: int, ts: int) -> void:
	if diff(sn, snd_una) < 0 or diff(sn, snd_nxt) >= 0: return
	for seg in snd_buf:
		if diff(sn, seg.sn) < 0: break
		if sn != seg.sn and diff(ts, seg.ts) >= 0:
			seg.fastack += 1 # IKCP_FASTACK_CONSERVE

func _parse_data(newseg: Segment) -> void:
	var sn := newseg.sn
	if diff(sn, (rcv_nxt + rcv_wnd) & U32) >= 0 or diff(sn, rcv_nxt) < 0: return
	var insert := rcv_buf.size()
	var i := rcv_buf.size() - 1
	while i >= 0:
		var seg: Segment = rcv_buf[i]
		if seg.sn == sn: return # duplicate
		if diff(sn, seg.sn) > 0: break
		insert = i
		i -= 1
	rcv_buf.insert(insert, newseg)
	_move_received()

## Processes one datagram; false if it is malformed or for another conversation.
func input(data: PackedByteArray) -> bool:
	var prev_una := snd_una
	var maxack := 0
	var latest_ts := 0
	var flag := false
	var size := data.size()
	if size < OVERHEAD: return false
	var at := 0
	while size - at >= OVERHEAD:
		if data.decode_u32(at) != conv: return false
		var cmd := data[at + 4]
		var frg := data[at + 5]
		var wnd := data.decode_u16(at + 6)
		var ts := data.decode_u32(at + 8)
		var sn := data.decode_u32(at + 12)
		var una := data.decode_u32(at + 16)
		var length := data.decode_u32(at + 20)
		at += OVERHEAD
		if size - at < length: return false
		if cmd != CMD_PUSH and cmd != CMD_ACK and cmd != CMD_WASK and cmd != CMD_WINS: return false
		rmt_wnd = wnd
		_parse_una(una)
		_shrink_buf()
		match cmd:
			CMD_ACK:
				if diff(current, ts) >= 0:
					_update_ack(diff(current, ts))
				_parse_ack(sn)
				_shrink_buf()
				if not flag:
					flag = true
					maxack = sn
					latest_ts = ts
				elif diff(sn, maxack) > 0 and diff(ts, latest_ts) > 0:
					maxack = sn
					latest_ts = ts
			CMD_PUSH:
				if diff(sn, (rcv_nxt + rcv_wnd) & U32) < 0:
					acklist.append(sn)
					acklist.append(ts)
					if diff(sn, rcv_nxt) >= 0:
						var seg := Segment.new()
						seg.conv = conv
						seg.cmd = cmd
						seg.frg = frg
						seg.wnd = wnd
						seg.ts = ts
						seg.sn = sn
						seg.una = una
						seg.data = data.slice(at, at + length)
						_parse_data(seg)
			CMD_WASK:
				probe |= ASK_TELL
		at += length
	if flag:
		_parse_fastack(maxack, latest_ts)
	if diff(snd_una, prev_una) > 0 and cwnd < rmt_wnd:
		if cwnd < ssthresh:
			cwnd += 1
			incr += mss
		else:
			if incr < mss: incr = mss
			incr += mss * mss / incr + mss / 16
			if (cwnd + 1) * mss <= incr:
				cwnd = (incr + mss - 1) / mss
		if cwnd > rmt_wnd:
			cwnd = rmt_wnd
			incr = rmt_wnd * mss
	return true

func _wnd_unused() -> int:
	return rcv_wnd - rcv_queue.size() if rcv_queue.size() < rcv_wnd else 0

func _encode(seg: Segment) -> void:
	var i := buffer.size()
	buffer.resize(i + OVERHEAD)
	buffer.encode_u32(i, seg.conv)
	buffer[i + 4] = seg.cmd
	buffer[i + 5] = seg.frg
	buffer.encode_u16(i + 6, seg.wnd)
	buffer.encode_u32(i + 8, seg.ts)
	buffer.encode_u32(i + 12, seg.sn)
	buffer.encode_u32(i + 16, seg.una)
	buffer.encode_u32(i + 20, seg.data.size())
	buffer.append_array(seg.data)

func _make_space(size: int) -> void:
	if buffer.size() + size > mtu:
		_flush_buffer()

func _flush_buffer() -> void:
	if not buffer.is_empty():
		output.call(buffer)
		buffer = PackedByteArray()

## Sends pending ACKs, probes and due segments now.
func flush() -> void:
	if updated == 0: return
	var now := current
	var seg := Segment.new()
	seg.conv = conv
	seg.cmd = CMD_ACK
	seg.wnd = _wnd_unused()
	seg.una = rcv_nxt
	var i := 0
	while i + 1 < acklist.size():
		_make_space(OVERHEAD)
		seg.sn = acklist[i]
		seg.ts = acklist[i + 1]
		_encode(seg)
		i += 2
	acklist.clear()

	if rmt_wnd == 0:
		if probe_wait == 0:
			probe_wait = PROBE_INIT
			ts_probe = (now + probe_wait) & U32
		elif diff(now, ts_probe) >= 0:
			if probe_wait < PROBE_INIT: probe_wait = PROBE_INIT
			probe_wait += probe_wait / 2
			if probe_wait > PROBE_LIMIT: probe_wait = PROBE_LIMIT
			ts_probe = (now + probe_wait) & U32
			probe |= ASK_SEND
	else:
		ts_probe = 0
		probe_wait = 0
	seg.sn = 0
	seg.ts = 0
	if probe & ASK_SEND:
		seg.cmd = CMD_WASK
		_make_space(OVERHEAD)
		_encode(seg)
	if probe & ASK_TELL:
		seg.cmd = CMD_WINS
		_make_space(OVERHEAD)
		_encode(seg)
	probe = 0

	var window := mini(snd_wnd, rmt_wnd)
	if nocwnd == 0:
		window = mini(cwnd, window)
	while diff(snd_nxt, (snd_una + window) & U32) < 0 and not snd_queue.is_empty():
		var newseg: Segment = snd_queue.pop_front()
		newseg.conv = conv
		newseg.cmd = CMD_PUSH
		newseg.wnd = seg.wnd
		newseg.ts = now
		newseg.sn = snd_nxt
		newseg.una = rcv_nxt
		newseg.resendts = now
		newseg.rto = rx_rto
		newseg.fastack = 0
		newseg.xmit = 0
		snd_nxt = (snd_nxt + 1) & U32
		snd_buf.append(newseg)

	var resent := fastresend if fastresend > 0 else U32
	var rtomin := (rx_rto >> 3) if nodelay == 0 else 0
	var change := false
	var lost := false
	for segment in snd_buf:
		var needsend := false
		if segment.xmit == 0:
			needsend = true
			segment.xmit += 1
			segment.rto = rx_rto
			segment.resendts = (now + segment.rto + rtomin) & U32
		elif diff(now, segment.resendts) >= 0:
			needsend = true
			segment.xmit += 1
			if nodelay == 0:
				segment.rto += maxi(segment.rto, rx_rto)
			else:
				segment.rto += (rx_rto if nodelay >= 2 else segment.rto) / 2
			segment.resendts = (now + segment.rto) & U32
			lost = true
		elif segment.fastack >= resent:
			if segment.xmit <= fastlimit or fastlimit <= 0:
				needsend = true
				segment.xmit += 1
				segment.fastack = 0
				segment.resendts = (now + segment.rto) & U32
				change = true
		if needsend:
			segment.ts = now
			segment.wnd = seg.wnd
			segment.una = rcv_nxt
			_make_space(OVERHEAD + segment.data.size())
			_encode(segment)
			if segment.xmit >= dead_link:
				state = U32
	_flush_buffer()

	if change:
		var inflight := (snd_nxt - snd_una) & U32
		ssthresh = maxi(inflight / 2, THRESH_MIN)
		cwnd = ssthresh + resent
		incr = cwnd * mss
	if lost:
		ssthresh = maxi(window / 2, THRESH_MIN)
		cwnd = 1
		incr = mss
	if cwnd < 1:
		cwnd = 1
		incr = mss

## Advances the clock (milliseconds, any origin) and flushes when the interval has passed.
func update(now: int) -> void:
	current = now & U32
	if updated == 0:
		updated = 1
		ts_flush = current
	var slap := diff(current, ts_flush)
	if slap >= 10000 or slap < -10000:
		ts_flush = current
		slap = 0
	if slap >= 0:
		ts_flush = (ts_flush + interval) & U32
		if diff(current, ts_flush) >= 0:
			ts_flush = (current + interval) & U32
		flush()
