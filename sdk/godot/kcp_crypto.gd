extends RefCounted
## Encrypted KCP, version 1 (doc/realtime.md, protocol/realtime/kcpcrypto.go):
## X25519 handshake, HKDF-SHA256 keys and ChaCha20-Poly1305 datagrams.
## Godot has no X25519 or ChaCha20-Poly1305, so both are written out here,
## constant-time (no secret-dependent branches or table lookups). HMAC and
## SHA-256 come from Godot's Crypto and HashingContext.

const HELLO := 0xFD
const WELCOME := 0xFC
const SEALED := 0xFB
const PLAIN_REFUSE := 0xFA
const KEEPALIVE := 0xFE
const RESET := 0xFF
const VERSION := 1
const KEY_SIZE := 32
const HELLO_SIZE := 64
const KEY_ID_SIZE := 8
const WELCOME_SIZE := 5 + KEY_SIZE + 16
const SEALED_HEADER := 5 + 8
const SEAL_OVERHEAD := SEALED_HEADER + 16
const U32 := 0xFFFFFFFF

static var _crypto := Crypto.new()

static func random_bytes(n: int) -> PackedByteArray:
	return _crypto.generate_random_bytes(n)

# ---- X25519 (RFC 7748), after TweetNaCl: 16 limbs of 16 bits ----

static func _car(o: PackedInt64Array) -> void:
	for i in 16:
		o[i] += 1 << 16
		var c := o[i] >> 16
		if i < 15:
			o[i + 1] += c - 1
		else:
			o[0] += 38 * (c - 1)
		o[i] -= c << 16

static func _sel(p: PackedInt64Array, q: PackedInt64Array, b: int) -> void:
	var c := ~(b - 1)
	for i in 16:
		var t := c & (p[i] ^ q[i])
		p[i] ^= t
		q[i] ^= t

static func _pack(n: PackedInt64Array) -> PackedByteArray:
	var m := PackedInt64Array()
	m.resize(16)
	var t := n.duplicate()
	_car(t)
	_car(t)
	_car(t)
	for j in 2:
		m[0] = t[0] - 0xffed
		for i in range(1, 15):
			m[i] = t[i] - 0xffff - ((m[i - 1] >> 16) & 1)
			m[i - 1] &= 0xffff
		m[15] = t[15] - 0x7fff - ((m[14] >> 16) & 1)
		var b := (m[15] >> 16) & 1
		m[14] &= 0xffff
		_sel(t, m, 1 - b)
	var o := PackedByteArray()
	o.resize(32)
	for i in 16:
		o[2 * i] = t[i] & 0xff
		o[2 * i + 1] = (t[i] >> 8) & 0xff
	return o

static func _unpack(n: PackedByteArray) -> PackedInt64Array:
	var o := PackedInt64Array()
	o.resize(16)
	for i in 16:
		o[i] = n[2 * i] + (n[2 * i + 1] << 8)
	o[15] &= 0x7fff
	return o

static func _add(o: PackedInt64Array, a: PackedInt64Array, b: PackedInt64Array) -> void:
	for i in 16:
		o[i] = a[i] + b[i]

static func _sub(o: PackedInt64Array, a: PackedInt64Array, b: PackedInt64Array) -> void:
	for i in 16:
		o[i] = a[i] - b[i]

static func _mul(o: PackedInt64Array, a: PackedInt64Array, b: PackedInt64Array) -> void:
	var t := PackedInt64Array()
	t.resize(31)
	for i in 16:
		var ai := a[i]
		for j in 16:
			t[i + j] += ai * b[j]
	for i in 15:
		t[i] += 38 * t[i + 16]
	for i in 16:
		o[i] = t[i]
	_car(o)
	_car(o)

static func _inv(o: PackedInt64Array, x: PackedInt64Array) -> void:
	var c := x.duplicate()
	for a in range(253, -1, -1):
		_mul(c, c, c)
		if a != 2 and a != 4:
			_mul(c, c, x)
	for i in 16:
		o[i] = c[i]

static func _limbs() -> PackedInt64Array:
	var a := PackedInt64Array()
	a.resize(16)
	return a

