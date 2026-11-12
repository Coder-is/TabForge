extends SceneTree
## Realtime client tests (Godot headless), driven by protocol/realtime tests:
##   unit <protocol.gd>                         scripted fake server
##   codec <protocol.gd> <cases> <out>            Go/GDScript codec equivalence
##   chaos <protocol.gd> <url> <proto|json> <s> [key]   Go server behind a cutting proxy
##                                   (ws:// or kcp://; key: base64 KCP server key or -)
##   kcp <protocol.gd> <cases> <vectors>          KCP core and crypto against Go-generated cases
##   kcprefuse <protocol.gd> <url> <key> <text>   expects closed:protocol_error mentioning text

const Client = preload("../realtime_client.gd")
const Codec = preload("../realtime_codec.gd")
const Wire = preload("../wire_validator.gd")
const KcpCore = preload("../kcp_core.gd")
const KcpCrypto = preload("../kcp_crypto.gd")

var failures := 0
var contract: Dictionary

func check(condition: bool, description: String) -> void:
	if not condition:
		failures += 1
		printerr("FAIL: " + description)

func _initialize() -> void:
	_run.call_deferred()

func _run() -> void:
	var args := OS.get_cmdline_user_args()
	var generated: GDScript = load(args[1])
	contract = generated.CONTRACT
	match args[0]:
		"unit": await unit()
		"codec": codec(args[2], args[3])
		"chaos": await chaos(args[2], args[3], int(args[4]), key_arg(args[5]) if args.size() > 5 else PackedByteArray())
		"kcp": kcp(args[2], args[3])
		"kcprefuse": await kcp_refuse(args[2], key_arg(args[3]), args[4])
	print("realtime %s: %d failures" % [args[0], failures])
	quit(1 if failures > 0 else 0)

# ---- scripted fake server (JSON subprotocol) ----

class FakeSocket extends RefCounted:
	var sent: Array = []
	var inbox: Array = []
	var state := 0
	var opened_protocol := ""
	func open(url: String, protocol: String) -> Error:
		opened_protocol = protocol
		state = 1
		return OK
	func poll() -> void: pass
	func status() -> int: return state
	func protocol() -> String: return opened_protocol
	func receive() -> Array:
		return [] if inbox.is_empty() else inbox.pop_front()
	func send(data: PackedByteArray, text: bool) -> Error:
		if state != 1: return ERR_UNAVAILABLE
		sent.append(JSON.parse_string(data.get_string_from_utf8()))
		return OK
	func close() -> void: state = 2
	func reply(frame: Dictionary) -> void:
		inbox.append([JSON.stringify(frame).to_utf8_buffer(), true])
	func raw(text: String) -> void:
		inbox.append([text.to_utf8_buffer(), true])
	func welcome(resumed: bool = false, session: String = "s1") -> void:
		reply({"kind": "welcome", "welcome": {"sessionId": session, "resumeToken": "t", "heartbeatMS": 5000, "resumed": resumed}})
	func cut() -> void: state = 2

var sockets: Array = []
func make_socket() -> FakeSocket:
	var socket := FakeSocket.new()
	sockets.append(socket)
	return socket

func until(client: Node, condition: Callable, what: String) -> void:
	for i in 2000:
		client.tick()
		if condition.call(): return
		await process_frame
	check(false, "timed out waiting for " + what)

func last() -> FakeSocket:
	return sockets[-1]

func start(states: Array, max_unacked: int = 1024) -> Node:
	sockets.clear()
	var client: Node = Client.new()
	client.set_process(false)
	root.add_child(client)
	check(client.configure("ws://x/ws/game", contract, "game", {"encoding": "json", "token": func(): return "user-1", "socket": make_socket, "min_backoff_ms": 1, "max_backoff_ms": 2, "max_unacked": max_unacked}) == OK, "configure")
	client.state_changed.connect(func(s: String, e: Dictionary) -> void: states.append(s if e.is_empty() else s + ":" + e.code))
	client.start()
	await until(client, func(): return sockets.size() == 1 and last().sent.size() == 1, "hello")
	return client

func result_of(client: Node, request: RefCounted) -> void:
	await until(client, func(): return request.done, "request")

