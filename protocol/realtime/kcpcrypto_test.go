package realtime

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// kcpVectors are fixed-key results of the encrypted KCP handshake, for
// implementers of other clients: given the three private keys and conv,
// a conforming client produces hello, derives the keys from welcome, and
// seals and opens the datagrams byte for byte. All byte strings are hex.
type kcpVectors struct {
	Comment                string `json:"comment"`
	Conv                   uint32 `json:"conv"`
	ServerStaticPrivate    string `json:"server_static_private"`
	ServerStaticPublic     string `json:"server_static_public"`
	ClientEphemeralPrivate string `json:"client_ephemeral_private"`
	ServerEphemeralPrivate string `json:"server_ephemeral_private"`
	Hello                  string `json:"hello"`
	Welcome                string `json:"welcome"`
	ClientToServerKey      string `json:"client_to_server_key"`
	ServerToClientKey      string `json:"server_to_client_key"`
	Datagrams              []struct {
		From      string `json:"from"`
		Counter   uint64 `json:"counter"`
		Plaintext string `json:"plaintext"`
		Sealed    string `json:"sealed"`
	} `json:"datagrams"`
}

const kcpVectorsFile = "testdata/kcp_crypto_vectors.json"

func fixedX25519(t *testing.T, fill byte) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestKCPCryptoVectors recomputes the vectors and compares them with the
// file; TABFORGE_UPDATE_VECTORS=1 rewrites it after an intended change.
func TestKCPCryptoVectors(t *testing.T) {
	const conv = 0x01020304
	static, clientEph, serverEph := fixedX25519(t, 0x11), fixedX25519(t, 0x22), fixedX25519(t, 0x33)
	h := &kcpClientHandshake{conv: conv, eph: clientEph, server: static.PublicKey(), hello: kcpHelloDatagram(conv, clientEph.PublicKey().Bytes(), static.PublicKey())}
	server, welcome, err := kcpAcceptWith(static, serverEph, conv, h.hello)
	if err != nil {
		t.Fatal(err)
	}
	client := h.finish(welcome)
	if client == nil {
		t.Fatal("client rejected the welcome")
	}
	ee, _ := clientEph.ECDH(serverEph.PublicKey())
	es, _ := clientEph.ECDH(static.PublicKey())
	keys, err := kcpKeyMaterial(conv, clientEph.PublicKey().Bytes(), serverEph.PublicKey().Bytes(), static.PublicKey().Bytes(), ee, es)
	if err != nil {
		t.Fatal(err)
	}
	got := kcpVectors{
		Comment:                "Encrypted KCP v1 (doc/realtime.md). Private keys are 32 repeated bytes; counters start at 1 in each direction; the empty client datagram is a keepalive.",
		Conv:                   conv,
		ServerStaticPrivate:    hex.EncodeToString(static.Bytes()),
		ServerStaticPublic:     hex.EncodeToString(static.PublicKey().Bytes()),
		ClientEphemeralPrivate: hex.EncodeToString(clientEph.Bytes()),
		ServerEphemeralPrivate: hex.EncodeToString(serverEph.Bytes()),
		Hello:                  hex.EncodeToString(h.hello),
		Welcome:                hex.EncodeToString(welcome),
		ClientToServerKey:      hex.EncodeToString(keys[:32]),
		ServerToClientKey:      hex.EncodeToString(keys[32:]),
	}
	for _, d := range []struct {
		from          string
		seal, open    *kcpCipher
		plain         []byte
		wantedCounter uint64
	}{
		{"client", client, server, []byte("first KCP datagram from the client"), 1},
		{"client", client, server, nil, 2},
		{"server", server, client, []byte("first KCP datagram from the server"), 1},
	} {
		sealed := d.seal.seal(d.plain)
		opened, ok := d.open.open(sealed)
		if !ok || !bytes.Equal(opened, d.plain) {
			t.Fatalf("%s datagram %d does not open", d.from, d.wantedCounter)
		}
		got.Datagrams = append(got.Datagrams, struct {
			From      string `json:"from"`
			Counter   uint64 `json:"counter"`
			Plaintext string `json:"plaintext"`
			Sealed    string `json:"sealed"`
		}{d.from, d.wantedCounter, hex.EncodeToString(d.plain), hex.EncodeToString(sealed)})
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if os.Getenv("TABFORGE_UPDATE_VECTORS") == "1" {
		os.MkdirAll(filepath.Dir(kcpVectorsFile), 0o755)
		if err := os.WriteFile(kcpVectorsFile, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(kcpVectorsFile)
	if err != nil {
		t.Fatalf("%v (generate with TABFORGE_UPDATE_VECTORS=1)", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), data) {
		t.Fatalf("vectors changed; if the wire format change is intended, regenerate with TABFORGE_UPDATE_VECTORS=1 and update every client.\ngot:\n%s", data)
	}
}

// TestKCPCryptoRFC8439 pins the AEAD to ChaCha20-Poly1305 with the
// RFC 8439 section 2.8.2 test vector, independent of this package's
// framing, so a swapped primitive cannot hide behind regenerated vectors.
func TestKCPCryptoRFC8439(t *testing.T) {
	key, _ := hex.DecodeString("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce, _ := hex.DecodeString("070000004041424344454647")
	aad, _ := hex.DecodeString("50515253c0c1c2c3c4c5c6c7")
	plain := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	wantTag := "1ae10b594f09e26a7e902ecbd0600691"
	c, err := kcpCipherFromKeys(1, append(append([]byte(nil), key...), key...), true)
	if err != nil {
		t.Fatal(err)
	}
	out := c.send.Seal(nil, nonce, plain, aad)
	if got := hex.EncodeToString(out[len(out)-16:]); got != wantTag {
		t.Fatalf("tag %s, want %s (RFC 8439 2.8.2)", got, wantTag)
	}
}
