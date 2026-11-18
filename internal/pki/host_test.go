package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newHostCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewHostCA(now)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// signRaw signs any leaf template with ca's key, bypassing IssueHost's
// checks, to prove the certificate's own constraints reject it.
func signRaw(t *testing.T, ca *CA, tmpl *x509.Certificate) *Leaf {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(42)
	tmpl.NotBefore, tmpl.NotAfter = now.Add(-time.Hour), now.Add(LeafLifetime)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &Leaf{Cert: cert}
}

func TestHostCAShape(t *testing.T) {
	ca := newHostCA(t)
	c := ca.Cert
	if !c.IsCA || !c.MaxPathLenZero || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA basics wrong: isCA=%v maxPathLenZero=%v usage=%v", c.IsCA, c.MaxPathLenZero, c.KeyUsage)
	}
	if !c.PermittedDNSDomainsCritical || !slices.Equal(c.PermittedDNSDomains, []string{".localhost"}) {
		t.Fatalf("permitted DNS = %v critical=%v", c.PermittedDNSDomains, c.PermittedDNSDomainsCritical)
	}
	closed := []string{".invalid"}
	if !slices.Equal(c.PermittedURIDomains, closed) || !slices.Equal(c.ExcludedURIDomains, closed) ||
		!slices.Equal(c.PermittedEmailAddresses, closed) || !slices.Equal(c.ExcludedEmailAddresses, closed) {
		t.Fatalf("URI constraints = %v / %v, email constraints = %v / %v, want .invalid permitted and excluded",
			c.PermittedURIDomains, c.ExcludedURIDomains, c.PermittedEmailAddresses, c.ExcludedEmailAddresses)
	}
	if len(c.ExcludedIPRanges) != 2 || c.ExcludedIPRanges[0].String() != "0.0.0.0/0" || c.ExcludedIPRanges[1].String() != "::/0" {
		t.Fatalf("excluded IP ranges = %v", c.ExcludedIPRanges)
	}
	if c.Subject.CommonName != "Breadboard host CA (localhost)" || !slices.Equal(c.Subject.Organization, []string{"Breadboard"}) {
		t.Fatalf("subject = %v", c.Subject)
	}
	if _, ok := c.PublicKey.(*ecdsa.PublicKey); !ok || c.PublicKey.(*ecdsa.PublicKey).Curve != elliptic.P256() {
		t.Fatal("host CA key is not ECDSA P-256")
	}
	if got := c.NotAfter.Sub(now); got < CALifetime-time.Hour || got > CALifetime+time.Hour {
		t.Fatalf("CA lifetime = %s", got)
	}
	if ca.Domain() != HostDomain {
		t.Fatalf("Domain() = %q", ca.Domain())
	}
	if !ca.HostUsable(now) {
		t.Error("HostUsable = false for a fresh host CA")
	}
	if ca.HostUsable(now.Add(CALifetime - LeafLifetime + time.Hour)) {
		t.Error("HostUsable = true when a full leaf would outlive the CA")
	}
	if newCA(t).HostUsable(now) {
		t.Error("HostUsable = true for the lab CA")
	}
	if lab, err := NewCA(HostDomain, now); err != nil || lab.HostUsable(now) {
		t.Errorf("HostUsable = true for a lab CA on localhost (err %v)", err)
	}
}

// hostCAWith is a host CA whose template mutate changed: a saved CA of
// another shape.
func hostCAWith(t *testing.T, mutate func(*x509.Certificate)) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := hostCATemplate(big.NewInt(7), now)
	mutate(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &CA{Cert: cert, Key: key, CertPEM: pemBlock("CERTIFICATE", der)}
}

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Only exactly the host CA's constraints make a host CA: earlier shapes and
// wrong IP exclusions are not usable, so they are replaced.
func TestHostCAExactConstraints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*x509.Certificate)
	}{
		{"empty URI and email exclusions (earlier shape)", func(c *x509.Certificate) {
			c.PermittedURIDomains, c.ExcludedURIDomains = nil, []string{""}
			c.PermittedEmailAddresses, c.ExcludedEmailAddresses = nil, []string{""}
		}},
		{"no permitted URI subtree", func(c *x509.Certificate) { c.PermittedURIDomains = nil }},
		{"no excluded email subtree", func(c *x509.Certificate) { c.ExcludedEmailAddresses = nil }},
		{"other closed subtree", func(c *x509.Certificate) { c.ExcludedURIDomains = []string{".example"} }},
		{"two narrow IP exclusions", func(c *x509.Certificate) {
			c.ExcludedIPRanges = []*net.IPNet{cidr(t, "10.0.0.0/8"), cidr(t, "fc00::/7")}
		}},
		{"IPv4 twice, no IPv6", func(c *x509.Certificate) {
			c.ExcludedIPRanges = []*net.IPNet{cidr(t, "0.0.0.0/0"), cidr(t, "0.0.0.0/0")}
		}},
		{"IPv6 twice, no IPv4", func(c *x509.Certificate) {
			c.ExcludedIPRanges = []*net.IPNet{cidr(t, "::/0"), cidr(t, "::/0")}
		}},
		{"IPv4 only", func(c *x509.Certificate) { c.ExcludedIPRanges = []*net.IPNet{cidr(t, "0.0.0.0/0")} }},
		{"a third IP exclusion", func(c *x509.Certificate) {
			c.ExcludedIPRanges = []*net.IPNet{cidr(t, "0.0.0.0/0"), cidr(t, "::/0"), cidr(t, "10.0.0.0/8")}
		}},
		{"permitted IP range", func(c *x509.Certificate) { c.PermittedIPRanges = []*net.IPNet{cidr(t, "127.0.0.0/8")} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca := hostCAWith(t, tc.mutate)
			if ca.HostUsable(now) {
				t.Error("HostUsable = true")
			}
			if _, err := ca.IssueHost([]string{"api.lab.localhost"}, now); !errors.Is(err, ErrNotHostCA) {
				t.Errorf("IssueHost = %v, want ErrNotHostCA", err)
			}
		})
	}
	// The IP exclusions may come in either order.
	swapped := hostCAWith(t, func(c *x509.Certificate) {
		c.ExcludedIPRanges = []*net.IPNet{cidr(t, "::/0"), cidr(t, "0.0.0.0/0")}
	})
	if !swapped.HostUsable(now) {
		t.Error("HostUsable = false with the IP exclusions swapped")
	}
}

