package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/internal/tlscert"
)

// authContextKey marks a request that authenticated with a valid
// bearer token (as opposed to arriving from loopback). Downstream
// tier gates read it via requestAuthenticated. It is stamped only by
// the auth middleware -- unexported so no handler can forge it.
type authContextKey struct{}

// requestAuthenticated reports whether the auth middleware validated
// a bearer token for this request. Loopback requests are trusted
// without a token and are NOT marked authenticated; use adminAllowed
// / isLoopback for origin decisions rather than this alone.
func requestAuthenticated(r *http.Request) bool {
	v, _ := r.Context().Value(authContextKey{}).(bool)
	return v
}

// bearerToken extracts the token from an "Authorization: Bearer x"
// header, or "" if absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// tokenMatches compares a presented token against the configured
// secret in constant time. Both are SHA-256'd first so the compare
// operates on equal-length digests and a length difference cannot
// leak through timing.
func tokenMatches(presented, want string) bool {
	if want == "" || presented == "" {
		return false
	}
	p := sha256.Sum256([]byte(presented))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(p[:], w[:]) == 1
}

// authenticate is the global gate wrapping the whole mux. Loopback
// requests pass untouched (the local CLI, hooks, and MCP proxy keep
// working with no token). A non-loopback request must present a
// valid bearer token or gets 401; when it does, the request is
// stamped authenticated for downstream tier gates.
//
// It is safe to run even when remote access is disabled: with no
// remote listener, non-loopback requests cannot reach the process,
// so this only ever sees loopback traffic and is a passthrough.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !tokenMatches(bearerToken(r), s.cfg.Remote.Token) {
			// Distinguish authn (present a credential) from the
			// authz 403 the tier gates use, and tell the agent what
			// to do. WWW-Authenticate advertises the scheme.
			// Bare error (no curation/store-state envelope): an
			// unauthenticated caller must learn nothing about the
			// store.
			w.Header().Set("WWW-Authenticate", `Bearer realm="gramaton"`)
			s.writeBareError(w, http.StatusUnauthorized, "unauthorized",
				"remote access requires a valid bearer token; import a credentials bundle with `gramaton remote add`")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, true)))
	})
}

// adminAllowed reports whether the caller may invoke a path-taking
// admin operation (restore, store carve/add, session archive,
// path-mode ingest). Loopback always may; an authenticated remote
// may only when the operator opted in via server.remote.admin_ops.
// These operations materialize or read caller-influenced host paths,
// so a bearer token alone -- which proves identity, not path
// safety -- does not unlock them by default.
func (s *Server) adminAllowed(r *http.Request) bool {
	if isLoopback(r) {
		return true
	}
	return s.cfg.Remote.AdminOps && requestAuthenticated(r)
}

// writeAdminForbidden emits the standard 403 for an authenticated
// remote caller who hit a loopback-only or admin-gated operation.
func (s *Server) writeAdminForbidden(w http.ResponseWriter, op string) {
	s.writeError(w, http.StatusForbidden, "forbidden",
		op+" is not available to remote callers on this server (enable server.remote.admin_ops to allow it, or run it locally)", false)
}

// listenRemoteTLS opens the TLS listener for remote access. The
// certificate and key paths are already resolved (cli/serve.go fails
// closed if they are missing), so this only loads them and binds.
func (s *Server) listenRemoteTLS() (net.Listener, error) {
	tlsCfg, err := tlscert.ServerTLS(s.cfg.Remote.CertFile, s.cfg.Remote.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("remote tls: %w", err)
	}
	port := s.cfg.Remote.Port
	if port == 0 {
		port = config.DefaultRemotePort
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Remote.BindAddr, port)
	raw, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("remote listen on %s: %w", addr, err)
	}
	return tls.NewListener(raw, tlsCfg), nil
}
