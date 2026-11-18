package core

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/pki"
)

// Projects share the install CA, so concurrent first runs in different
// projects must agree on one CA and never leave a torn key/certificate pair.
func TestConcurrentEnsureCAAgreesOnOneCA(t *testing.T) {
	h := newHarness(t)
	const n = 8
	var wg sync.WaitGroup
	certs := make([][]byte, n)
	created := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			// Each goroutine is its own engine, as separate bb processes are.
			eng, err := New(Options{Home: h.eng.home.Dir, Runtime: h.rt, Now: h.eng.now, Version: "v0.0.0-test"})
			if err != nil {
				errs[i] = err
				return
			}
			ca, made, err := eng.ensureCA("bb.internal")
			if err != nil {
				errs[i] = err
				return
			}
			certs[i], created[i] = ca.CertPEM, made
		})
	}
	wg.Wait()
	creators := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("ensureCA %d: %v", i, errs[i])
		}
		if !bytes.Equal(certs[i], certs[0]) {
			t.Fatalf("ensureCA %d returned a different CA", i)
		}
		if created[i] {
			creators++
		}
	}
	if creators != 1 {
		t.Errorf("%d calls created a CA, want exactly 1", creators)
	}
	certPEM, keyPEM, ok, err := h.eng.home.LoadCA(caName)
	if err != nil || !ok {
		t.Fatalf("LoadCA = %v %v", ok, err)
	}
	if _, err := pki.LoadCA(certPEM, keyPEM); err != nil {
		t.Fatalf("stored CA is unusable: %v", err)
	}
	if !bytes.Equal(certPEM, certs[0]) {
		t.Error("stored CA differs from the one returned")
	}
}

// A key that does not match its certificate (an interrupted write) is
// replaced instead of breaking every later command.
func TestEnsureCAReplacesATornPair(t *testing.T) {
	h := newHarness(t)
	first, _, err := h.eng.ensureCA("bb.internal")
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.NewCA("bb.internal", h.now)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := other.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.home.SaveCA(caName, first.CertPEM, otherKey); err != nil {
		t.Fatal(err)
	}
	ca, created, err := h.eng.ensureCA("bb.internal")
	if err != nil {
		t.Fatalf("ensureCA with a torn pair: %v", err)
	}
	if !created || bytes.Equal(ca.CertPEM, first.CertPEM) {
		t.Error("torn pair was not replaced with a new CA")
	}
	certPEM, _ := os.ReadFile(h.eng.home.CACertPath(caName))
	if !bytes.Equal(certPEM, ca.CertPEM) {
		t.Error("replacement CA was not saved")
	}
}

