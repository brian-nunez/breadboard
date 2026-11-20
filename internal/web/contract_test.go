package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestWebContractSecurityHeadersAndGuards locks the loopback Host, Origin,
// session auth, and HTTP security header contract on every route.
func TestWebContractSecurityHeadersAndGuards(t *testing.T) {
	s := newServer(t)

	checkSecurityHeaders := func(t *testing.T, label string, h http.Header) {
		t.Helper()

		want := map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
		}
		for k, v := range want {
			if got := h.Get(k); got != v {
				t.Errorf("%s: header %s = %q, want %q", label, k, got, v)
			}
		}

		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: unexpected CSP %q", label, csp)
		}
	}

	// Unknown Host -> 421 Misdirected Request.
	rec := s.get(t, "/", map[string]string{"Host": "evil.example:" + s.port})
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("unknown Host status = %d, want %d", rec.Code, http.StatusMisdirectedRequest)
	}

	// Cross-origin Origin -> 403 Forbidden.
	rec = s.get(t, "/", map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// Public static assets required by the unauthenticated sign-in page stay accessible.
	for _, asset := range []string{"/static/app.css", "/static/icon.svg"} {
		rec = s.get(t, asset, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("unauthenticated %s = %d, want 200", asset, rec.Code)
		}

		checkSecurityHeaders(t, asset, rec.Header())
	}

	// Authenticated route contract.
	cookie := signIn(t, s)
	authHeader := map[string]string{"Cookie": cookie}

	routes := []struct {
		path        string
		wantCode    int
		wantContent string
	}{
		{"/", http.StatusOK, "text/html"},
		{"/home", http.StatusOK, "text/html"},
		{"/dashboard", http.StatusOK, "text/html"},
		{"/architecture", http.StatusOK, "text/html"},
		{"/starters", http.StatusOK, "text/html"},
		{"/docs", http.StatusOK, "text/html"},
		{"/docs/getting-started", http.StatusOK, "text/html"},
		{"/search-index", http.StatusOK, "application/json"},
		{"/does-not-exist", http.StatusNotFound, ""},
	}

	for _, tc := range routes {
		rec = s.get(t, tc.path, authHeader)
		if rec.Code != tc.wantCode {
			t.Errorf("GET %s = %d, want %d", tc.path, rec.Code, tc.wantCode)
		}

		checkSecurityHeaders(t, tc.path, rec.Header())

		if tc.wantContent != "" && !strings.Contains(rec.Header().Get("Content-Type"), tc.wantContent) {
			t.Errorf("GET %s Content-Type = %q, want substring %q", tc.path, rec.Header().Get("Content-Type"), tc.wantContent)
		}
	}

	// /search-index contract: valid non-empty JSON array of search entries.
	rec = s.get(t, "/search-index", authHeader)

	var entries []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("/search-index invalid JSON: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("/search-index returned empty list")
	}
}

// TestWebContractAuthRedirectSafety locks that /auth?next=... preserves safe
// local paths and neutralizes open-redirect payloads.
func TestWebContractAuthRedirectSafety(t *testing.T) {
	s := newServer(t)

	for _, tc := range []struct {
		next string
		want string
	}{
		{"/dashboard", "/dashboard"},
		{"/services/postgres?tab=explore", "/services/postgres?tab=explore"},
		{"", "/"},
		{"https://evil.example/phish", "/"},
		{"//evil.example/phish", "/"},
		{`/\evil.example/phish`, "/"},
		{"javascript:alert(1)", "/"},
		{"http://127.0.0.1:9999/other", "/"},
		{"/ok\r\nSet-Cookie: bad=1", "/"},
	} {
		path := "/auth?token=" + url.QueryEscape(s.token) + "&next=" + url.QueryEscape(tc.next)

		rec := s.get(t, path, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("next=%q status = %d, want 303", tc.next, rec.Code)

			continue
		}

		if got := rec.Header().Get("Location"); got != tc.want {
			t.Errorf("next=%q Location = %q, want %q", tc.next, got, tc.want)
		}
	}
}
