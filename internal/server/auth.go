package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// apiTokenHeader carries the admin API token. It doubles as the CSRF token:
// a browser cannot set a custom header on a cross-site simple request.
const apiTokenHeader = "X-Dyrokol-Api"

// authorizeAPI reports whether the request carries the admin token, either
// in apiTokenHeader or as a bearer token.
func (s *Server) authorizeAPI(r *http.Request) bool {
	tok := r.Header.Get(apiTokenHeader)
	if tok == "" {
		tok, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	want := s.adminTokenHash()
	if tok == "" || want == "" {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// guard protects /api/ with the token. Static assets are not guarded so the
// login page can load before the token is known.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		// Defense in depth on top of the token: reject cross-site origins.
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		if !s.authorizeAPI(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid api token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether the request Origin matches the request host.
// The admin UI is served by this very process, so a matching origin (or a
// loopback origin for a loopback host) is expected; anything else is a
// cross-site request.
func sameOrigin(origin, host string) bool {
	rest := origin
	if _, tail, ok := strings.Cut(origin, "://"); ok {
		rest = tail
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == host {
		return true
	}
	oh, hh := hostOnly(rest), hostOnly(host)
	if oh == hh {
		return true
	}
	return isLoopback(oh) && isLoopback(hh)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}