func unit() -> void:
	var states: Array = []
	var c: Node = await start(states)
	var hello: Dictionary = last().sent[0]
	check(last().opened_protocol == Client.SUBPROTOCOL_JSON, "subprotocol")
	check(hello.kind == "hello" and hello.hello.token == "user-1" and not hello.hello.has("sessionId"), "hello " + str(hello))
	last().welcome()
	await until(c, func(): return c.state == "connected", "welcome")
	var login: RefCounted = c.request("login", {"nickname": "勇者😀"})
	var sent: Dictionary = last().sent[-1]
	check(sent.kind == "request" and sent.seq == "1" and int(sent.message) == 1, "request " + str(sent))
	last().reply({"kind": "response", "seq": "1", "ack": "1", "message": 1, "replyTo": "1", "payload": {"playerId": "7"}})
	await result_of(c, login)
	check(login.result.get("playerId") == "7", "response " + str(login.result))
	var bad: RefCounted = c.request("chat", {"text": 1})
	await result_of(c, bad)
	check(bad.error.get("code") == "invalid_message", "invalid request")
	var original := {"itemId": 1.0, "count": "2"}
	c.request("buy", original)
	var normalized: Dictionary = last().sent[-1].payload
	check(not normalized.count is String and normalized.count == 2 and normalized.itemId == 1, "normalized " + str(normalized))
	check(original.count is String, "caller object modified")
	# Pushes: duplicate dropped, unknown ignored.
	var pushes: Array = []
	c.on_push("chatMessage", func(m: Dictionary) -> void: pushes.append(m.messageId))
	last().reply({"kind": "push", "seq": "2", "message": 201, "payload": {"messageId": "1"}})
	last().reply({"kind": "push", "seq": "2", "message": 201, "payload": {"messageId": "1"}})
	last().reply({"kind": "push", "seq": "3", "message": 999, "payload": {}})
	last().reply({"kind": "push", "seq": "4", "message": 201, "payload": {"messageId": "2"}})
	await until(c, func(): return pushes.size() == 2, "pushes")
	check(pushes == ["1", "2"], "pushes " + str(pushes))
	await until(c, func(): return last().sent.any(func(f): return f.kind == "ack" and f.get("ack") == "4"), "ack")
	# Resume retransmits.
	var first: RefCounted = c.request("chat", {"text": "one"})
	last().cut()
	await until(c, func(): return sockets.size() == 2 and last().sent.size() == 1, "reconnect")
	check(last().sent[0].hello.get("sessionId") == "s1" and not last().sent[0].hello.has("token"), "resume hello")
	last().welcome(true)
	await until(c, func(): return c.state == "resumed", "resumed")
	check(last().sent[-1].get("seq") == "3", "retransmitted " + str(last().sent[-1]))
	last().reply({"kind": "response", "seq": "5", "ack": "3", "message": 3, "replyTo": "3", "payload": {"messageId": "9"}})
	await result_of(c, first)
	check(first.result.get("messageId") == "9", "resumed response " + str(first.error))
	# Expiry: written requests fail, unwritten ones carry over.
	var uncertain: RefCounted = c.request("chat", {"text": "two"})
	last().cut()
	c.tick()
	var queued: RefCounted = c.request("chat", {"text": "three"})
	await until(c, func(): return sockets.size() == 3 and last().sent.size() == 1, "third")
	last().reply({"kind": "close", "error": {"code": "session_expired", "message": "gone", "retryable": true}})
	await result_of(c, uncertain)
	check(uncertain.error.get("code") == "session_lost", "session_lost " + str(uncertain.error))
	await until(c, func(): return sockets.size() == 4 and last().sent.size() == 1, "new session")
	check(last().sent[0].hello.get("token") == "user-1", "token for new session")
	last().welcome(false, "s2")
	await until(c, func(): return c.session_id == "s2", "s2")
	check(last().sent[-1].get("seq") == "1" and last().sent[-1].payload.text == "three", "carried " + str(last().sent[-1]))
	# Protocol errors reconnect; permanent closes stop.
	last().raw("{\"kind\":\"push\",\"kind\":\"push\"}")
	await until(c, func(): return sockets.size() == 5 and last().sent.size() >= 1, "reconnect after garbage")
	last().welcome(true, "s2")
	last().reply({"kind": "close", "error": {"code": "duplicate_login", "message": "elsewhere", "retryable": false}})
	await result_of(c, queued)
	check(queued.error.get("code") == "duplicate_login", "pending rejected " + str(queued.error))
	check(c.state == "closed", "closed")
	for i in 20: c.tick()
	check(sockets.size() == 5, "no reconnect after permanent close")
	c.queue_free()
	# Timeouts, backpressure, volatile.
	states = []
	c = await start(states, 2)
	last().welcome()
	await until(c, func(): return c.state == "connected", "welcome")
	var slow: RefCounted = c.request("chat", {"text": "a"}, 5)
	await result_of(c, slow)
	check(slow.error.get("code") == "timeout", "timeout")
	c.request("chat", {"text": "b"})
	var over: RefCounted = c.request("chat", {"text": "c"})
	await result_of(c, over)
	check(over.error.get("code") == "overloaded", "backpressure " + str(over.error))
	check(c.notify("move", {"tick": 1}), "volatile sent")
	check(last().sent[-1].kind == "notify" and not last().sent[-1].has("seq"), "volatile frame")
	last().cut()
	c.tick()
	check(not c.notify("move", {"tick": 2}), "volatile dropped offline")
	c.close()
	check(states[-1] == "closed:closed", "close state " + str(states))
	c.queue_free()
	# Codec and 64-bit helpers.
	check(Codec.u64_string(-1) == "18446744073709551615" and Codec.u64_string(Codec.MIN_INT64) == "9223372036854775808", "u64_string")
	check(Codec.parse_u64("18446744073709551615") == -1 and Codec.parse_u64("-9223372036854775808") == Codec.MIN_INT64 and Codec.parse_u64("123") == 123, "parse_u64")
	var codec := Codec.new(contract.codecSchema)
	var message := {"messageId": "18446744073709551615", "from": "1", "text": "hi", "sentAt": "2023-11-14T22:13:20.000000005Z"}
	check(codec.decode("tabforge.game.ChatMessage", codec.encode("tabforge.game.ChatMessage", message)) == message, "ChatMessage round trip")
	check(codec.decode("tabforge.game.ChatMessage", codec.encode("tabforge.game.ChatMessage", {"sentAt": "2024-01-01T08:00:00+08:00"})).sentAt == "2024-01-01T00:00:00Z", "timestamp offset")
	check(codec.decode("tabforge.game.Move", PackedByteArray([0x10, 1, 0x10, 2])) == {"tick": 2}, "last scalar wins")
	check(codec.decode("tabforge.game.Move", PackedByteArray([0x78, 1, 0x10, 3])) == {"tick": 3}, "unknown skipped")
	check(codec.decode("tabforge.game.Move", PackedByteArray([0x12, 5, 1])) == null, "truncated")
	check(codec.decode("tabforge.game.Move", PackedByteArray([0x12, 1])) == null, "wrong wire type")
	var welcome := Client.decode_binary_frame(PackedByteArray([0x08, 0x02, 0x4a, 0x09, 0x0a, 0x01, 0x73, 0x12, 0x01, 0x74, 0x20, 0x88, 0x27]))
	check(Client.validate_server_frame(welcome).is_empty() and welcome.welcome.sessionId == "s" and welcome.welcome.heartbeatMS == 5000, "binary welcome " + str(welcome))