## X25519(scalar, point); an empty array for a low-order point (all-zero result).
static func x25519(scalar: PackedByteArray, point: PackedByteArray) -> PackedByteArray:
	var z := scalar.duplicate()
	z[31] = (z[31] & 127) | 64
	z[0] &= 248
	var x := _unpack(point)
	var a := _limbs()
	var b := x.duplicate()
	var c := _limbs()
	var d := _limbs()
	var e := _limbs()
	var f := _limbs()
	var a24 := _limbs()
	a24[0] = 0xDB41
	a24[1] = 1
	a[0] = 1
	d[0] = 1
	for i in range(254, -1, -1):
		var r := (z[i >> 3] >> (i & 7)) & 1
		_sel(a, b, r)
		_sel(c, d, r)
		_add(e, a, c)
		_sub(a, a, c)
		_add(c, b, d)
		_sub(b, b, d)
		_mul(d, e, e)
		_mul(f, a, a)
		_mul(a, c, a)
		_mul(c, b, e)
		_add(e, a, c)
		_sub(a, a, c)
		_mul(b, a, a)
		_sub(c, d, f)
		_mul(a, c, a24)
		_add(a, a, d)
		_mul(c, c, a)
		_mul(a, d, f)
		_mul(d, b, x)
		_mul(b, e, e)
		_sel(a, b, r)
		_sel(c, d, r)
	_inv(c, c)
	_mul(a, a, c)
	var q := _pack(a)
	var any := 0
	for v in q:
		any |= v
	return PackedByteArray() if any == 0 else q

static func x25519_public(private_key: PackedByteArray) -> PackedByteArray:
	var base := PackedByteArray()
	base.resize(32)
	base[0] = 9
	return x25519(private_key, base)

# ---- ChaCha20 (RFC 8439) ----

static func _quarter(x: PackedInt64Array, a: int, b: int, c: int, d: int) -> void:
	var t: int
	x[a] = (x[a] + x[b]) & U32
	t = x[d] ^ x[a]
	x[d] = ((t << 16) | (t >> 16)) & U32
	x[c] = (x[c] + x[d]) & U32
	t = x[b] ^ x[c]
	x[b] = ((t << 12) | (t >> 20)) & U32
	x[a] = (x[a] + x[b]) & U32
	t = x[d] ^ x[a]
	x[d] = ((t << 8) | (t >> 24)) & U32
	x[c] = (x[c] + x[d]) & U32
	t = x[b] ^ x[c]
	x[b] = ((t << 7) | (t >> 25)) & U32

static func chacha_block(key: PackedByteArray, counter: int, nonce: PackedByteArray) -> PackedByteArray:
	var s := PackedInt64Array([0x61707865, 0x3320646e, 0x79622d32, 0x6b206574])
	s.resize(16)
	for i in 8:
		s[4 + i] = key.decode_u32(4 * i)
	s[12] = counter & U32
	s[13] = nonce.decode_u32(0)
	s[14] = nonce.decode_u32(4)
	s[15] = nonce.decode_u32(8)
	var x := s.duplicate()
	for i in 10:
		_quarter(x, 0, 4, 8, 12)
		_quarter(x, 1, 5, 9, 13)
		_quarter(x, 2, 6, 10, 14)
		_quarter(x, 3, 7, 11, 15)
		_quarter(x, 0, 5, 10, 15)
		_quarter(x, 1, 6, 11, 12)
		_quarter(x, 2, 7, 8, 13)
		_quarter(x, 3, 4, 9, 14)
	var out := PackedByteArray()
	out.resize(64)
	for i in 16:
		out.encode_u32(4 * i, (x[i] + s[i]) & U32)
	return out

static func _chacha_xor(key: PackedByteArray, nonce: PackedByteArray, data: PackedByteArray) -> PackedByteArray:
	var out := data.duplicate()
	var counter := 1
	var done := 0
	while done < data.size():
		var block := chacha_block(key, counter, nonce)
		var n := mini(64, data.size() - done)
		for i in n:
			out[done + i] = data[done + i] ^ block[i]
		done += 64
		counter += 1
	return out