// hostRejectCases are leaves the host CA's key can sign but its name
// constraints must reject.
func hostRejectCases(t *testing.T) []struct {
	name string
	tmpl *x509.Certificate
	host string
} {
	t.Helper()
	u := func(s string) *url.URL {
		v, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	const h = "api.lab.localhost"
	dns := []string{h}
	return []struct {
		name string
		tmpl *x509.Certificate
		host string
	}{
		{"real site", &x509.Certificate{DNSNames: []string{"example.com"}}, "example.com"},
		{"lab name", &x509.Certificate{DNSNames: []string{"api.bb.internal"}}, "api.bb.internal"},
		{"localhost apex", &x509.Certificate{DNSNames: []string{"localhost"}}, "localhost"},
		{"real site beside a host name", &x509.Certificate{DNSNames: []string{h, "example.com"}}, h},
		{"spiffe URI under localhost", &x509.Certificate{DNSNames: dns, URIs: []*url.URL{u("spiffe://localhost/experiment/x/service/y")}}, h},
		{"lab identity URI", &x509.Certificate{DNSNames: dns, URIs: []*url.URL{u(Identity("bb.internal", "x", "y"))}}, h},
		{"https URI", &x509.Certificate{DNSNames: dns, URIs: []*url.URL{u("https://api.lab.localhost/")}}, h},
		{"URI inside .invalid", &x509.Certificate{DNSNames: dns, URIs: []*url.URL{u("spiffe://x.invalid/x")}}, h},
		{"URI at the .invalid apex", &x509.Certificate{DNSNames: dns, URIs: []*url.URL{u("spiffe://invalid/x")}}, h},
		{"public IPv4", &x509.Certificate{DNSNames: dns, IPAddresses: []net.IP{net.ParseIP("203.0.113.7")}}, h},
		{"loopback IPv6", &x509.Certificate{DNSNames: dns, IPAddresses: []net.IP{net.IPv6loopback}}, h},
		{"email", &x509.Certificate{DNSNames: dns, EmailAddresses: []string{"someone@example.com"}}, h},
		{"email inside .invalid", &x509.Certificate{DNSNames: dns, EmailAddresses: []string{"a@x.invalid"}}, h},
	}
}

func TestHostCARejectsNamesOutsideLocalhost(t *testing.T) {
	ca := newHostCA(t)
	if err := verify(ca, signRaw(t, ca, &x509.Certificate{Subject: pkix.Name{CommonName: "api.lab.localhost"}, DNSNames: []string{"api.lab.localhost"}}), "api.lab.localhost"); err != nil {
		t.Fatalf("a DNS-only host leaf does not verify: %v", err)
	}
	for _, tc := range hostRejectCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			tc.tmpl.Subject = pkix.Name{CommonName: tc.host}
			err := verify(ca, signRaw(t, ca, tc.tmpl), tc.host)
			var invalid x509.CertificateInvalidError
			if !errors.As(err, &invalid) || invalid.Reason != x509.CANotAuthorizedForThisName {
				t.Fatalf("verify error = %v, want a name-constraint violation", err)
			}
		})
	}
}