# ---- Go/GDScript codec equivalence ----

func same(a: Variant, b: Variant) -> bool:
	if (a is int or a is float) and (b is int or b is float):
		return float(a) == float(b) or PackedFloat32Array([a])[0] == PackedFloat32Array([b])[0]
	if a is Dictionary and b is Dictionary:
		if a.size() != b.size(): return false
		for key in a:
			if not b.has(key) or not same(a[key], b[key]): return false
		return true
	if a is Array and b is Array:
		if a.size() != b.size(): return false
		for i in a.size():
			if not same(a[i], b[i]): return false
		return true
	return typeof(a) == typeof(b) and a == b

func codec(input: String, output: String) -> void:
	var codec := Codec.new(contract.codecSchema)
	var normalizer := Wire.new(contract.wireSchema, Wire.PROTOCOL_MAX_NODES, true)
	var validator := Wire.new(contract.wireSchema)
	var cases: Array = Wire.parse_json_bytes(FileAccess.get_file_as_bytes(input), 100000000).value
	var results: Array = []
	for c in cases:
		var expected: Variant = Wire.parse_json_bytes(c.json.to_utf8_buffer()).value
		var decoded: Variant = codec.decode(c.type, Marshalls.base64_to_raw(c.binary))
		if decoded == null or not same(decoded, expected):
			check(false, "%s decoded differently (%s)\nGo:  %s\nGDS: %s" % [c.type, codec.error, c.json, JSON.stringify(decoded)])
			return
		check(validator.validate(c.type, decoded).is_empty(), "decoded output is valid ProtoJSON")
		var problem: String = normalizer.validate(c.type, Wire.parse_json_bytes(c.json.to_utf8_buffer()).value)
		check(problem.is_empty(), problem)
		var from_json := codec.encode(c.type, normalizer.result)
		var from_decoded := codec.encode(c.type, decoded)
		check(codec.error.is_empty(), codec.error)
		results.append({"FromJSON": "" if from_json.is_empty() else Marshalls.raw_to_base64(from_json), "FromDecoded": "" if from_decoded.is_empty() else Marshalls.raw_to_base64(from_decoded)})
	var file := FileAccess.open(output, FileAccess.WRITE)
	file.store_string(JSON.stringify(results))
	file.close()
	print("codec: %d messages match Go" % cases.size())

