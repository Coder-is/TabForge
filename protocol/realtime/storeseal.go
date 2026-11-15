package realtime

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Session snapshots in a SessionStore are sealed when Server.StoreKey is
// set: XChaCha20-Poly1305 under a key derived from StoreKey, with the store
// key (which hashes the session id and resume token) as associated data.
// Whoever can write to the store can then neither forge a session for any
// user nor move a snapshot to another session, and whoever can read it
// sees no payloads or application state.
//
//	sealed = "tfs1" | nonce (24) | ciphertext and tag
const storeSealMagic = "tfs1"

// StoreKeySize is the length of Server.StoreKey and StorePreviousKeys.
const StoreKeySize = 32

func storeAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != StoreKeySize {
		return nil, fmt.Errorf("realtime: StoreKey must be %d bytes, not %d", StoreKeySize, len(key))
	}
	derived, err := hkdf.Key(sha256.New, key, nil, "tabforge-realtime-store-v1", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(derived)
}

// sealSnapshot seals data stored under name, or returns it as is without
// a StoreKey.
func (s *Server) sealSnapshot(name string, data []byte) ([]byte, error) {
	if s.StoreKey == nil {
		return data, nil
	}
	aead, err := storeAEAD(s.StoreKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(storeSealMagic)+chacha20poly1305.NonceSizeX, len(storeSealMagic)+chacha20poly1305.NonceSizeX+len(data)+chacha20poly1305.Overhead)
	copy(out, storeSealMagic)
	nonce := out[len(storeSealMagic):]
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(out, nonce, data, []byte(name)), nil
}

// openSnapshot reverses sealSnapshot, trying StoreKey and then
// StorePreviousKeys. With a key configured, unsealed snapshots are refused:
// anyone who can write to the store could have written them.
func (s *Server) openSnapshot(name string, data []byte) ([]byte, error) {
	sealed := bytes.HasPrefix(data, []byte(storeSealMagic))
	if s.StoreKey == nil && len(s.StorePreviousKeys) == 0 {
		if sealed {
			return nil, errors.New("snapshot is sealed but this server has no StoreKey")
		}
		return data, nil
	}
	if !sealed || len(data) < len(storeSealMagic)+chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, errors.New("snapshot is not sealed, but StoreKey is set")
	}
	nonce := data[len(storeSealMagic) : len(storeSealMagic)+chacha20poly1305.NonceSizeX]
	body := data[len(storeSealMagic)+chacha20poly1305.NonceSizeX:]
	for _, key := range append([][]byte{s.StoreKey}, s.StorePreviousKeys...) {
		if key == nil {
			continue
		}
		aead, err := storeAEAD(key)
		if err != nil {
			return nil, err
		}
		if plain, err := aead.Open(nil, nonce, body, []byte(name)); err == nil {
			return plain, nil
		}
	}
	return nil, errors.New("snapshot does not open with StoreKey or StorePreviousKeys")
}