func TestHostLeafShape(t *testing.T) {
	ca := newHostCA(t)
	names := []string{"web.lab.localhost", "api.lab.localhost", "api-1.lab.localhost", "api.lab.localhost"}
	leaf, err := ca.IssueHost(names, now)
	if err != nil {
		t.Fatal(err)
	}
	c := leaf.Cert
	want := []string{"api-1.lab.localhost", "api.lab.localhost", "web.lab.localhost"}
	if !slices.Equal(c.DNSNames, want) {
		t.Errorf("DNSNames = %v, want %v", c.DNSNames, want)
	}
	if len(c.URIs) != 0 || len(c.IPAddresses) != 0 || len(c.EmailAddresses) != 0 {
		t.Errorf("SANs beyond DNS: %v %v %v", c.URIs, c.IPAddresses, c.EmailAddresses)
	}
	if !slices.Equal(c.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("ExtKeyUsage = %v", c.ExtKeyUsage)
	}
	if c.IsCA {
		t.Error("host leaf must not be a CA")
	}
	if got := c.NotAfter.Sub(now); got != LeafLifetime {
		t.Errorf("host leaf lifetime = %s", got)
	}
	for _, n := range want {
		if err := verify(ca, leaf, n); err != nil {
			t.Errorf("host leaf does not verify for %s: %v", n, err)
		}
	}
	if !KeyMatches(c, leaf.KeyPEM) {
		t.Error("KeyMatches = false for the host leaf's own key")
	}
	// The host certificate never verifies against the lab CA.
	if err := verify(newCA(t), leaf, want[0]); err == nil {
		t.Error("host leaf verified against a lab CA")
	}
	if names[0] != "web.lab.localhost" {
		t.Error("IssueHost reordered the caller's slice")
	}
}

func TestIssueHostRejectsBadInput(t *testing.T) {
	ca := newHostCA(t)
	for _, names := range [][]string{nil, {"api.bb.internal"}, {"localhost"}, {".localhost"}, {"api.lab.localhost", "example.com"}} {
		if _, err := ca.IssueHost(names, now); err == nil {
			t.Errorf("IssueHost(%q) succeeded", names)
		}
	}
	if _, err := ca.IssueHost([]string{"evil.com"}, now); !errors.Is(err, ErrNotHostName) {
		t.Errorf("IssueHost outside .localhost = %v, want ErrNotHostName", err)
	}
	if _, err := newCA(t).IssueHost([]string{"api.lab.localhost"}, now); !errors.Is(err, ErrNotHostCA) {
		t.Errorf("IssueHost from the lab CA = %v, want ErrNotHostCA", err)
	}
}

func TestHostNeedsReissue(t *testing.T) {
	ca := newHostCA(t)
	names := []string{"api.lab.localhost", "web.lab.localhost"}
	leaf, err := ca.IssueHost(names, now)
	if err != nil {
		t.Fatal(err)
	}
	other := newHostCA(t)
	for _, tc := range []struct {
		name  string
		ca    *CA
		names []string
		at    time.Time
		want  bool
	}{
		{"fresh", ca, names, now, false},
		{"same names, other order", ca, []string{"web.lab.localhost", "api.lab.localhost"}, now, false},
		{"31 days left", ca, names, now.Add(LeafLifetime - 31*24*time.Hour), false},
		{"29 days left", ca, names, now.Add(LeafLifetime - 29*24*time.Hour), true},
		{"expired", ca, names, now.Add(LeafLifetime + time.Hour), true},
		{"name added", ca, append(slices.Clone(names), "api-1.lab.localhost"), now, true},
		{"name removed", ca, names[:1], now, true},
		{"other CA", other, names, now, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ca.HostNeedsReissue(leaf.Cert, tc.names, tc.at); got != tc.want {
				t.Fatalf("HostNeedsReissue = %v, want %v", got, tc.want)
			}
		})
	}
	if !ca.HostNeedsReissue(nil, names, now) {
		t.Error("HostNeedsReissue(nil) = false")
	}
}

func TestHostCALoadRoundTrip(t *testing.T) {
	ca := newHostCA(t)
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCA(ca.CertPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.HostUsable(now) || loaded.Fingerprint() != ca.Fingerprint() {
		t.Error("host CA changed after a round trip")
	}
}

// OpenSSL reads name constraints differently from Go (D-195): the host CA
// must reject the same raw-signed leaves there. It runs only where an
// openssl binary is installed.
func TestHostCAOpenSSL(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not on PATH")
	}
	ca := newHostCA(t)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "host-ca.crt")
	if err := os.WriteFile(caFile, ca.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	opensslVerify := func(t *testing.T, leaf *Leaf) (bool, string) {
		t.Helper()
		f := filepath.Join(dir, strings.NewReplacer(" ", "-", "/", "-").Replace(t.Name())+".crt")
		if err := os.WriteFile(f, pemBlock("CERTIFICATE", leaf.Cert.Raw), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, "verify", "-CAfile", caFile, "-attime", strconv.FormatInt(now.Unix(), 10), "-purpose", "sslserver", f).CombinedOutput()
		return err == nil && strings.Contains(string(out), ": OK"), string(out)
	}
	t.Run("legitimate leaf", func(t *testing.T) {
		leaf, err := ca.IssueHost([]string{"api.lab.localhost", "api-1.lab.localhost"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if ok, out := opensslVerify(t, leaf); !ok {
			t.Fatalf("openssl rejected the host leaf:\n%s", out)
		}
	})
	for _, tc := range hostRejectCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			tc.tmpl.Subject = pkix.Name{CommonName: tc.host}
			ok, out := opensslVerify(t, signRaw(t, ca, tc.tmpl))
			if ok || !strings.Contains(out, "subtree violation") {
				t.Fatalf("openssl did not reject the leaf for its names:\n%s", out)
			}
		})
	}
}
