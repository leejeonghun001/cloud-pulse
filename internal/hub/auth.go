package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, returning ok=false if the header is missing or malformed.
func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// constantTimeEqual reports whether a and b are equal, in constant time
// regardless of their lengths. It hashes both operands first so the
// subtle.ConstantTimeCompare call always compares equal-length buffers,
// avoiding a length-based timing side channel.
func constantTimeEqual(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// unauthorized writes a 401 JSON response with a WWW-Authenticate
// header.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, models.APIError{Error: "unauthorized"})
}

// requireAgentToken wraps next, requiring a valid bearer token matching
// s.opts.AgentToken.
func (s *Server) requireAgentToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !constantTimeEqual(token, s.opts.AgentToken) {
			unauthorized(w)
			return
		}
		next(w, r)
	}
}

// requireUIToken wraps next, requiring a valid bearer token matching
// s.opts.UIToken when UIToken is non-empty. When UIToken is empty, the
// read API is unauthenticated.
func (s *Server) requireUIToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.UIToken == "" {
			next(w, r)
			return
		}
		token, ok := bearerToken(r)
		if !ok || !constantTimeEqual(token, s.opts.UIToken) {
			unauthorized(w)
			return
		}
		next(w, r)
	}
}
