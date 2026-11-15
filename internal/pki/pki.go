// Package pki creates the install's certificate authority and issues leaf
// certificates for services, using only the standard library.
//
// The CA is name-constrained to the Breadboard domain, so a leaked CA key
// cannot mint a certificate any client accepts for a real site. Leaves carry
// both serverAuth and clientAuth and a SPIFFE identity, so mTLS later is a
// policy change rather than a reissue.
package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Lifetimes and the reissue threshold.
const (
	CALifetime     = 5 * 365 * 24 * time.Hour
	LeafLifetime   = 90 * 24 * time.Hour
	ReissueBefore  = 30 * 24 * time.Hour
	clockSkewGrace = 5 * time.Minute
)

// CA is a certificate authority with its private key.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	// CertPEM is the CA certificate as written to disk.
	CertPEM []byte
}

// Domain returns the DNS domain the CA is constrained to.
func (ca *CA) Domain() string {
	for _, d := range ca.Cert.PermittedDNSDomains {
		return strings.TrimPrefix(d, ".")
	}
	return ""
}

// Fingerprint returns the SHA-256 fingerprint of the CA certificate.
func (ca *CA) Fingerprint() string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// NewCA creates a CA constrained to domain.
func NewCA(domain string, now time.Time) (*CA, error) {
	if domain == "" {
		return nil, errors.New("pki: domain is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "Breadboard root CA (" + domain + ")", Organization: []string{"Breadboard"}},
		NotBefore:                   now.Add(-clockSkewGrace),
		NotAfter:                    now.Add(CALifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"." + domain},
		PermittedURIDomains:         []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA certificate: %w", err)
	}
	return &CA{Cert: cert, Key: key, CertPEM: pemBlock("CERTIFICATE", der)}, nil
}

// LoadCA parses a CA from its PEM certificate and PKCS#8 PEM key.
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, err
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("pki: CA key does not match its certificate")
	}
	return &CA{Cert: cert, Key: key, CertPEM: certPEM}, nil
}

// KeyPEM returns the CA key as PKCS#8 PEM.
func (ca *CA) KeyPEM() ([]byte, error) { return marshalKey(ca.Key) }

// Usable reports whether the CA can keep issuing for domain at time now: it
// must be constrained to that domain and outlive a fresh leaf.
func (ca *CA) Usable(domain string, now time.Time) bool {
	return ca.Domain() == domain && now.Add(LeafLifetime).Before(ca.Cert.NotAfter)
}

// LeafRequest describes a service certificate.
type LeafRequest struct {
	DNSNames []string
	// Identity is the SPIFFE URI, e.g. spiffe://bb.internal/experiment/x/service/y.
	Identity string
}

// Leaf is an issued certificate and its key, both PEM.
type Leaf struct {
	CertPEM []byte
	KeyPEM  []byte // PKCS#8
	Cert    *x509.Certificate
}

// Issue signs a leaf certificate valid for LeafLifetime.
func (ca *CA) Issue(req LeafRequest, now time.Time) (*Leaf, error) {
	if len(req.DNSNames) == 0 {
		return nil, errors.New("pki: a leaf needs at least one DNS name")
	}
	var uris []*url.URL
	if req.Identity != "" {
		u, err := url.Parse(req.Identity)
		if err != nil || u.Scheme != "spiffe" {
			return nil, fmt.Errorf("pki: identity %q is not a spiffe URI", req.Identity)
		}
		uris = append(uris, u)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate leaf key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	notAfter := now.Add(LeafLifetime)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: req.DNSNames[0], Organization: []string{"Breadboard"}},
		DNSNames:     slices.Clone(req.DNSNames),
		URIs:         uris,
		NotBefore:    now.Add(-clockSkewGrace),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign leaf: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse leaf: %w", err)
	}
	keyPEM, err := marshalKey(key)
	if err != nil {
		return nil, err
	}
	return &Leaf{CertPEM: pemBlock("CERTIFICATE", der), KeyPEM: keyPEM, Cert: cert}, nil
}

// NeedsReissue reports whether an existing leaf must be replaced: it was not
// signed by ca, its names or identity differ from req, or fewer than
// ReissueBefore remain before it expires.
func (ca *CA) NeedsReissue(cert *x509.Certificate, req LeafRequest, now time.Time) bool {
	if cert == nil {
		return true
	}
	if err := cert.CheckSignatureFrom(ca.Cert); err != nil {
		return true
	}
	if !slices.Equal(cert.DNSNames, req.DNSNames) {
		return true
	}
	ids := []string{}
	for _, u := range cert.URIs {
		ids = append(ids, u.String())
	}
	want := []string{}
	if req.Identity != "" {
		want = append(want, req.Identity)
	}
	if !slices.Equal(ids, want) {
		return true
	}
	return cert.NotAfter.Sub(now) < ReissueBefore
}

// ParseCert parses the first certificate in PEM data.
func ParseCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("pki: no PEM certificate found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse certificate: %w", err)
	}
	return cert, nil
}

// KeyMatches reports whether keyPEM is the private key for cert.
func KeyMatches(cert *x509.Certificate, keyPEM []byte) bool {
	key, err := parseKey(keyPEM)
	if err != nil {
		return false
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	return ok && pub.Equal(&key.PublicKey)
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("pki: no PKCS#8 PEM private key found")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse private key: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("pki: private key is not ECDSA")
	}
	return key, nil
}

func marshalKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshal key: %w", err)
	}
	return pemBlock("PRIVATE KEY", der), nil
}

func pemBlock(typ string, der []byte) []byte {
	var buf bytes.Buffer
	_ = pem.Encode(&buf, &pem.Block{Type: typ, Bytes: der})
	return buf.Bytes()
}

func serialNumber() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("pki: serial number: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

// Identity builds a service's SPIFFE ID.
func Identity(domain, experiment, service string) string {
	return fmt.Sprintf("spiffe://%s/experiment/%s/service/%s", domain, experiment, service)
}
