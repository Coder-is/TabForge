package httptransport

import (
	"net/http"
	"strings"
	"time"
)

// Result excludes tokens, headers and payloads. Observe must be nonblocking.
type Result struct {
	RequestID, Endpoint string
	Code, TerminalEvent string
	Status              int
	Bytes               int64
	Duration            time.Duration
}
type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	code, event string
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}

func canFlush(w http.ResponseWriter) bool {
	for depth := 0; depth < 32; depth++ {
		if _, ok := w.(interface{ FlushError() error }); ok {
			return true
		}
		if _, ok := w.(http.Flusher); ok {
			return true
		}
		if wrapped, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			w = wrapped.Unwrap()
		} else {
			return false
		}
	}
	return false
}

// HTTPServer provides bounded connection/header handling. Per-endpoint response
// deadlines are installed by Server; a global WriteTimeout would truncate SSE.
func HTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}

// WithCORS allows only explicit origins. It does not replace bearer auth.
func WithCORS(handler http.Handler, origins []string) http.Handler {
	allowed := map[string]bool{}
	for _, origin := range origins {
		if origin != "*" && origin != "" {
			allowed[origin] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			handler.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !allowed[origin] {
			http.Error(w, "origin not allowed", 403)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, X-Protocol-Version, X-Protocol-Schema")
		if r.Method == "OPTIONS" {
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if r.Header.Get("Access-Control-Request-Method") != "POST" {
				http.Error(w, "method not allowed", 405)
				return
			}
			for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				switch strings.ToLower(strings.TrimSpace(header)) {
				case "", "authorization", "content-type", "x-request-id", "x-protocol-version", "x-protocol-schema", "accept":
				default:
					http.Error(w, "header not allowed", 403)
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-Protocol-Version, X-Protocol-Schema, Accept")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(204)
			return
		}
		handler.ServeHTTP(w, r)
	})
}