# ---- against the Go server behind a cutting proxy ----

## A game loop at a high frame rate: Godot headless runs far fewer frames per
## second than the proxy cuts connections, so poll several times per frame.
func pump(client: Node) -> void:
	for i in 20:
		client.tick()
		OS.delay_usec(250)
	await process_frame

func chaos(url: String, encoding: String, seconds: int, kcp_key: PackedByteArray = PackedByteArray()) -> void:
	var states: Array = []
	var client: Node = Client.new()
	root.add_child(client)
	client.configure(url, contract, "game", {"encoding": encoding, "token": func(): return "user-31", "min_backoff_ms": 5, "max_backoff_ms": 50, "kcp_server_key": kcp_key})
	client.state_changed.connect(func(s: String, e: Dictionary) -> void: states.append(s if e.is_empty() else s + ":" + e.code))
	var pushes: Array = []
	client.on_push("chatMessage", func(m: Dictionary) -> void: pushes.append(int(m.messageId)))
	client.start()
	var deadline := Time.get_ticks_msec() + 15000
	while client.state != "connected" and Time.get_ticks_msec() < deadline:
		await pump(client)
	var session: String = client.session_id
	var login: RefCounted = client.request("login", {"nickname": "勇者😀"})
	while not login.done: await pump(client)
	check(login.result.get("playerId") == "31" and login.result.get("nickname") == "勇者😀" and login.result.get("gold") == "100", "login " + str(login.result) + str(login.error))
	# Keep 8 purchases in flight until the deadline.
	var golds: Array = []
	var inflight: Array = []
	var end := Time.get_ticks_msec() + seconds * 1000
	while Time.get_ticks_msec() < end or not inflight.is_empty():
		while Time.get_ticks_msec() < end and inflight.size() < 8:
			inflight.append(client.request("buy", {"itemId": 4294967295, "count": 2}))
		await pump(client)
		for request in inflight.duplicate():
			if not request.done: continue
			inflight.erase(request)
			if not request.error.is_empty():
				check(false, "purchase failed: " + str(request.error)); return
			check(int(request.result.inventory["4294967295"]) == 2, "inventory " + str(request.result))
			golds.append(int(request.result.gold))
	golds.sort()
	for i in golds.size():
		if golds[i] != i + 1:
			check(false, "a purchase ran twice or was lost at %d" % i); return
	deadline = Time.get_ticks_msec() + 10000
	while pushes.size() < golds.size() and Time.get_ticks_msec() < deadline:
		await pump(client)
	check(pushes.size() == golds.size(), "pushes %d of %d" % [pushes.size(), golds.size()])
	for i in pushes.size():
		if pushes[i] != i + 1:
			check(false, "pushes lost, duplicated or reordered at %d" % i); return
	check(client.session_id == session, "session was replaced instead of resumed")
	var business: RefCounted = client.request("chat", {"text": ""})
	while not business.done: await pump(client)
	check(business.error.get("code") == "empty_message" and business.error.get("message") == "say something", "business error " + str(business.error))
	var slow: RefCounted = client.request("chat", {"text": "slow"}, 50)
	while not slow.done: await pump(client)
	check(slow.error.get("code") == "timeout", "timeout " + str(slow.error))
	var moved: Array = []
	client.on_push("playerMoved", func(m: Dictionary) -> void: moved.append(m))
	while moved.is_empty():
		client.notify("move", {"position": {"x": 1.5, "y": -2}, "tick": 9})
		await pump(client)
	check(int(moved[0].tick) == 9 and moved[0].position.x == 1.5, "moved " + str(moved[0]))
	var kick: RefCounted = client.request("chat", {"text": "kick"})
	while not kick.done: await pump(client)
	deadline = Time.get_ticks_msec() + 5000
	while client.state != "closed" and Time.get_ticks_msec() < deadline:
		await pump(client)
	check(states[-1] == "closed:kicked", "kick: " + str(states.slice(-10)))
	print(JSON.stringify({"requests": golds.size(), "resumes": states.count("resumed")}))
	client.queue_free()