# ---- Poly1305 (RFC 8439), after poly1305-donna 32-bit ----

static func poly1305(key: PackedByteArray, m: PackedByteArray) -> PackedByteArray:
	var r0 := key.decode_u32(0) & 0x3ffffff
	var r1 := (key.decode_u32(3) >> 2) & 0x3ffff03
	var r2 := (key.decode_u32(6) >> 4) & 0x3ffc0ff
	var r3 := (key.decode_u32(9) >> 6) & 0x3f03fff
	var r4 := (key.decode_u32(12) >> 8) & 0x00fffff
	var s1 := r1 * 5
	var s2 := r2 * 5
	var s3 := r3 * 5
	var s4 := r4 * 5
	var h0 := 0
	var h1 := 0
	var h2 := 0
	var h3 := 0
	var h4 := 0
	var block := PackedByteArray()
	block.resize(16)
	var at := 0
	while at < m.size():
		var n := mini(16, m.size() - at)
		var hibit := 1 << 24
		for i in n:
			block[i] = m[at + i]
		if n < 16:
			block[n] = 1
			for i in range(n + 1, 16):
				block[i] = 0
			hibit = 0
		h0 += block.decode_u32(0) & 0x3ffffff
		h1 += (block.decode_u32(3) >> 2) & 0x3ffffff
		h2 += (block.decode_u32(6) >> 4) & 0x3ffffff
		h3 += (block.decode_u32(9) >> 6) & 0x3ffffff
		h4 += (block.decode_u32(12) >> 8) | hibit
		var d0 := h0 * r0 + h1 * s4 + h2 * s3 + h3 * s2 + h4 * s1
		var d1 := h0 * r1 + h1 * r0 + h2 * s4 + h3 * s3 + h4 * s2
		var d2 := h0 * r2 + h1 * r1 + h2 * r0 + h3 * s4 + h4 * s3
		var d3 := h0 * r3 + h1 * r2 + h2 * r1 + h3 * r0 + h4 * s4
		var d4 := h0 * r4 + h1 * r3 + h2 * r2 + h3 * r1 + h4 * r0
		var c := d0 >> 26
		h0 = d0 & 0x3ffffff
		d1 += c
		c = d1 >> 26
		h1 = d1 & 0x3ffffff
		d2 += c
		c = d2 >> 26
		h2 = d2 & 0x3ffffff
		d3 += c
		c = d3 >> 26
		h3 = d3 & 0x3ffffff
		d4 += c
		c = d4 >> 26
		h4 = d4 & 0x3ffffff
		h0 += c * 5
		c = h0 >> 26
		h0 &= 0x3ffffff
		h1 += c
		at += 16
	var c := h1 >> 26
	h1 &= 0x3ffffff
	h2 += c
	c = h2 >> 26
	h2 &= 0x3ffffff
	h3 += c
	c = h3 >> 26
	h3 &= 0x3ffffff
	h4 += c
	c = h4 >> 26
	h4 &= 0x3ffffff
	h0 += c * 5
	c = h0 >> 26
	h0 &= 0x3ffffff
	h1 += c
	var g0 := h0 + 5
	c = g0 >> 26
	g0 &= 0x3ffffff
	var g1 := h1 + c
	c = g1 >> 26
	g1 &= 0x3ffffff
	var g2 := h2 + c
	c = g2 >> 26
	g2 &= 0x3ffffff
	var g3 := h3 + c
	c = g3 >> 26
	g3 &= 0x3ffffff
	var g4 := (h4 + c - (1 << 26)) & U32
	var mask := ((g4 >> 31) - 1) & U32
	g0 &= mask
	g1 &= mask
	g2 &= mask
	g3 &= mask
	g4 &= mask
	var keep := mask ^ U32
	h0 = (h0 & keep) | g0
	h1 = (h1 & keep) | g1
	h2 = (h2 & keep) | g2
	h3 = (h3 & keep) | g3
	h4 = (h4 & keep) | g4
	h0 = (h0 | (h1 << 26)) & U32
	h1 = ((h1 >> 6) | (h2 << 20)) & U32
	h2 = ((h2 >> 12) | (h3 << 14)) & U32
	h3 = ((h3 >> 18) | (h4 << 8)) & U32
	var f := h0 + key.decode_u32(16)
	h0 = f & U32
	f = h1 + key.decode_u32(20) + (f >> 32)
	h1 = f & U32
	f = h2 + key.decode_u32(24) + (f >> 32)
	h2 = f & U32
	f = h3 + key.decode_u32(28) + (f >> 32)
	h3 = f & U32
	var tag := PackedByteArray()
	tag.resize(16)
	tag.encode_u32(0, h0)
	tag.encode_u32(4, h1)
	tag.encode_u32(8, h2)
	tag.encode_u32(12, h3)
	return tag

