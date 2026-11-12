package realtime

import "github.com/coder/websocket"

// Compression selects the permessage-deflate (RFC 7692) mode a server or Go
// client offers. Compression only takes effect when the peer supports it;
// otherwise the connection runs uncompressed, so enabling it never breaks a
// client. Size limits (MaxFrameBytes) apply to the decompressed message.
type Compression int

const (
	// CompressionDisabled does not negotiate permessage-deflate (default).
	CompressionDisabled Compression = iota
	// CompressionContextTakeover keeps a 32 KiB window across messages:
	// best ratio for repetitive JSON, but a fixed ~1.2 MB writer per
	// connection. Falls back to no-context-takeover if the peer requires it.
	CompressionContextTakeover
	// CompressionNoContextTakeover compresses each message independently
	// with pooled buffers: lower memory for many idle connections.
	CompressionNoContextTakeover
)

func (c Compression) mode() websocket.CompressionMode {
	switch c {
	case CompressionContextTakeover:
		return websocket.CompressionContextTakeover
	case CompressionNoContextTakeover:
		return websocket.CompressionNoContextTakeover
	}
	return websocket.CompressionDisabled
}