# ---- KCP ----

func key_arg(arg: String) -> PackedByteArray:
	return PackedByteArray() if arg == "-" else Marshalls.base64_to_raw(arg)

func kcp_refuse(url: String, key: PackedByteArray, text: String) -> void:
	var states: Array = []
	var last: Dictionary = {}
	var client: Node = Client.new()
	root.add_child(client)
	client.configure(url, contract, "game", {"token": func(): return "user-5", "min_backoff_ms": 5, "max_backoff_ms": 50, "kcp_server_key": key})
	client.state_changed.connect(func(s: String, e: Dictionary) -> void:
		states.append(s if e.is_empty() else s + ":" + e.code)
		if not e.is_empty(): last.merge(e, true))
	client.start()
	var deadline := Time.get_ticks_msec() + 10000
	while client.state != "closed" and Time.get_ticks_msec() < deadline:
		await pump(client)
	check(client.state == "closed" and states[-1] == "closed:protocol_error" and str(last.get("message", "")).contains(text), "refusal: " + str(states) + " " + str(last))
	check(states.count("connecting") == 1, "retried a permanent refusal: " + str(states))
	client.queue_free()

func kcp(cases_path: String, vectors_path: String) -> void:
	var cases: Dictionary = JSON.parse_string(FileAccess.get_file_as_string(cases_path))
	# X25519 in GDScript takes ~40 ms; 40 random cases cover it well enough.
	var xs: Array = cases.x25519.slice(0, 40)
	for c in xs:
		check(KcpCrypto.x25519(c.scalar.hex_decode(), c.point.hex_decode()).hex_encode() == c.out, "x25519 " + str(c))
	var zero := PackedByteArray()
	zero.resize(32)
	check(KcpCrypto.x25519(KcpCrypto.random_bytes(32), zero).is_empty(), "x25519 accepted a low-order point")
	for c in cases.aead:
		var key: PackedByteArray = c.key.hex_decode()
		var nonce: PackedByteArray = c.nonce.hex_decode()
		var aad: PackedByteArray = c.aad.hex_decode()
		var sealed := KcpCrypto.seal(key, nonce, aad, c.plain.hex_decode())
		check(sealed.hex_encode() == c.sealed, "seal " + str(c))
		var opened := KcpCrypto.open(key, nonce, aad, sealed)
		check(not opened.is_empty() and opened[0].hex_encode() == c.plain, "open " + str(c))
		var step := maxi(1, sealed.size() / 7)
		for i in range(0, sealed.size(), step):
			var bad := sealed.duplicate()
			bad[i] ^= 1
			check(KcpCrypto.open(key, nonce, aad, bad).is_empty(), "opened a damaged datagram at byte %d" % i)
	print("kcp crypto: %d x25519, %d aead cases" % [xs.size(), cases.aead.size()])
	kcp_vectors(JSON.parse_string(FileAccess.get_file_as_string(vectors_path)))
	kcp_core_under_loss()

