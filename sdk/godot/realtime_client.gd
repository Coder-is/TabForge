extends Node
## TabForge realtime channel client for Godot (WebSocketPeer). Add it to the
## scene tree: it polls in _process, so every signal fires on the main thread.
## The session survives network drops: unacknowledged frames are sent again
## after a resume, so requests run at most once and pushes arrive once, in
## order. Messages are ProtoJSON Dictionaries (64-bit integers as strings).
##
##   var client := preload("res://addons/tabforge/realtime_client.gd").new()
##   add_child(client)
##   client.configure("wss://host/ws/game", PROTOCOL.CONTRACT, "game", {"token": func(): return token})
##   client.on_push("chatMessage", func(m): print(m.text))
##   client.start()
##   var login = client.request("login", {"nickname": "勇者"})
##   login.completed.connect(func(reply): print(reply.playerId))

const Wire = preload("wire_validator.gd")
const Codec = preload("realtime_codec.gd")

const SUBPROTOCOL_PROTO := "tabforge.realtime.v1.proto"
const SUBPROTOCOL_JSON := "tabforge.realtime.v1.json"
const KIND_NAMES := ["", "hello", "welcome", "request", "response", "push", "notify", "ping", "pong", "ack", "close"]
const HELLO := 1
const WELCOME := 2
const REQUEST := 3
const RESPONSE := 4
const PUSH := 5
const NOTIFY := 6
const PING := 7
const PONG := 8
const ACK := 9
const CLOSE := 10
const MAX_VOLATILE := 256
## Close codes after which the client reconnects (see doc/realtime.md).
const RETRYABLE_CLOSES := ["session_expired", "server_shutdown", "overloaded"]

## "connecting", "connected" (new session: reload state), "resumed" (nothing
## lost), "disconnected", "closed" (permanent: kicked, duplicate_login...).
signal state_changed(state: String, error: Dictionary)
## Errors raised while decoding pushes.
signal handler_error(message: String)

class Request extends RefCounted:
	signal completed(value: Dictionary)
	signal failed(error: Dictionary)
	var done := false
	var result: Dictionary = {}
	var error: Dictionary = {}
	var _response_type := ""
	var _deadline := 0

## Default socket on WebSocketPeer (desktop, mobile and Web exports).
class PeerSocket extends RefCounted:
	var peer := WebSocketPeer.new()
	func open(url: String, protocol: String) -> Error:
		peer.supported_protocols = PackedStringArray([protocol])
		peer.inbound_buffer_size = 1 << 20
		peer.outbound_buffer_size = 1 << 20
		return peer.connect_to_url(url)
	func poll() -> void:
		peer.poll()
	## 0 connecting, 1 open, 2 closed.
	func status() -> int:
		match peer.get_ready_state():
			WebSocketPeer.STATE_CONNECTING: return 0
			WebSocketPeer.STATE_OPEN: return 1
		return 2
	func protocol() -> String:
		return peer.get_selected_protocol()
	## [bytes, is_text] or [] when nothing is waiting.
	func receive() -> Array:
		if peer.get_available_packet_count() == 0:
			return []
		var packet := peer.get_packet()
		return [packet, peer.was_string_packet()]
	func send(data: PackedByteArray, text: bool) -> Error:
		if text:
			return peer.send_text(data.get_string_from_utf8())
		return peer.send(data)
	## Close reason sent by the server (the TabForge error code), "" if none.
	func close_reason() -> String:
		return peer.get_close_reason() if peer.get_close_code() == 1000 else ""
	func close() -> void:
		peer.close()

class Connection extends RefCounted:
	var socket: Variant
	var opened := false
	var ready := false
	var idle_deadline := 0
	var next_ping := 0

var state := "idle"
var session_id := ""
var _url := ""
var _version := ""
var _schema_hash := ""
var _codec: Codec
var _validator: Wire
var _normalizer: Wire
var _by_name := {}
var _by_id := {}
var _handlers := {}
var _token: Callable
var _socket_factory: Callable
var _min_backoff := 200
var _max_backoff := 10000
var _max_unacked := 1024
var _resume_token := ""
var _heartbeat := 5000
var _next_seq := 0
var _peer_ack := 0
var _received := 0
var _ack_sent := 0
var _outbox: Array = []
var _pending := {}
var _conn: Connection
var _failures := 0
var _retry_at := -1
var _ack_at := -1
var _closed_error: Dictionary = {}
var _deferred: Array = []

