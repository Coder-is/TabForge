extends RefCounted
## Realtime socket over KCP on UDP (kcp://host:port/path), for the Go
## server's ServeKCP: the same frames as WebSocket without TCP's
## head-of-line blocking on lossy mobile networks. With a server key it runs
## encrypted KCP, otherwise clear text. Desktop and mobile exports; the Web
## has no UDP. Polled from the client's tick like PeerSocket; no threads.

const Core = preload("kcp_core.gd")
const KcpCrypto = preload("kcp_crypto.gd")

const TYPE_OPEN := 0
const TYPE_BINARY := 1
const TYPE_TEXT := 2
const TYPE_CLOSE := 3
const MORE := 0x80
const CHUNK := 16 << 10
const MTU := 1200
const WINDOW := 256
const INTERVAL_MS := 10
const DEAD_LINK := 40
const QUIET_MS := 1000
const HELLO_RETRY_MS := 250
const HANDSHAKE_RESETS := 3
const HANDSHAKE_MS := 10000
const CLOSE_LINGER_MS := 1000
const MAX_MESSAGE := 1 << 20

var _server_key: PackedByteArray
var _udp := PacketPeerUDP.new()
var _conv := 0
var _path := ""
var _subprotocol := ""
var _kcp: Core
var _cipher: Variant = null
var _handshake: Variant = null
var _handshaking := false
## X25519 takes tens of milliseconds in GDScript: the handshake runs it on
## WorkerThreadPool so the game does not stall on every reconnect.
var _task := -1
var _task_result: Array = []
var _hello_at := 0
var _handshake_deadline := 0
var _resets := 0
var _opened := false
var _closing := false
var _closed := false
var _close_deadline := 0
var _last_recv := 0
var _last_alive := 0
var _inbox: Array = []
var _partial := PackedByteArray()
var _partial_type := -1
var _close_reason := ""
var _refusal := ""

## server_key: the server's 32-byte X25519 public key for encrypted KCP, or empty.
func _init(server_key: PackedByteArray = PackedByteArray()) -> void:
	_server_key = server_key

func open(url: String, protocol: String) -> Error:
	var m := RegEx.create_from_string("^kcp://(\\[[^\\]]+\\]|[^/:]+):(\\d+)(/.*)?$").search(url)
	if m == null:
		return ERR_INVALID_PARAMETER
	if not _server_key.is_empty() and _server_key.size() != KcpCrypto.KEY_SIZE:
		return ERR_INVALID_PARAMETER
	var host := m.get_string(1).trim_prefix("[").trim_suffix("]")
	_path = m.get_string(3) if not m.get_string(3).is_empty() else "/"
	_subprotocol = protocol
	var address := host if host.is_valid_ip_address() else IP.resolve_hostname(host)
	if address.is_empty() or _udp.connect_to_host(address, int(m.get_string(2))) != OK:
		return ERR_CANT_CONNECT
	while _conv == 0:
		_conv = KcpCrypto.random_bytes(4).decode_u32(0)
	var now := Time.get_ticks_msec()
	if _server_key.is_empty():
		_start_kcp()
	else:
		_handshaking = true
		_handshake_deadline = now + HANDSHAKE_MS
		var conv := _conv
		var key := _server_key
		_start_task(func() -> Array: return [KcpCrypto.ClientHandshake.new(conv, key)])
	return OK

func _start_task(work: Callable) -> void:
	_task_result = []
	_task = WorkerThreadPool.add_task(func() -> void: _task_result = work.call())

## The finished task's result, [] while it runs or if there is none.
func _take_task() -> Array:
	if _task < 0 or not WorkerThreadPool.is_task_completed(_task):
		return []
	WorkerThreadPool.wait_for_task_completion(_task)
	_task = -1
	return [_task_result]

func _send_hello(now: int) -> void:
	_udp.put_packet(_handshake.hello)
	_hello_at = now + HELLO_RETRY_MS

func _start_kcp() -> void:
	_kcp = Core.new(_conv, _output)
	_kcp.no_delay(1, INTERVAL_MS, 2, true)
	_kcp.wnd_size(WINDOW, WINDOW)
	_kcp.set_mtu(MTU - KcpCrypto.SEAL_OVERHEAD if _cipher != null else MTU)
	_kcp.set_dead_link(DEAD_LINK)
	_kcp.update(0)
	_last_recv = Time.get_ticks_msec()
	_last_alive = _last_recv
	_queue(TYPE_OPEN, (_path + "\n" + _subprotocol).to_utf8_buffer())

func _output(data: PackedByteArray) -> void:
	_udp.put_packet(_cipher.seal(data) if _cipher != null else data)

func _control(type: int) -> PackedByteArray:
	var b := PackedByteArray()
	b.resize(5)
	b.encode_u32(0, _conv)
	b[4] = type
	return b

func poll() -> void:
	if _closed:
		return
	var now := Time.get_ticks_msec()
	while not _closed and _udp.get_available_packet_count() > 0:
		var packet := _udp.get_packet()
		if _udp.get_packet_error() != OK:
			_fail()
			return
		_receive(packet, now)
	if _closed:
		return
	if _handshaking:
		var done := _take_task()
		if not done.is_empty():
			var result: Array = done[0]
			if result.is_empty():
				pass # A welcome that did not authenticate: keep waiting.
			elif _handshake == null:
				_handshake = result[0]
				_send_hello(now)
			else:
				_cipher = result[0]
				_handshaking = false
				_handshake = null
				_start_kcp()
				return
		if now >= _handshake_deadline:
			_fail() # Retried like a network failure.
		elif _handshake != null and now >= _hello_at:
			_send_hello(now)
		return
	_kcp.update(now)
	if _kcp.dead():
		_fail()
		return
	if now - _last_recv > QUIET_MS and now - _last_alive > QUIET_MS:
		_last_alive = now
		_udp.put_packet(_cipher.seal(PackedByteArray()) if _cipher != null else _control(KcpCrypto.KEEPALIVE))
	if _closing and (_kcp.wait_snd() == 0 or now >= _close_deadline):
		_fail()