func kcp_vectors(v: Dictionary) -> void:
	var conv := int(v.conv)
	var server_public: PackedByteArray = v.server_static_public.hex_decode()
	check(KcpCrypto.x25519_public(v.server_static_private.hex_decode()).hex_encode() == v.server_static_public, "server public key")
	var h = KcpCrypto.ClientHandshake.new(conv, server_public, v.client_ephemeral_private.hex_decode())
	check(h.hello.hex_encode() == v.hello, "hello " + h.hello.hex_encode())
	var welcome: PackedByteArray = v.welcome.hex_decode()
	var done: Array = h.finish(welcome)
	check(not done.is_empty(), "the client rejected the genuine welcome")
	if done.is_empty(): return
	var client = done[0]
	check(client.send_key.hex_encode() == v.client_to_server_key and client.recv_key.hex_encode() == v.server_to_client_key, "derived keys")
	var wrong = KcpCrypto.ClientHandshake.new(conv, KcpCrypto.x25519_public(KcpCrypto.random_bytes(32)), v.client_ephemeral_private.hex_decode())
	check(wrong.finish(welcome).is_empty(), "a client pinned to another key accepted the welcome")
	var damaged := welcome.duplicate()
	damaged[20] ^= 1
	check(h.finish(damaged).is_empty(), "accepted a damaged welcome")
	var keys: PackedByteArray = (v.client_to_server_key + v.server_to_client_key).hex_decode()
	var server = KcpCrypto.Cipher.new(conv, keys, false)
	for d in v.datagrams:
		var from_client: bool = d.from == "client"
		var sealed: PackedByteArray = (client if from_client else server).seal(d.plaintext.hex_decode())
		check(sealed.hex_encode() == d.sealed, "sealed " + str(d))
		var opened: Array = (server if from_client else client).open(sealed)
		check(not opened.is_empty() and opened[0].hex_encode() == d.plaintext, "opened " + str(d))
	var zero_counter: PackedByteArray = client.seal(PackedByteArray([1]))
	for i in range(5, 13):
		zero_counter[i] = 0
	check(server.open(zero_counter).is_empty(), "opened a datagram with counter 0")
	print("kcp vectors match Go")

## Two cores over a link that drops, duplicates and reorders, on a virtual clock.
func kcp_core_under_loss() -> void:
	var r := RandomNumberGenerator.new()
	r.seed = 7
	var in_flight: Array = [] # [at, to_b, data]
	var clock := [0]
	var link := func(to_b: bool) -> Callable:
		return func(data: PackedByteArray) -> void:
			var copies := 2 if r.randf() < 0.05 else 1
			for copy in copies:
				if r.randf() >= 0.2:
					in_flight.append([clock[0] + r.randi_range(5, 60), to_b, data.duplicate()])
	var a := KcpCore.new(9, link.call(true))
	var b := KcpCore.new(9, link.call(false))
	for k in [a, b]:
		k.no_delay(1, 10, 2, true)
		k.wnd_size(256, 256)
		k.set_mtu(1200)
	var sent: Array = []
	for i in 100:
		var m := PackedByteArray()
		m.resize(r.randi_range(1, 5000))
		for j in m.size():
			m[j] = r.randi() & 0xff
		sent.append(m)
		check(a.send(m), "send %d" % i)
	var got: Array = []
	while clock[0] < 600000 and got.size() < sent.size():
		a.update(clock[0])
		b.update(clock[0])
		var due: Array = in_flight.filter(func(p: Array) -> bool: return p[0] <= clock[0])
		var rest: Array = in_flight.filter(func(p: Array) -> bool: return p[0] > clock[0])
		in_flight.clear() # In place: the link closures hold this array.
		in_flight.append_array(rest)
		due.shuffle()
		for p in due:
			(b if p[1] else a).input(p[2])
		while true:
			var m: Array = b.recv()
			if m.is_empty(): break
			got.append(m[0])
		clock[0] += 5
	check(got.size() == sent.size(), "delivered %d of %d" % [got.size(), sent.size()])
	for i in mini(got.size(), sent.size()):
		if got[i] != sent[i]:
			check(false, "message %d differs" % i)
			break
	check(not a.dead() and not b.dead(), "dead link under 20% loss")
	print("kcp core: %d messages in order after %d virtual ms" % [sent.size(), clock[0]])