// The host CA is created once, reused, replaced when its key does not match
// or it is not a host CA, and never changes the root CA's files.
func TestEnsureHostCA(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.eng.ensureCA("bb.internal"); err != nil {
		t.Fatal(err)
	}
	rootFiles := func() [2][]byte {
		cert, key, ok, err := h.eng.home.LoadCA(caName)
		if err != nil || !ok {
			t.Fatalf("LoadCA(root) = %v %v", ok, err)
		}
		return [2][]byte{cert, key}
	}
	before := rootFiles()

	first, created, err := h.eng.ensureHostCA()
	if err != nil || !created {
		t.Fatalf("first ensureHostCA = created %v, err %v", created, err)
	}
	if !first.HostUsable(h.now) || first.Domain() != pki.HostDomain {
		t.Fatal("ensureHostCA returned a CA that is not a host CA")
	}
	// The key is 0600; the certificate is 0644, like root's.
	for file, mode := range map[string]os.FileMode{"host.key": 0o600, "host.crt": 0o644} {
		if fi, err := os.Stat(filepath.Join(h.eng.home.CADir(), file)); err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, %v, want %v", file, fi, err, mode)
		}
	}
	again, created, err := h.eng.ensureHostCA()
	if err != nil || created || !bytes.Equal(again.CertPEM, first.CertPEM) {
		t.Fatalf("second ensureHostCA = created %v, err %v, same %v", created, err, bytes.Equal(again.CertPEM, first.CertPEM))
	}

	// A torn pair is replaced.
	other, err := pki.NewHostCA(h.now)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, _ := other.KeyPEM()
	if err := h.eng.home.SaveCA(hostCAName, first.CertPEM, otherKey); err != nil {
		t.Fatal(err)
	}
	replaced, created, err := h.eng.ensureHostCA()
	if err != nil || !created || bytes.Equal(replaced.CertPEM, first.CertPEM) {
		t.Fatalf("ensureHostCA with a torn pair = created %v, err %v", created, err)
	}
	if saved, _ := os.ReadFile(h.eng.home.CACertPath(hostCAName)); !bytes.Equal(saved, replaced.CertPEM) {
		t.Error("replacement host CA was not saved")
	}

	// A CA without the host constraints under the name host is replaced.
	lab, err := pki.NewCA("localhost", h.now)
	if err != nil {
		t.Fatal(err)
	}
	labKey, _ := lab.KeyPEM()
	if err := h.eng.home.SaveCA(hostCAName, lab.CertPEM, labKey); err != nil {
		t.Fatal(err)
	}
	if ca, created, err := h.eng.ensureHostCA(); err != nil || !created || !ca.HostUsable(h.now) {
		t.Fatalf("ensureHostCA over a lab-shaped CA = created %v, err %v", created, err)
	}

	// A host CA of an earlier shape (empty URI and email exclusions, which
	// OpenSSL reads as excluding nothing) or with wrong IP exclusions is
	// replaced.
	_, v4, _ := net.ParseCIDR("10.0.0.0/8")
	_, v6, _ := net.ParseCIDR("fc00::/7")
	_, allV4, _ := net.ParseCIDR("0.0.0.0/0")
	// The fixture unchanged is a usable host CA, so each change alone
	// makes it unusable.
	{
		certPEM, keyPEM := hostShapedCA(t, h.now, func(*x509.Certificate) {})
		ca, err := pki.LoadCA(certPEM, keyPEM)
		if err != nil || !ca.HostUsable(h.now) {
			t.Fatalf("the unchanged host-shaped CA is not a usable host CA (err %v)", err)
		}
	}
	for name, mutate := range map[string]func(*x509.Certificate){
		"earlier shape": func(c *x509.Certificate) {
			c.PermittedURIDomains, c.ExcludedURIDomains = nil, []string{""}
			c.PermittedEmailAddresses, c.ExcludedEmailAddresses = nil, []string{""}
		},
		"narrow IP exclusions": func(c *x509.Certificate) { c.ExcludedIPRanges = []*net.IPNet{v4, v6} },
		"IPv4 exclusion twice": func(c *x509.Certificate) { c.ExcludedIPRanges = []*net.IPNet{allV4, allV4} },
	} {
		certPEM, keyPEM := hostShapedCA(t, h.now, mutate)
		if err := h.eng.home.SaveCA(hostCAName, certPEM, keyPEM); err != nil {
			t.Fatal(err)
		}
		ca, created, err := h.eng.ensureHostCA()
		if err != nil || !created || !ca.HostUsable(h.now) || bytes.Equal(ca.CertPEM, certPEM) {
			t.Fatalf("ensureHostCA over a host CA with %s = created %v, err %v", name, created, err)
		}
		if saved, _ := os.ReadFile(h.eng.home.CACertPath(hostCAName)); !bytes.Equal(saved, ca.CertPEM) {
			t.Errorf("%s: replacement host CA was not saved", name)
		}
	}

	if after := rootFiles(); !bytes.Equal(after[0], before[0]) || !bytes.Equal(after[1], before[1]) {
		t.Error("ensureHostCA changed the root CA's files")
	}
}

// hostShapedCA is a CA PEM pair shaped like the host CA but changed by
// mutate: a host CA saved by an earlier or broken version.
func hostShapedCA(t *testing.T, now time.Time, mutate func(*x509.Certificate)) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:                big.NewInt(9),
		Subject:                     pkix.Name{CommonName: "Breadboard host CA (localhost)", Organization: []string{"Breadboard"}},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(pki.CALifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{".localhost"},
		PermittedURIDomains:         []string{".invalid"},
		ExcludedURIDomains:          []string{".invalid"},
		PermittedEmailAddresses:     []string{".invalid"},
		ExcludedEmailAddresses:      []string{".invalid"},
		ExcludedIPRanges:            []*net.IPNet{{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}, {IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}},
	}
	mutate(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// The CA operation reports both CAs and keeps root's files unchanged.
func TestCAOperationReportsHostCA(t *testing.T) {
	h := newHarness(t)
	first, err := h.eng.CA(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "root" || first.Domain != "bb.internal" || !first.Created {
		t.Fatalf("root = %+v", first)
	}
	host := first.Host
	if host == nil || host.Name != "host" || host.Domain != "localhost" || !host.Created ||
		host.Subject != "Breadboard host CA (localhost)" || host.CertPath != h.eng.home.CACertPath("host") ||
		host.Fingerprint == "" || host.Fingerprint == first.Fingerprint || !bytes.HasPrefix(host.CertPEM, []byte("-----BEGIN CERTIFICATE-----")) {
		t.Fatalf("host = %+v", host)
	}
	if !host.NotAfter.After(host.NotBefore.Add(pki.CALifetime - time.Hour)) {
		t.Errorf("host validity = %s to %s", host.NotBefore, host.NotAfter)
	}
	second, err := h.eng.CA(h.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Host.Created || !bytes.Equal(second.CertPEM, first.CertPEM) || !bytes.Equal(second.Host.CertPEM, host.CertPEM) {
		t.Error("a second CA call replaced a CA")
	}
}
