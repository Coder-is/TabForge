package realtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"time"
)

// kcpRetryPending is how many pending handshakes make the listener ask
// for cookies (see kcpcrypto.go): a quarter of MaxKCPPending. A variable
// for tests.
var kcpRetryPending = MaxKCPPending / 4

// kcpCookieEpoch is how long a cookie stays valid (one to two epochs).
const kcpCookieEpoch = 30 * time.Second

// kcpCookie binds a cookie to the client's address, the conversation and
// the client's ephemeral key, for one epoch.
func kcpCookie(secret []byte, addr netip.AddrPort, conv uint32, hello []byte, epoch int64) []byte {
	m := hmac.New(sha256.New, secret)
	ip := addr.Addr().Unmap().As16()
	m.Write(ip[:])
	var b [14]byte
	binary.LittleEndian.PutUint16(b[0:], addr.Port())
	binary.LittleEndian.PutUint32(b[2:], conv)
	binary.LittleEndian.PutUint64(b[6:], uint64(epoch))
	m.Write(b[:])
	m.Write(hello[6 : 6+kcpKeySize])
	return m.Sum(nil)[:kcpCookieSize]
}

func kcpCookieNow() int64 { return time.Now().Unix() / int64(kcpCookieEpoch/time.Second) }

// validCookie reports whether hello carries the cookie for its sender, from
// this epoch or the previous one.
func (l *kcpListener) validCookie(key kcpKey, hello []byte) bool {
	got := hello[kcpCookieOffset : kcpCookieOffset+kcpCookieSize]
	now := kcpCookieNow()
	for _, epoch := range []int64{now, now - 1} {
		if hmac.Equal(got, kcpCookie(l.cookieSecret, key.addr, key.conv, hello, epoch)) {
			return true
		}
	}
	return false
}

// sendRetry answers a hello with the cookie to repeat it with.
func (l *kcpListener) sendRetry(key kcpKey, hello []byte) {
	retry := make([]byte, 5, kcpRetrySize)
	binary.LittleEndian.PutUint32(retry, key.conv)
	retry[4] = kcpRetry
	l.write(append(retry, kcpCookie(l.cookieSecret, key.addr, key.conv, hello, kcpCookieNow())...), key.addr)
}