func _receive(packet: PackedByteArray, now: int) -> void:
	if packet.size() < 5 or packet.decode_u32(0) != _conv:
		return
	var reset := packet.size() == 5 and packet[4] == KcpCrypto.RESET
	if _handshaking:
		if reset:
			# A plain server resets every hello; one or two may be damage or forgery.
			_resets += 1
			if _resets >= HANDSHAKE_RESETS:
				_refuse("The server does not offer encrypted KCP; remove kcp_server_key or set Server.KCPKey")
			elif _handshake != null:
				_send_hello(now)
			return
		# Cheap checks here; the X25519 work runs on a worker. Welcomes that
		# arrive meanwhile are dropped: the server repeats them.
		if _handshake != null and _task < 0 and packet.size() == KcpCrypto.WELCOME_SIZE and packet[4] == KcpCrypto.WELCOME:
			var h: Variant = _handshake
			_start_task(func() -> Array: return h.finish(packet))
		return
	if reset:
		_fail()
		return
	var plain := packet
	if _cipher == null:
		if packet.size() == 5 and packet[4] == KcpCrypto.KEEPALIVE:
			return
		if packet.size() == 5 and packet[4] == KcpCrypto.PLAIN_REFUSE:
			_refuse("The server only accepts encrypted KCP; set kcp_server_key")
			return
	else:
		var opened: Array = _cipher.open(packet)
		if opened.is_empty():
			return # Damaged or forged: dropped like a lost datagram.
		plain = opened[0]
		if plain.is_empty():
			_last_recv = now
			return # Keepalive.
	_last_recv = now
	if not _kcp.input(plain):
		return
	while not _closed:
		var m: Array = _kcp.recv()
		if m.is_empty():
			break
		_deliver(m[0])
	if not _closed:
		_kcp.flush() # Acknowledge at once instead of on the next tick.

func _deliver(m: PackedByteArray) -> void:
	if m.is_empty():
		_fail()
		return
	var type := m[0] & ~MORE
	var more := (m[0] & MORE) != 0
	match type:
		TYPE_OPEN:
			if _opened or m.slice(1).get_string_from_utf8() != _subprotocol:
				_fail()
				return
			_opened = true
		TYPE_CLOSE:
			var reason := m.slice(1).get_string_from_utf8()
			if not _opened and reason.begins_with("offer "):
				_refuse("Server did not accept " + _subprotocol)
				return
			_close_reason = reason
			_fail()
		TYPE_BINARY, TYPE_TEXT:
			if not _opened or (_partial_type >= 0 and _partial_type != type):
				_fail()
				return
			_partial_type = type
			if _partial.size() + m.size() - 1 > MAX_MESSAGE:
				_fail()
				return
			_partial.append_array(m.slice(1))
			if not more:
				_inbox.append([_partial, type == TYPE_TEXT])
				_partial = PackedByteArray()
				_partial_type = -1
		_:
			_fail()

func _queue(type: int, p: PackedByteArray) -> void:
	var at := 0
	while true:
		var n := mini(p.size() - at, CHUNK)
		var chunk := PackedByteArray([type | (MORE if at + n < p.size() else 0)])
		chunk.append_array(p.slice(at, at + n))
		_kcp.send(chunk)
		at += n
		if at >= p.size():
			break
	_kcp.flush()

## 0 connecting, 1 open, 2 closed.
func status() -> int:
	if _closed:
		return 2
	return 1 if _opened else 0

func protocol() -> String:
	return _subprotocol if _opened else ""

## [bytes, is_text] or [] when nothing is waiting.
func receive() -> Array:
	return _inbox.pop_front() if not _inbox.is_empty() else []

func send(data: PackedByteArray, text: bool) -> Error:
	if _closed or _closing or not _opened:
		return ERR_UNAVAILABLE
	if data.size() > MAX_MESSAGE:
		return ERR_INVALID_PARAMETER
	_queue(TYPE_TEXT if text else TYPE_BINARY, data)
	return OK

## Close reason sent by the server (the TabForge error code), "" if none.
func close_reason() -> String:
	return _close_reason

## A refusal that reconnecting cannot fix (encryption mismatch, encoding), "" if none.
func refusal() -> String:
	return _refusal

## Sends what is already queued and a close message, then closes; at most one second.
func close() -> void:
	if _closing or _closed:
		return
	_closing = true
	_close_deadline = Time.get_ticks_msec() + CLOSE_LINGER_MS
	if _kcp == null or not _opened:
		_fail()
		return
	_queue(TYPE_CLOSE, PackedByteArray())
	# The client stops polling a closed connection: send the rest now.
	var deadline := Time.get_ticks_msec() + 50
	while _kcp.wait_snd() > 0 and Time.get_ticks_msec() < deadline:
		OS.delay_msec(INTERVAL_MS)
		poll()
	_fail()

func _refuse(message: String) -> void:
	_refusal = message
	_fail()

func _fail() -> void:
	if _closed:
		return
	_closed = true
	_udp.close()
	if _task >= 0:
		WorkerThreadPool.wait_for_task_completion(_task)
		_task = -1