# ---- AEAD_CHACHA20_POLY1305 (RFC 8439 2.8) ----

static func _pad16(b: PackedByteArray) -> PackedByteArray:
	var out := b.duplicate()
	out.resize((b.size() + 15) & ~15)
	return out

static func _aead_tag(key: PackedByteArray, nonce: PackedByteArray, aad: PackedByteArray, ct: PackedByteArray) -> PackedByteArray:
	var poly_key := chacha_block(key, 0, nonce).slice(0, 32)
	var mac := _pad16(aad)
	mac.append_array(_pad16(ct))
	var lengths := PackedByteArray()
	lengths.resize(16)
	lengths.encode_u64(0, aad.size())
	lengths.encode_u64(8, ct.size())
	mac.append_array(lengths)
	return poly1305(poly_key, mac)

## Ciphertext followed by the 16-byte tag.
static func seal(key: PackedByteArray, nonce: PackedByteArray, aad: PackedByteArray, plain: PackedByteArray) -> PackedByteArray:
	var ct := _chacha_xor(key, nonce, plain)
	var out := ct.duplicate()
	out.append_array(_aead_tag(key, nonce, aad, ct))
	return out

## The plaintext, or an empty Array if it does not authenticate.
static func open(key: PackedByteArray, nonce: PackedByteArray, aad: PackedByteArray, sealed: PackedByteArray) -> Array:
	if sealed.size() < 16:
		return []
	var ct := sealed.slice(0, sealed.size() - 16)
	var tag := _aead_tag(key, nonce, aad, ct)
	var diff := 0
	for i in 16:
		diff |= tag[i] ^ sealed[ct.size() + i]
	if diff != 0:
		return []
	return [_chacha_xor(key, nonce, ct)]

# ---- the KCP handshake ----

static func nonce(counter: int) -> PackedByteArray:
	var n := PackedByteArray()
	n.resize(12)
	n.encode_u64(4, counter)
	return n

static func _sha256(data: PackedByteArray) -> PackedByteArray:
	var h := HashingContext.new()
	h.start(HashingContext.HASH_SHA256)
	h.update(data)
	return h.finish()

static func _hmac(key: PackedByteArray, data: PackedByteArray) -> PackedByteArray:
	return _crypto.hmac_digest(HashingContext.HASH_SHA256, key, data)

## Client-to-server key followed by server-to-client key (kcpKeyMaterial in Go).
static func key_material(conv: int, client_eph: PackedByteArray, server_eph: PackedByteArray, server_static: PackedByteArray, ee: PackedByteArray, es: PackedByteArray) -> PackedByteArray:
	var transcript := "tabforge-kcp-v1".to_ascii_buffer()
	var conv_bytes := PackedByteArray()
	conv_bytes.resize(4)
	conv_bytes.encode_u32(0, conv)
	transcript.append_array(conv_bytes)
	transcript.append_array(client_eph)
	transcript.append_array(server_eph)
	transcript.append_array(server_static)
	var ikm := ee.duplicate()
	ikm.append_array(es)
	var prk := _hmac(_sha256(transcript), ikm)
	var info := "keys".to_ascii_buffer()
	var t1_in := info.duplicate()
	t1_in.append(1)
	var t1 := _hmac(prk, t1_in)
	var t2_in := t1.duplicate()
	t2_in.append_array(info)
	t2_in.append(2)
	var out := t1.duplicate()
	out.append_array(_hmac(prk, t2_in))
	return out

