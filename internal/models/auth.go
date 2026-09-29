package models

// Session is a server-side dashboard login session. It is persisted by
// hub.Store implementations, keyed by IDHash (hex-encoded SHA-256 of the
// bearer token presented by the browser). IDHash is never serialized to
// JSON: callers that need to expose a session externally use SessionView
// instead.
type Session struct {
	IDHash    string `json:"-"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	ExpiresAt int64  `json:"expires_at"`
	Remote    string `json:"remote"`
	UserAgent string `json:"user_agent"`
}

// SessionView is the read-model for one session in the active-sessions
// list (GET /api/v1/auth/sessions): everything from Session except the
// full id hash (only its first 8 hex characters are exposed), plus
// whether it is the caller's own current session.
type SessionView struct {
	// ID is the first 8 hex characters of the session's IDHash, enough
	// to distinguish sessions in a list without exposing anything
	// usable to forge or look up the full session token.
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	ExpiresAt int64  `json:"expires_at"`
	Remote    string `json:"remote"`
	UserAgent string `json:"user_agent"`
	Current   bool   `json:"current"`
}

// LoginResponse is returned by a successful POST /api/v1/auth/login or
// POST /api/v1/auth/password: a fresh session token plus its expiry and
// whether the account still must change its password.
type LoginResponse struct {
	// Token is the bearer token for the new session, returned once; only
	// its hash is persisted server-side.
	Token              string `json:"token"`
	ExpiresAt          int64  `json:"expires_at"`
	MustChangePassword bool   `json:"must_change_password"`
	Username           string `json:"username"`
}

// MeResponse is returned by GET /api/v1/auth/me: identifies the
// authenticated caller and how they authenticated.
type MeResponse struct {
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
	// AuthMethod is "session" for a browser session bearer token or
	// "api_token" for a static CP_UI_TOKEN bearer token.
	AuthMethod string `json:"auth_method"`
	// SessionExpiresAt is the current session's expiry (unix seconds),
	// or 0 when AuthMethod is "api_token" (static tokens never expire).
	SessionExpiresAt int64 `json:"session_expires_at"`
}
