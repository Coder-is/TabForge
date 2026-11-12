package realtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync/atomic"
)

// Encrypted KCP, version 1. The server has a long-term X25519 key; clients
// are configured with its public key (Server.KCPKey, Client.KCPServerKey),
// which authenticates the server the way a pinned certificate would.
//
//	hello    conv | 0xFD | version 1 | client ephemeral key (32) | zero padding to 64 bytes
//	welcome  conv | 0xFC | server ephemeral key (32) | GCM tag of nothing (16)
//	sealed   conv | 0xFB | counter (8, little endian) | AES-256-GCM(KCP datagram)
//
// Both sides derive the keys from DH(client ephemeral, server ephemeral)
// and DH(client ephemeral, server static) with HKDF-SHA256 over the whole
// transcript, one key per direction, so a recording stays secret even if
// the server key leaks later. The welcome's tag (counter 0) proves the
// server holds its static key, so the client ignores forged welcomes.
// Hellos are padded so a welcome is never larger than the hello that
// caused it; the client repeats its hello until a welcome arrives and the
// server answers repeats with the same welcome.
//
// Every datagram after the handshake is sealed, KCP headers included, so
// forged acknowledgements or window updates are dropped like noise. A
// replayed datagram decrypts, but KCP discards segments it already has.
// Sealed datagrams with an empty body are keepalives. Resets (conv | 0xFF)
// stay in clear: a peer that lost the conversation no longer has its keys.
const (
	kcpHello       = 0xFD
	kcpWelcome     = 0xFC
	kcpSealed      = 0xFB
	kcpPlainRefuse = 0xFA // conv | 0xFA: this server only accepts encrypted KCP

	kcpCryptoVersion = 1
	kcpKeySize       = 32
	kcpHelloSize     = 64
	kcpWelcomeSize   = 5 + kcpKeySize + 16
	kcpSealedHeader  = 5 + 8
	kcpSealOverhead  = kcpSealedHeader + 16
)

var (
	// ErrKCPEncryptionRequired reports a plain KCP client talking to a
	// server that has Server.KCPKey set.
	ErrKCPEncryptionRequired = errors.New("realtime: the server only accepts encrypted KCP; set Client.KCPServerKey")
	// ErrKCPEncryptionUnsupported reports an encrypting client talking to a
	// server without Server.KCPKey.
	ErrKCPEncryptionUnsupported = errors.New("realtime: the server does not offer encrypted KCP; set Server.KCPKey or clear Client.KCPServerKey")
	errKCPKeyCurve              = errors.New("realtime: KCP keys must be X25519 (ecdh.X25519())")
)

// kcpCipher seals and opens the datagrams of one conversation.
type kcpCipher struct {
	conv       uint32
	send, recv cipher.AEAD
	sent       atomic.Uint64 // last counter used; 0 is the welcome's
}