## contract is the generated protocol.gd CONTRACT. options: encoding ("proto"
## or "json"), token (Callable returning String), min_backoff_ms,
## max_backoff_ms, max_unacked, socket (Callable(url, protocol) -> socket).
func configure(url: String, contract: Dictionary, channel: String, options: Dictionary = {}) -> Error:
	if state != "idle" or not contract.has_all(["version", "schemaHash", "wireSchema", "channels"]) or not contract.channels.has(channel):
		return ERR_INVALID_PARAMETER
	var spec: Dictionary = contract.channels[channel]
	_url = url
	_version = contract.version
	_schema_hash = contract.schemaHash
	_validator = Wire.new(contract.wireSchema)
	_normalizer = Wire.new(contract.wireSchema, Wire.PROTOCOL_MAX_NODES, true)
	if options.get("encoding", "proto") == "proto":
		if not contract.has("codecSchema"): return ERR_INVALID_PARAMETER
		_codec = Codec.new(contract.codecSchema)
	_heartbeat = int(spec.heartbeatMS)
	for name in spec.messages:
		var message: Dictionary = spec.messages[name].duplicate()
		message["name"] = name
		_by_name[name] = message
		_by_id[int(message.id)] = message
	_token = options.get("token", Callable())
	_socket_factory = options.get("socket", Callable())
	_min_backoff = int(options.get("min_backoff_ms", 200))
	_max_backoff = int(options.get("max_backoff_ms", 10000))
	_max_unacked = int(options.get("max_unacked", 1024))
	return OK

func start() -> void:
	if state == "idle" and _closed_error.is_empty() and not _url.is_empty():
		_open()

## Ends the session on the server and stops reconnecting.
func close() -> void:
	_shutdown({"code": "closed", "message": "Closed by client", "retryable": false}, true)

func _exit_tree() -> void:
	close()

## Subscribes to a push; unknown pushes from newer servers are ignored.
func on_push(name: String, handler: Callable) -> Error:
	if not _by_name.has(name) or _by_name[name].kind != "push":
		return ERR_INVALID_PARAMETER
	if not _handlers.has(name):
		_handlers[name] = []
	_handlers[name].append(handler)
	return OK

## timeout_ms 0 uses the contract timeout. Failures are delivered on the next
## frame, so signals can be connected after this call.
func request(name: String, data: Dictionary, timeout_ms: int = 0) -> Request:
	var handle := Request.new()
	if not _by_name.has(name) or _by_name[name].kind != "request":
		_reject_later(handle, {"code": "invalid_message", "message": "No request message " + name, "retryable": false})
		return handle
	if not _closed_error.is_empty():
		_reject_later(handle, _closed_error)
		return handle
	var spec: Dictionary = _by_name[name]
	var frame := _encode(spec, data)
	if frame.has("error"):
		_reject_later(handle, frame.error)
		return handle
	if _outbox.size() >= _max_unacked:
		_reject_later(handle, {"code": "overloaded", "message": "Too many unacknowledged messages", "retryable": true})
		return handle
	handle._response_type = spec.responseType
	var timeout := int(spec.timeoutMS)
	if timeout_ms > 0: timeout = mini(timeout, timeout_ms)
	handle._deadline = Time.get_ticks_msec() + timeout
	frame["kind"] = REQUEST
	frame["call"] = handle
	_enqueue(frame)
	return handle

## Reliable notifies queue while offline; volatile ones return false when dropped.
func notify(name: String, data: Dictionary) -> bool:
	if not _by_name.has(name) or _by_name[name].kind != "notify" or not _closed_error.is_empty():
		return false
	var spec: Dictionary = _by_name[name]
	var frame := _encode(spec, data)
	if frame.has("error"):
		handler_error.emit(frame.error.message)
		return false
	frame["kind"] = NOTIFY
	if not spec.get("volatile", false):
		if _outbox.size() >= _max_unacked: return false
		_enqueue(frame)
		return true
	if _conn == null or not _conn.ready:
		return false
	_write(_conn, frame)
	return true

func _reject_later(handle: Request, error: Dictionary) -> void:
	_deferred.append(_finish.bind(handle, {}, error))

