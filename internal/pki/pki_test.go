package pki

import (
	"crypto/x509"
	"errors"
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func newCA(t *testing.T) *CA {
	t.Helper()

	ca, err := NewCA("bb.internal", now)
	if err != nil {
		t.Fatal(err)
	}

	return ca
}

func verify(ca *CA, leaf *Leaf, name string) error {
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	_, err := leaf.Cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		DNSName:     name,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	return err
}

func TestCAShape(t *testing.T) {
	ca := newCA(t)

	c := ca.Cert
	if !c.IsCA || !c.MaxPathLenZero || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA basics wrong: isCA=%v maxPathLenZero=%v usage=%v", c.IsCA, c.MaxPathLenZero, c.KeyUsage)
	}

	if !c.PermittedDNSDomainsCritical || !slices.Equal(c.PermittedDNSDomains, []string{".bb.internal"}) || !slices.Equal(c.PermittedURIDomains, []string{"bb.internal"}) {
		t.Fatalf("name constraints = %v %v critical=%v", c.PermittedDNSDomains, c.PermittedURIDomains, c.PermittedDNSDomainsCritical)
	}

	if got := c.NotAfter.Sub(now); got < CALifetime-time.Hour || got > CALifetime+time.Hour {
		t.Fatalf("CA lifetime = %s", got)
	}

	if ca.Domain() != "bb.internal" {
		t.Fatalf("Domain() = %q", ca.Domain())
	}
}

func TestLeafSANsIdentityAndEKUs(t *testing.T) {
	ca := newCA(t)
	id := Identity("bb.internal", "kafka-lab", "kafka")

	leaf, err := ca.Issue(LeafRequest{DNSNames: []string{"kafka.bb.internal"}, Identity: id}, now)
	if err != nil {
		t.Fatal(err)
	}

	c := leaf.Cert
	if !slices.Equal(c.DNSNames, []string{"kafka.bb.internal"}) {
		t.Errorf("DNSNames = %v", c.DNSNames)
	}

	if len(c.URIs) != 1 || c.URIs[0].String() != "spiffe://bb.internal/experiment/kafka-lab/service/kafka" {
		t.Errorf("URIs = %v", c.URIs)
	}

	if !slices.Equal(c.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}) {
		t.Errorf("ExtKeyUsage = %v", c.ExtKeyUsage)
	}

	if c.IsCA {
		t.Error("leaf must not be a CA")
	}

	if got := c.NotAfter.Sub(now); got != LeafLifetime {
		t.Errorf("leaf lifetime = %s", got)
	}

	if err := verify(ca, leaf, "kafka.bb.internal"); err != nil {
		t.Errorf("leaf does not verify: %v", err)
	}

	if !KeyMatches(c, leaf.KeyPEM) {
		t.Error("KeyMatches = false for the leaf's own key")
	}
}

func TestNameConstraintsRejectOutOfDomainLeaf(t *testing.T) {
	ca := newCA(t)
	for _, tc := range []struct {
		name string
		req  LeafRequest
		host string
	}{
		{"dns outside domain", LeafRequest{DNSNames: []string{"www.example.com"}}, "www.example.com"},
		{"domain apex", LeafRequest{DNSNames: []string{"bb.internal"}}, "bb.internal"},
		{"uri outside domain", LeafRequest{DNSNames: []string{"kafka.bb.internal"}, Identity: "spiffe://evil.example/experiment/x/service/y"}, "kafka.bb.internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf, err := ca.Issue(tc.req, now)
			if err != nil {
				t.Fatal(err)
			}

			err = verify(ca, leaf, tc.host)

			var invalid x509.CertificateInvalidError
			if !errors.As(err, &invalid) || invalid.Reason != x509.CANotAuthorizedForThisName {
				t.Fatalf("verify error = %v, want a name-constraint violation", err)
			}
		})
	}
}

func TestNeedsReissue(t *testing.T) {
	ca := newCA(t)
	req := LeafRequest{DNSNames: []string{"redis.bb.internal"}, Identity: Identity("bb.internal", "x", "redis")}

	leaf, err := ca.Issue(req, now)
	if err != nil {
		t.Fatal(err)
	}

	other := newCA(t)
	for _, tc := range []struct {
		name string
		ca   *CA
		req  LeafRequest
		at   time.Time
		want bool
	}{
		{"fresh", ca, req, now, false},
		{"31 days left", ca, req, now.Add(LeafLifetime - 31*24*time.Hour), false},
		{"29 days left", ca, req, now.Add(LeafLifetime - 29*24*time.Hour), true},
		{"expired", ca, req, now.Add(LeafLifetime + time.Hour), true},
		{"renamed", ca, LeafRequest{DNSNames: []string{"cache.bb.internal"}, Identity: req.Identity}, now, true},
		{"new identity", ca, LeafRequest{DNSNames: req.DNSNames, Identity: Identity("bb.internal", "y", "redis")}, now, true},
		{"other CA", other, req, now, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ca.NeedsReissue(leaf.Cert, tc.req, tc.at); got != tc.want {
				t.Fatalf("NeedsReissue = %v, want %v", got, tc.want)
			}
		})
	}

	if !ca.NeedsReissue(nil, req, now) {
		t.Error("NeedsReissue(nil) = false")
	}
}

func TestLoadCARoundTrip(t *testing.T) {
	ca := newCA(t)

	keyPEM, err := ca.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCA(ca.CertPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Fingerprint() != ca.Fingerprint() {
		t.Fatal("fingerprint changed after round trip")
	}

	otherKey, _ := newCA(t).KeyPEM()
	if _, err := LoadCA(ca.CertPEM, otherKey); err == nil {
		t.Fatal("LoadCA accepted a key that does not match")
	}

	if !loaded.Usable("bb.internal", now) {
		t.Error("Usable = false for its own domain")
	}

	if loaded.Usable("lab.internal", now) {
		t.Error("Usable = true for another domain")
	}

	if loaded.Usable("bb.internal", now.Add(CALifetime-LeafLifetime+time.Hour)) {
		t.Error("Usable = true when a full leaf would outlive the CA")
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	ca := newCA(t)
	if _, err := ca.Issue(LeafRequest{}, now); err == nil {
		t.Error("Issue without DNS names succeeded")
	}

	if _, err := ca.Issue(LeafRequest{DNSNames: []string{"a.bb.internal"}, Identity: "https://bb.internal/x"}, now); err == nil {
		t.Error("Issue accepted a non-spiffe identity")
	}
}
