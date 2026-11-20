package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brian-nunez/bkit/stash"
)

const (
	sessionCookie = "bb_session"
	sessionTTL    = 12 * time.Hour
)

// guard protects every request. A local endpoint that can read Docker is
// reachable by malicious pages through DNS rebinding and cross-site requests,
// so the server:
//
//   - answers only requests whose Host is its own loopback address;
//   - rejects requests with an Origin header that is not its own;
//   - requires a session cookie, obtained once by opening the URL bb serve
//     printed, which carries a random per-launch token.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ownHost(r.Host) {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)

			return
		}

		if o := r.Header.Get("Origin"); o != "" && !s.ownOrigin(o) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)

			return
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")

		if r.URL.Path == "/auth" {
			s.auth(w, r)

			return
		}

		if r.URL.Path == "/static/app.css" || r.URL.Path == "/static/icon.svg" {
			next.ServeHTTP(w, r) // the "open the link" page needs its styles

			return
		}

		if !s.validSession(r) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = needLink().Render(r.Context(), w)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// auth exchanges the launch token for a session cookie and redirects to the
// page asked for, so the token does not stay in the address bar or history.
func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	got := r.URL.Query().Get("token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		_ = needLink().Render(r.Context(), w)

		return
	}

	id := rand.Text()
	if err := s.sessions.Set(r.Context(), id, []byte("1"), stash.WithTTL(sessionTTL)); err != nil {
		http.Error(w, "could not start a session", http.StatusInternalServerError)

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	next := safeNextPath(r.URL.Query().Get("next"))
	w.Header().Set("Location", next)
	w.WriteHeader(http.StatusSeeOther)
}

// safeNextPath ensures post-auth redirects stay on a relative path on this
// server, rejecting scheme-relative ("//..."), backslash-prefixed ("/\..."),
// control characters, and absolute URLs.
func safeNextPath(raw string) string {
	if raw == "" || raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return "/"
	}

	if strings.ContainsAny(raw, "\r\n") {
		return "/"
	}

	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.Scheme != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}

	if strings.HasPrefix(u.Path, "//") || strings.HasPrefix(u.Path, `/\`) {
		return "/"
	}

	clean := &url.URL{Path: u.Path, RawQuery: u.RawQuery}

	out := clean.RequestURI()
	if strings.HasPrefix(out, "//") || strings.HasPrefix(out, `/\`) {
		return "/"
	}

	return out
}

func (s *Server) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}

	ok, err := s.sessions.Exists(context.WithoutCancel(r.Context()), c.Value)

	return err == nil && ok
}

func (s *Server) ownHost(host string) bool {
	h, port, err := net.SplitHostPort(host)
	if err != nil || port != s.port {
		return false
	}

	return h == "127.0.0.1" || h == "localhost"
}

func (s *Server) ownOrigin(origin string) bool {
	u, err := url.Parse(origin)

	return err == nil && u.Scheme == "http" && s.ownHost(u.Host)
}