func _encode(spec: Dictionary, data: Variant) -> Dictionary:
	var problem: String = _normalizer.validate(spec.type, data.duplicate(true) if data is Dictionary else data)
	if not problem.is_empty():
		return {"error": {"code": "invalid_message", "message": problem, "retryable": false}}
	var frame := {"message": int(spec.id), "seq": 0, "written": false}
	if _codec != null:
		var payload := _codec.encode(spec.type, _normalizer.result)
		if not _codec.error.is_empty():
			return {"error": {"code": "invalid_message", "message": _codec.error, "retryable": false}}
		frame["payload"] = payload
	else:
		frame["json"] = _normalizer.result
	return frame

func _decode_payload(type_name: String, frame: Dictionary) -> Variant:
	if _codec != null:
		var value: Variant = _codec.decode(type_name, frame.get("payload", PackedByteArray()))
		return value if _codec.error.is_empty() else _codec.error
	var json: Dictionary = frame.get("json", {})
	var problem: String = _validator.validate(type_name, json)
	return json if problem.is_empty() else problem

func _set_state(next: String, error: Dictionary = {}) -> void:
	state = next
	state_changed.emit(next, error)

func _enqueue(frame: Dictionary) -> void:
	_next_seq += 1
	frame.seq = _next_seq
	_outbox.append(frame)
	if frame.has("call"):
		_pending[frame.seq] = frame.call
	if _conn != null and _conn.ready:
		_write(_conn, frame)

func _write(c: Connection, frame: Dictionary) -> void:
	if frame.seq != 0 and frame.seq <= _peer_ack:
		return
	frame.written = true
	_ack_sent = _received
	var out := {"kind": frame.kind, "seq": frame.seq, "ack": _received, "message": frame.message}
	if frame.has("payload"): out["payload"] = frame.payload
	if frame.has("json"): out["json"] = frame.json
	_send(c, out)

func _send(c: Connection, frame: Dictionary) -> void:
	var data: PackedByteArray
	if _codec != null:
		data = encode_binary_frame(frame)
	else:
		data = encode_json_frame(frame).to_utf8_buffer()
	if c.socket.send(data, _codec == null) != OK:
		_drop(c, {})

func _open() -> void:
	if not _closed_error.is_empty(): return
	_retry_at = -1
	_set_state("connecting")
	var c := Connection.new()
	c.idle_deadline = Time.get_ticks_msec() + _heartbeat * 2 + 1000
	c.socket = _socket_factory.call() if _socket_factory.is_valid() else PeerSocket.new()
	_conn = c
	if c.socket.open(_url, SUBPROTOCOL_PROTO if _codec != null else SUBPROTOCOL_JSON) != OK:
		_conn = null
		_schedule_retry(false)

func _process(_delta: float) -> void:
	tick()

## Polls the socket and runs timers. Called from _process; tests may call it.
func tick() -> void:
	var deferred := _deferred
	_deferred = []
	for callback in deferred:
		callback.call()
	var c := _conn
	if c != null:
		c.socket.poll()
		var status: int = c.socket.status()
		if not c.opened and status == 1:
			c.opened = true
			_on_opened(c)
		while _conn == c and c.opened:
			var packet: Array = c.socket.receive()
			if packet.is_empty(): break
			_on_received(c, packet[0], packet[1])
		if _conn == c and status == 2:
			_closed_by_peer(c)
	_timers(Time.get_ticks_msec())

## Some WebSocket stacks drop the last data frame when the server closes
## right after it. The server also puts the error code in the WebSocket close
## reason, so a lost close frame still stops the client correctly.
func _closed_by_peer(c: Connection) -> void:
	var reason: String = c.socket.close_reason() if c.socket.has_method("close_reason") else ""
	if c.opened and RegEx.create_from_string("^[a-z][a-z0-9_]{0,63}$").search(reason) != null:
		_server_closed(c, {"code": reason, "message": "Closed by server", "retryable": reason in RETRYABLE_CLOSES})
		return
	_drop(c, {})