## First 8 bytes of SHA-256(server public key): lets a rotating server pick the key.
static func key_id(server_public: PackedByteArray) -> PackedByteArray:
	return _sha256(server_public).slice(0, KEY_ID_SIZE)

static func hello_datagram(conv: int, client_eph_public: PackedByteArray, server_public: PackedByteArray) -> PackedByteArray:
	var b := PackedByteArray()
	b.resize(HELLO_SIZE)
	b.encode_u32(0, conv)
	b[4] = HELLO
	b[5] = VERSION
	for i in KEY_SIZE:
		b[6 + i] = client_eph_public[i]
	var id := key_id(server_public)
	for i in KEY_ID_SIZE:
		b[6 + KEY_SIZE + i] = id[i]
	return b

## Seals and opens the datagrams of one encrypted conversation.
class Cipher extends RefCounted:
	const Crypto_ = preload("kcp_crypto.gd")
	var conv := 0
	var send_key: PackedByteArray
	var recv_key: PackedByteArray
	var sent := 0 # last counter used; 0 is the welcome's

	func _init(conversation: int, keys: PackedByteArray, client: bool) -> void:
		conv = conversation
		var c2s := keys.slice(0, 32)
		var s2c := keys.slice(32, 64)
		send_key = c2s if client else s2c
		recv_key = s2c if client else c2s

	## conv | 0xFB | counter | ChaCha20-Poly1305(plain), the header as associated data.
	func seal(plain: PackedByteArray) -> PackedByteArray:
		sent += 1
		var header := PackedByteArray()
		header.resize(Crypto_.SEALED_HEADER)
		header.encode_u32(0, conv)
		header[4] = Crypto_.SEALED
		header.encode_u64(5, sent)
		var out := header.duplicate()
		out.append_array(Crypto_.seal(send_key, Crypto_.nonce(sent), header, plain))
		return out

	## [plaintext] of a sealed datagram, or [].
	func open(data: PackedByteArray) -> Array:
		if data.size() < Crypto_.SEAL_OVERHEAD or data[4] != Crypto_.SEALED:
			return []
		var counter := data.decode_u64(5)
		if counter == 0:
			return [] # reserved for the welcome
		return Crypto_.open(recv_key, Crypto_.nonce(counter), data.slice(0, Crypto_.SEALED_HEADER), data.slice(Crypto_.SEALED_HEADER))

## The client's half of the handshake, kept until a welcome arrives.
class ClientHandshake extends RefCounted:
	const Crypto_ = preload("kcp_crypto.gd")
	var conv := 0
	var hello: PackedByteArray
	var _eph_private: PackedByteArray
	var _eph_public: PackedByteArray
	var _server: PackedByteArray

	func _init(conversation: int, server_public: PackedByteArray, ephemeral_private: PackedByteArray = PackedByteArray()) -> void:
		conv = conversation
		_server = server_public
		_eph_private = ephemeral_private if not ephemeral_private.is_empty() else Crypto_.random_bytes(Crypto_.KEY_SIZE)
		_eph_public = Crypto_.x25519_public(_eph_private)
		hello = Crypto_.hello_datagram(conv, _eph_public, _server)

	## [Cipher] if data is this conversation's genuine welcome, else [].
	func finish(data: PackedByteArray) -> Array:
		if data.size() != Crypto_.WELCOME_SIZE or data[4] != Crypto_.WELCOME or data.decode_u32(0) != conv:
			return []
		var server_eph := data.slice(5, 5 + Crypto_.KEY_SIZE)
		var ee := Crypto_.x25519(_eph_private, server_eph)
		var es := Crypto_.x25519(_eph_private, _server)
		if ee.is_empty() or es.is_empty():
			return []
		var cipher = Cipher.new(conv, Crypto_.key_material(conv, _eph_public, server_eph, _server, ee, es), true)
		var header := data.slice(0, 5 + Crypto_.KEY_SIZE)
		if Crypto_.open(cipher.recv_key, Crypto_.nonce(0), header, data.slice(5 + Crypto_.KEY_SIZE)).is_empty():
			return []
		return [cipher]
