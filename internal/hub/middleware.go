package hub

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"runtime/debug"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// securityHeaders sets a fixed set of security-related response headers
// on every response before delegating to next.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// cidrAllowlist rejects requests whose remote address (from
// http.Request.RemoteAddr only, never client-supplied headers) is not
// covered by any prefix in s.opts.AllowedCIDRs. A nil AllowedCIDRs
// allows every address.
func (s *Server) cidrAllowlist(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.AllowedCIDRs == nil {
			next.ServeHTTP(w, r)
			return
		}

		addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			// RemoteAddr without a port (e.g. some test transports); try
			// parsing as a bare address as a fallback.
			addr, addrErr := netip.ParseAddr(r.RemoteAddr)
			if addrErr != nil {
				writeJSON(w, http.StatusForbidden, models.APIError{Error: "forbidden"})
				return
			}
			addrPort = netip.AddrPortFrom(addr, 0)
		}

		addr := addrPort.Addr().Unmap()
		allowed := false
		for _, prefix := range s.opts.AllowedCIDRs {
			if prefix.Contains(addr) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, models.APIError{Error: "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// methodErrorWriter holds only the ServeMux-generated 405 response so it can
// be replaced with the API's JSON error shape. Other responses write through
// directly, and ServeMux's Allow header remains on the wrapped writer.
type methodErrorWriter struct {
	http.ResponseWriter
	methodNotAllowed bool
	wroteHeader      bool
}

func (w *methodErrorWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if code == http.StatusMethodNotAllowed {
		w.methodNotAllowed = true
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *methodErrorWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.methodNotAllowed {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

// jsonMethodNotAllowed replaces the standard library's plain-text 405 with
// an APIError while preserving the Allow header selected by http.ServeMux.
func (s *Server) jsonMethodNotAllowed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodErr := &methodErrorWriter{ResponseWriter: w}
		next.ServeHTTP(methodErr, r)
		if !methodErr.methodNotAllowed {
			return
		}

		w.Header().Del("Content-Length")
		writeJSON(w, http.StatusMethodNotAllowed, models.APIError{Error: "method not allowed"})
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code
// written, defaulting to 200 if WriteHeader is never called explicitly.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// requestLogger logs each request's method, path, status, duration, and
// remote address: at debug level for 2xx responses, info for >=400.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.opts.now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := s.opts.now().Sub(start)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", duration.Milliseconds(),
			"remote", r.RemoteAddr,
		}
		if rec.status >= 400 {
			s.logger.Info("http request", attrs...)
		} else {
			s.logger.Debug("http request", attrs...)
		}
	})
}

// recoverMiddleware converts a panicking handler into a JSON 500
// response, logging the panic value and stack trace.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
					"method", r.Method,
					"path", r.URL.Path,
				)
				writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as the JSON response body with status, setting
// Content-Type and Cache-Control headers appropriately for API
// responses.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// Encoding errors here mean the connection is already broken (client
	// gone, etc.); nothing meaningful can be done with the error at this
	// point since headers/status are already written.
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // best-effort write after headers are sent; nothing left to do on failure
}