func _on_opened(c: Connection) -> void:
	var expected := SUBPROTOCOL_PROTO if _codec != null else SUBPROTOCOL_JSON
	var negotiated: String = c.socket.protocol()
	if not negotiated.is_empty() and negotiated != expected:
		_shutdown({"code": "protocol_error", "message": "Server did not accept " + expected, "retryable": false}, false)
		return
	var hello := {"protocolVersion": _version, "schemaHash": _schema_hash}
	if session_id.is_empty():
		if _token.is_valid(): hello["token"] = str(_token.call())
	else:
		hello["sessionId"] = session_id
		hello["resumeToken"] = _resume_token
	_send(c, {"kind": HELLO, "seq": 0, "ack": _received, "message": 0, "hello": hello})

func _drop(c: Connection, error: Dictionary) -> void:
	if _conn != c: return
	_conn = null
	c.socket.close()
	if not _closed_error.is_empty(): return
	if not error.is_empty() and not error.get("retryable", false):
		_shutdown(error, false)
		return
	if c.ready or state != "disconnected":
		_set_state("disconnected", error)
	_schedule_retry(false)

func _schedule_retry(immediate: bool) -> void:
	if not _closed_error.is_empty(): return
	var delay := 0
	if not immediate:
		var bound := mini(_max_backoff, _min_backoff * int(pow(2, mini(_failures, 16))))
		delay = bound / 2 + randi_range(0, bound / 2)
		_failures += 1
	if state != "disconnected":
		_set_state("disconnected")
	_retry_at = Time.get_ticks_msec() + delay

func _timers(now: int) -> void:
	if not _closed_error.is_empty(): return
	if _retry_at >= 0 and now >= _retry_at and _conn == null:
		_open()
	var c := _conn
	if c != null and now >= c.idle_deadline:
		_drop(c, {})
		c = null
	if c != null and c.ready and now >= c.next_ping:
		c.next_ping = now + _heartbeat
		_send(c, {"kind": PING, "seq": 0, "ack": _received, "message": 0})
	if _ack_at >= 0 and now >= _ack_at:
		_ack_at = -1
		if _conn != null and _conn.ready and _received > _ack_sent:
			_ack_sent = _received
			_send(_conn, {"kind": ACK, "seq": 0, "ack": _received, "message": 0})
	for seq in _pending.keys():
		var call: Request = _pending[seq]
		if now >= call._deadline:
			_pending.erase(seq)
			_finish(call, {}, {"code": "timeout", "message": "Request deadline exceeded", "retryable": false})

func _finish(call: Request, value: Dictionary, error: Dictionary) -> void:
	if call.done: return
	call.done = true
	if error.is_empty():
		call.result = value
		call.completed.emit(value)
	else:
		call.error = error
		call.failed.emit(error)

func _on_received(c: Connection, data: PackedByteArray, text: bool) -> void:
	if data.is_empty(): return # Empty messages are not frames.
	var f: Dictionary
	if _codec != null:
		f = {"error": "text frame on binary subprotocol"} if text else decode_binary_frame(data)
	else:
		f = decode_json_frame(data) if text else {"error": "binary frame on JSON subprotocol"}
	if not f.has("error"):
		var problem := validate_server_frame(f)
		if not problem.is_empty(): f = {"error": problem}
	if f.has("error"):
		_drop(c, {"code": "protocol_error", "message": "Malformed frame: " + str(f.error), "retryable": true})
		return
	c.idle_deadline = Time.get_ticks_msec() + _heartbeat * 2 + 1000
	if f.kind == CLOSE:
		_server_closed(c, f.error_value)
		return
	if not c.ready:
		if f.kind != WELCOME:
			_drop(c, {"code": "protocol_error", "message": "Expected welcome", "retryable": true})
		else:
			_welcome(c, f)
		return
	if f.kind == WELCOME:
		_drop(c, {"code": "protocol_error", "message": "Unexpected welcome", "retryable": true})
		return
	if not _apply_ack(c, f.ack) or f.kind == PONG or f.kind == ACK:
		return
	if f.kind == PUSH and f.seq == 0:
		_deliver_push(f)
		return
	if f.seq <= _received:
		_ack_sent = 0 # A retransmission: acknowledge again.
		_schedule_ack()
		return
	if f.seq != _received + 1:
		_drop(c, {"code": "protocol_error", "message": "Sequence gap from server", "retryable": true})
		return
	_received = f.seq
	_schedule_ack()
	if f.kind == PUSH:
		_deliver_push(f)
		return
	if not _pending.has(f.replyTo):
		return # Timed out; the late answer is discarded.
	var call: Request = _pending[f.replyTo]
	_pending.erase(f.replyTo)
	if f.has("error_value"):
		_finish(call, {}, f.error_value)
		return
	var value: Variant = _decode_payload(call._response_type, f)
	if value is Dictionary:
		_finish(call, value, {})
	else:
		_finish(call, {}, {"code": "protocol_error", "message": "Invalid response: " + str(value), "retryable": false})