// kcpTranscriptKeys derives the per-direction ciphers. clientEph and
// serverEph are the public ephemeral keys; ee and es the two DH results.
func kcpTranscriptKeys(conv uint32, clientEph, serverEph, serverStatic, ee, es []byte, client bool) (*kcpCipher, error) {
	h := sha256.New()
	h.Write([]byte("tabforge-kcp-v1"))
	binary.Write(h, binary.LittleEndian, conv)
	h.Write(clientEph)
	h.Write(serverEph)
	h.Write(serverStatic)
	keys, err := hkdf.Key(sha256.New, append(append([]byte(nil), ee...), es...), h.Sum(nil), "keys", 2*kcpKeySize)
	if err != nil {
		return nil, err
	}
	c2s, err := newGCM(keys[:kcpKeySize])
	if err != nil {
		return nil, err
	}
	s2c, err := newGCM(keys[kcpKeySize:])
	if err != nil {
		return nil, err
	}
	if client {
		return &kcpCipher{conv: conv, send: c2s, recv: s2c}, nil
	}
	return &kcpCipher{conv: conv, send: s2c, recv: c2s}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func kcpNonce(counter uint64) []byte {
	nonce := make([]byte, 12)
	binary.LittleEndian.PutUint64(nonce[4:], counter)
	return nonce
}

// seal wraps one KCP datagram (or nothing, for a keepalive).
func (k *kcpCipher) seal(plain []byte) []byte {
	counter := k.sent.Add(1)
	out := make([]byte, kcpSealedHeader, kcpSealOverhead+len(plain))
	binary.LittleEndian.PutUint32(out, k.conv)
	out[4] = kcpSealed
	binary.LittleEndian.PutUint64(out[5:], counter)
	return k.send.Seal(out, kcpNonce(counter), plain, out[:kcpSealedHeader])
}

// open authenticates and decrypts a sealed datagram.
func (k *kcpCipher) open(data []byte) ([]byte, bool) {
	if len(data) < kcpSealOverhead || data[4] != kcpSealed {
		return nil, false
	}
	counter := binary.LittleEndian.Uint64(data[5:])
	if counter == 0 {
		return nil, false // reserved for the welcome
	}
	plain, err := k.recv.Open(nil, kcpNonce(counter), data[kcpSealedHeader:], data[:kcpSealedHeader])
	return plain, err == nil
}

func kcpHelloDatagram(conv uint32, eph []byte) []byte {
	b := make([]byte, kcpHelloSize)
	binary.LittleEndian.PutUint32(b, conv)
	b[4], b[5] = kcpHello, kcpCryptoVersion
	copy(b[6:], eph)
	return b
}

func isKCPHello(data []byte) bool {
	return len(data) == kcpHelloSize && data[4] == kcpHello && data[5] == kcpCryptoVersion
}

// kcpAccept answers a hello: it returns the cipher and the welcome to send
// (and to repeat if the hello is repeated).
func kcpAccept(key *ecdh.PrivateKey, conv uint32, hello []byte) (*kcpCipher, []byte, error) {
	curve := ecdh.X25519()
	clientEph, err := curve.NewPublicKey(hello[6 : 6+kcpKeySize])
	if err != nil {
		return nil, nil, err
	}
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	ee, err := eph.ECDH(clientEph)
	if err != nil {
		return nil, nil, err // low-order point
	}
	es, err := key.ECDH(clientEph)
	if err != nil {
		return nil, nil, err
	}
	k, err := kcpTranscriptKeys(conv, clientEph.Bytes(), eph.PublicKey().Bytes(), key.PublicKey().Bytes(), ee, es, false)
	if err != nil {
		return nil, nil, err
	}
	welcome := make([]byte, 5+kcpKeySize, kcpWelcomeSize)
	binary.LittleEndian.PutUint32(welcome, conv)
	welcome[4] = kcpWelcome
	copy(welcome[5:], eph.PublicKey().Bytes())
	welcome = k.send.Seal(welcome, kcpNonce(0), nil, welcome)
	return k, welcome, nil
}

// kcpClientHandshake holds the client's half until the welcome arrives.
type kcpClientHandshake struct {
	conv   uint32
	eph    *ecdh.PrivateKey
	server *ecdh.PublicKey
	hello  []byte
}

func newKCPClientHandshake(conv uint32, server *ecdh.PublicKey) (*kcpClientHandshake, error) {
	if server.Curve() != ecdh.X25519() {
		return nil, errKCPKeyCurve
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &kcpClientHandshake{conv: conv, eph: eph, server: server, hello: kcpHelloDatagram(conv, eph.PublicKey().Bytes())}, nil
}

// finish checks a welcome; nil means it was not a genuine welcome.
func (h *kcpClientHandshake) finish(welcome []byte) *kcpCipher {
	if len(welcome) != kcpWelcomeSize || welcome[4] != kcpWelcome || binary.LittleEndian.Uint32(welcome) != h.conv {
		return nil
	}
	serverEph, err := ecdh.X25519().NewPublicKey(welcome[5 : 5+kcpKeySize])
	if err != nil {
		return nil
	}
	ee, err := h.eph.ECDH(serverEph)
	if err != nil {
		return nil
	}
	es, err := h.eph.ECDH(h.server)
	if err != nil {
		return nil
	}
	k, err := kcpTranscriptKeys(h.conv, h.eph.PublicKey().Bytes(), serverEph.Bytes(), h.server.Bytes(), ee, es, true)
	if err != nil {
		return nil
	}
	header := welcome[:5+kcpKeySize]
	if _, err := k.recv.Open(nil, kcpNonce(0), welcome[len(header):], header); err != nil {
		return nil
	}
	return k
}
