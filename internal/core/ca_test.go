package core

import (
	"bytes"
	"os"
	"sync"
	"testing"

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