func _welcome(c: Connection, f: Dictionary) -> void:
	var w: Dictionary = f.welcome
	var resuming := not session_id.is_empty()
	if w.resumed and w.sessionId != session_id:
		_drop(c, {"code": "protocol_error", "message": "Server resumed a different session", "retryable": false})
		return
	if not w.resumed:
		if resuming: _reset_session()
		_received = 0
		_peer_ack = 0
	session_id = w.sessionId
	_resume_token = w.resumeToken
	_heartbeat = int(w.heartbeatMS)
	c.ready = true
	_failures = 0
	if not _apply_ack(c, f.ack): return
	_ack_sent = 0
	for frame in _outbox.duplicate():
		if _conn == c: _write(c, frame)
	if _conn != c: return
	c.next_ping = Time.get_ticks_msec() + _heartbeat
	c.idle_deadline = Time.get_ticks_msec() + _heartbeat * 2 + 1000
	_set_state("resumed" if w.resumed else "connected")

func _apply_ack(c: Connection, ack: int) -> bool:
	if ack > _next_seq:
		_drop(c, {"code": "protocol_error", "message": "Server acknowledged an unsent frame", "retryable": true})
		return false
	if ack > _peer_ack:
		_peer_ack = ack
		var n := 0
		while n < _outbox.size() and _outbox[n].seq <= ack: n += 1
		if n > 0: _outbox = _outbox.slice(n)
	return true

func _schedule_ack() -> void:
	if _ack_at < 0: _ack_at = Time.get_ticks_msec() + 50

func _deliver_push(f: Dictionary) -> void:
	if not _by_id.has(f.message) or _by_id[f.message].kind != "push":
		return # Pushes added by a newer server are ignored.
	var spec: Dictionary = _by_id[f.message]
	var handlers: Array = _handlers.get(spec.name, [])
	if handlers.is_empty(): return
	var value: Variant = _decode_payload(spec.type, f)
	if not value is Dictionary:
		handler_error.emit(str(value)); return
	for handler in handlers.duplicate():
		handler.call(value)

func _server_closed(c: Connection, error: Dictionary) -> void:
	if error.code == "session_expired" and not session_id.is_empty():
		# The server forgot the session: start a new one right away.
		_reset_session()
		_conn = null
		c.socket.close()
		_schedule_retry(true)
		return
	_drop(c, error)

## Forget the session. Frames never written move to the next session;
## requests that may have reached the server fail with session_lost.
func _reset_session() -> void:
	var kept: Array = []
	var lost: Array = []
	for frame in _outbox:
		if not frame.written: kept.append(frame)
		elif frame.has("call") and _pending.get(frame.seq) == frame.call: lost.append(frame.call)
	var kept_calls: Array = []
	for frame in kept:
		if frame.has("call"): kept_calls.append(frame.call)
	for call in _pending.values():
		if not call in kept_calls and not call in lost: lost.append(call)
	_pending.clear()
	for index in kept.size():
		kept[index].seq = index + 1
		if kept[index].has("call"): _pending[index + 1] = kept[index].call
	_outbox = kept
	_next_seq = kept.size()
	session_id = ""
	_resume_token = ""
	_received = 0
	_peer_ack = 0
	_ack_sent = 0
	for call in lost:
		_finish(call, {}, {"code": "session_lost", "message": "Connection lost; the request may or may not have run", "retryable": false})

func _shutdown(error: Dictionary, notify_server: bool) -> void:
	if not _closed_error.is_empty(): return
	_closed_error = error
	_retry_at = -1
	_ack_at = -1
	var c := _conn
	_conn = null
	if c != null:
		if notify_server and c.ready:
			_send(c, {"kind": CLOSE, "seq": 0, "ack": _received, "message": 0, "error_value": error})
		c.socket.close()
	for call in _pending.values():
		_finish(call, {}, error)
	_pending.clear()
	_outbox.clear()
	_set_state("closed", error)

# ---- frames (see doc/realtime.md) ----

