package pki

import (
	"strings"
	"testing"
)

// FuzzHostCANameConstraints ensures IssueHost never panics on arbitrary names
// and that no certificate issued by the host CA can ever verify for a DNS name
// outside .localhost.
func FuzzHostCANameConstraints(f *testing.F) {
	f.Add("api.lab.localhost")
	f.Add("evil.com")
	f.Add("localhost")
	f.Add("api.localhost.evil.com")
	f.Add("127.0.0.1")
	f.Add("")
	f.Add("*.localhost")
	f.Add(" .localhost")
	f.Add("..localhost")
	f.Add("-bad.localhost")
	f.Add("spiffe://bb.internal/ns/lab/sa/api")

	ca, err := NewHostCA(now)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, name string) {
		leaf, err := ca.IssueHost([]string{name}, now)
		if err != nil {
			return
		}

		// Any leaf that succeeds MUST be inside .localhost and verify for its name.
		low := strings.ToLower(strings.TrimSuffix(name, "."))
		if !strings.HasSuffix(low, "."+HostDomain) {
			t.Fatalf("IssueHost accepted out-of-domain name %q", name)
		}

		if err := verify(ca, leaf, name); err != nil {
			t.Fatalf("issued host leaf for %q failed verification: %v", name, err)
		}

		// Cross-check: an out-of-domain name must never verify against this leaf.
		if err := verify(ca, leaf, "evil.example.com"); err == nil {
			t.Fatalf("host leaf for %q unexpectedly verified for evil.example.com", name)
		}
	})
}

// FuzzLabCANameConstraints ensures the lab CA enforces its .bb.internal
// permitted DNS name constraint on verification for arbitrary issued names.
func FuzzLabCANameConstraints(f *testing.F) {
	f.Add("postgres.bb.internal")
	f.Add("evil.example.com")
	f.Add("bb.internal")
	f.Add("a.b.bb.internal")
	f.Add("")

	ca, err := NewCA("bb.internal", now)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, name string) {
		leaf, err := ca.Issue(LeafRequest{DNSNames: []string{name}}, now)
		if err != nil {
			return
		}

		low := strings.ToLower(strings.TrimSuffix(name, "."))
		if !strings.HasSuffix(low, ".bb.internal") {
			if err := verify(ca, leaf, name); err == nil {
				t.Fatalf("lab CA verified out-of-domain name %q", name)
			}
		}
	})
}
