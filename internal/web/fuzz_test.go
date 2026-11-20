package web

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzSafeNextPath guarantees safeNextPath never panics and can never produce
// an external redirect, scheme-relative URL, backslash-bypass URL, or CRLF
// header injection.
func FuzzSafeNextPath(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"/dashboard",
		"/services/postgres?tab=explore",
		"https://evil.example/x",
		"//evil.example/x",
		`/\evil.example/x`,
		`\/evil.example/x`,
		"javascript:alert(1)",
		"/ok\r\nLocation: https://evil.example",
		"/%2f%2fevil.example",
		"///evil.example",
		"/a?b=c#frag",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got := safeNextPath(raw)
		if got == "" || got[0] != '/' {
			t.Fatalf("safeNextPath(%q) = %q: must start with '/'", raw, got)
		}

		if strings.HasPrefix(got, "//") || strings.HasPrefix(got, `/\`) {
			t.Fatalf("safeNextPath(%q) = %q: forbidden prefix", raw, got)
		}

		if strings.ContainsAny(got, "\r\n") {
			t.Fatalf("safeNextPath(%q) = %q: contains CRLF", raw, got)
		}

		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("safeNextPath(%q) = %q: unparseable: %v", raw, got, err)
		}

		if u.IsAbs() || u.Scheme != "" || u.Host != "" {
			t.Fatalf("safeNextPath(%q) = %q: external target scheme=%q host=%q", raw, got, u.Scheme, u.Host)
		}
	})
}

// FuzzOwnHostAndOrigin guarantees host/origin validation never panics and only
// ever accepts the server's exact loopback host and port.
func FuzzOwnHostAndOrigin(f *testing.F) {
	f.Add("127.0.0.1:7777", "http://127.0.0.1:7777")
	f.Add("localhost:7777", "http://localhost:7777")
	f.Add("evil.com:7777", "https://127.0.0.1:7777")
	f.Add("127.0.0.1:8888", "http://127.0.0.1:7777@evil.com")
	f.Add("", "")

	s := &Server{port: "7777"}

	f.Fuzz(func(t *testing.T, host, origin string) {
		if s.ownHost(host) && host != "127.0.0.1:7777" && host != "localhost:7777" {
			t.Fatalf("ownHost accepted unexpected host %q", host)
		}

		if s.ownOrigin(origin) && origin != "http://127.0.0.1:7777" && origin != "http://localhost:7777" {
			// url.Parse allows trailing path/query on Origin only if empty host matched; verify host strictly.
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || (u.Host != "127.0.0.1:7777" && u.Host != "localhost:7777") {
				t.Fatalf("ownOrigin accepted unexpected origin %q", origin)
			}
		}
	})
}