static func _put_varint(out: PackedByteArray, number: int, value: int) -> void:
	if value != 0:
		Codec._tag(out, number, 0)
		Codec._varint(out, value)

static func _put_string(out: PackedByteArray, number: int, text: String) -> void:
	if not text.is_empty():
		Codec._tag(out, number, 2)
		Codec._bytes(out, text.to_utf8_buffer())

static func encode_binary_frame(f: Dictionary) -> PackedByteArray:
	var out := PackedByteArray()
	_put_varint(out, 1, f.kind)
	_put_varint(out, 2, f.get("seq", 0))
	_put_varint(out, 3, f.get("ack", 0))
	_put_varint(out, 4, f.get("message", 0))
	_put_varint(out, 5, f.get("replyTo", 0))
	if f.has("payload") and not f.payload.is_empty():
		Codec._tag(out, 6, 2)
		Codec._bytes(out, f.payload)
	if f.has("error_value"):
		var e := PackedByteArray()
		_put_string(e, 1, f.error_value.code)
		_put_string(e, 2, f.error_value.get("message", ""))
		_put_varint(e, 3, 1 if f.error_value.get("retryable", false) else 0)
		Codec._tag(out, 7, 2)
		Codec._bytes(out, e)
	if f.has("hello"):
		var h := PackedByteArray()
		_put_string(h, 1, f.hello.get("protocolVersion", ""))
		_put_string(h, 2, f.hello.get("schemaHash", ""))
		_put_string(h, 3, f.hello.get("token", ""))
		_put_string(h, 4, f.hello.get("sessionId", ""))
		_put_string(h, 5, f.hello.get("resumeToken", ""))
		Codec._tag(out, 8, 2)
		Codec._bytes(out, h)
	return out

static func _text(r: Codec.Reader, span: Array) -> Variant:
	var decoded: Dictionary = Wire.decode_utf8(r.data.slice(span[0], span[1]))
	return null if decoded.has("error") else decoded.text

## Returns the frame Dictionary, or {"error": reason}.
static func decode_binary_frame(data: PackedByteArray) -> Dictionary:
	var f := {"kind": 0, "seq": 0, "ack": 0, "message": 0, "replyTo": 0}
	var r := Codec.Reader.new(data, 0, data.size())
	while r.pos < r.end and r.error.is_empty():
		var tag := r.varint()
		var number := Codec._logical_shift(tag, 3)
		var wire := tag & 7
		if number >= 1 and number <= 5:
			if wire != 0: return {"error": "wire type of field %d" % number}
			var value := r.varint()
			if value < 0: return {"error": "number out of range"}
			f[["", "kind", "seq", "ack", "message", "replyTo"][number]] = value
		elif number >= 6 and number <= 9:
			if wire != 2: return {"error": "wire type of field %d" % number}
			var span := r.length()
			if number == 6:
				f["payload"] = data.slice(span[0], span[1])
				continue
			var inner := Codec.Reader.new(data, span[0], span[1])
			var fields := {}
			while inner.pos < inner.end and inner.error.is_empty():
				var inner_tag := inner.varint()
				var inner_number := Codec._logical_shift(inner_tag, 3)
				if inner_tag & 7 == 0:
					fields[inner_number] = inner.varint()
				elif inner_tag & 7 == 2:
					var text: Variant = _text(inner, inner.length())
					if text == null: return {"error": "invalid UTF-8"}
					fields[inner_number] = text
				else:
					inner.skip(inner_tag & 7, 0)
			if not inner.error.is_empty(): return {"error": inner.error}
			if number == 7:
				if not fields.get(1, "") is String or not fields.get(2, "") is String or not fields.get(3, 0) is int: return {"error": "error"}
				f["error_value"] = {"code": fields.get(1, ""), "message": fields.get(2, ""), "retryable": fields.get(3, 0) != 0}
			elif number == 8:
				f["hello"] = {}
			else:
				if not fields.get(1, "") is String or not fields.get(2, "") is String or not fields.get(3, 0) is int or not fields.get(4, 0) is int: return {"error": "welcome"}
				f["welcome"] = {"sessionId": fields.get(1, ""), "resumeToken": fields.get(2, ""), "resumed": fields.get(3, 0) != 0, "heartbeatMS": fields.get(4, 0), "resumeWindowMS": fields.get(5, 0)}
		else:
			r.skip(wire, 0)
	if not r.error.is_empty(): return {"error": r.error}
	if f.message > 65535: return {"error": "message id"}
	return f

static func encode_json_frame(f: Dictionary) -> String:
	var out := {"kind": KIND_NAMES[f.kind]}
	if f.get("seq", 0) != 0: out["seq"] = str(f.seq)
	if f.get("ack", 0) != 0: out["ack"] = str(f.ack)
	if f.get("message", 0) != 0: out["message"] = f.message
	if f.has("json"): out["payload"] = f.json
	if f.has("error_value"): out["error"] = f.error_value
	if f.has("hello"): out["hello"] = f.hello
	return JSON.stringify(out, "", false, true)

static func _decimal(value: Variant) -> int:
	if value == null: return 0
	if not value is String or RegEx.create_from_string("^[1-9][0-9]{0,17}$").search(value) == null: return -1
	return value.to_int()

static func decode_json_frame(data: PackedByteArray) -> Dictionary:
	var parsed: Dictionary = Wire.parse_json_bytes(data)
	if parsed.has("error") or not parsed.value is Dictionary: return {"error": "invalid JSON"}
	var o: Dictionary = parsed.value
	for key in o:
		if not key in ["kind", "seq", "ack", "message", "replyTo", "payload", "error", "hello", "welcome"]:
			return {"error": "unknown member " + str(key)}
	var kind: int = KIND_NAMES.find(o.get("kind", "")) if o.get("kind") is String else -1
	var f := {"kind": kind, "seq": _decimal(o.get("seq")), "ack": _decimal(o.get("ack")), "replyTo": _decimal(o.get("replyTo")), "message": 0}
	if kind <= 0 or f.seq < 0 or f.ack < 0 or f.replyTo < 0: return {"error": "kind or sequence"}
	if o.has("message"):
		if not (o.message is int or (o.message is float and o.message == floorf(o.message))) or o.message < 0 or o.message > 65535: return {"error": "message id"}
		f.message = int(o.message)
	if o.has("payload"):
		if not o.payload is Dictionary: return {"error": "payload"}
		f["json"] = o.payload
	if o.has("error"):
		var e: Variant = o.error
		if not e is Dictionary or not e.get("code") is String or not e.get("message", "") is String or not e.get("retryable", false) is bool: return {"error": "error"}
		f["error_value"] = {"code": e.code, "message": e.get("message", ""), "retryable": e.get("retryable", false)}
	if o.has("welcome"):
		var w: Variant = o.welcome
		if not w is Dictionary or not w.get("sessionId") is String or not w.get("resumeToken") is String or not (w.get("heartbeatMS") is int or w.get("heartbeatMS") is float): return {"error": "welcome"}
		f["welcome"] = {"sessionId": w.sessionId, "resumeToken": w.resumeToken, "resumed": w.get("resumed", false) == true, "heartbeatMS": int(w.heartbeatMS), "resumeWindowMS": int(w.get("resumeWindowMS", 0))}
	if o.has("hello"): f["hello"] = {}
	return f

## Server-to-client envelope rules shared with Go, TypeScript and C#.
static func validate_server_frame(f: Dictionary) -> String:
	var has_payload: bool = f.has("payload") or f.has("json")
	var control: bool = f.seq == 0 and f.message == 0 and f.replyTo == 0 and not has_payload and not f.has("hello")
	var has_error: bool = f.has("error_value")
	match f.kind:
		WELCOME:
			if not control or not f.has("welcome") or f.welcome.sessionId.is_empty() or f.welcome.resumeToken.is_empty() or f.welcome.heartbeatMS <= 0 or has_error: return "welcome"
		RESPONSE:
			if f.seq == 0 or f.message == 0 or f.replyTo == 0 or f.has("hello") or f.has("welcome") or (has_error and has_payload): return "response"
			if has_error and f.error_value.code.is_empty(): return "error code"
		PUSH:
			if f.message == 0 or f.replyTo != 0 or has_error or f.has("hello") or f.has("welcome"): return "push"
		PONG, ACK:
			if not control or has_error or f.has("welcome"): return "control"
		CLOSE:
			if not control or not has_error or f.error_value.code.is_empty() or f.has("welcome"): return "close"
		_:
			return "unexpected kind %d from server" % f.kind
	return ""
