package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"
)

// HostDomain is the only DNS domain the host CA may sign for. Browsers and
// curl resolve every *.localhost name to loopback (RFC 6761).
const HostDomain = "localhost"

// Host CA errors.
var (
	// ErrNotHostCA reports a host issuance from a CA that is not a host CA.
	ErrNotHostCA = errors.New("pki: not a host CA")
	// ErrNotHostName reports a host certificate name outside .localhost.
	ErrNotHostName = errors.New("pki: host certificate names must end in ." + HostDomain)
)

// hostClosedSubtree closes the URI and email name types (D-195): the host
// CA permits and excludes this same subtree for each, so any URI or email
// address is either outside the permitted subtree or inside the excluded
// one. An excluded empty subtree alone is not enough: Go reads it as every
// name, but OpenSSL reads it as no name. .invalid is reserved and never
// resolves (RFC 6761).
const hostClosedSubtree = ".invalid"

// hostExcludedIPs are every IPv4 and IPv6 address: the host CA can never sign
// for an IP address, public or not.
var hostExcludedIPs = []*net.IPNet{
	{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
	{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
}

// NewHostCA creates the host CA: it signs only the host router's certificate
// for *.localhost names. Its name constraints (critical) permit the DNS
// domain .localhost alone, exclude every IPv4 and IPv6 address, and permit
// and exclude the same .invalid subtree for URIs and for email addresses,
// so a leaked key can sign neither a real site nor a SPIFFE identity, in
// Go and in OpenSSL alike.
func NewHostCA(now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate host CA key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	tmpl := hostCATemplate(serial, now)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create host CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse host CA certificate: %w", err)
	}
	return &CA{Cert: cert, Key: key, CertPEM: pemBlock("CERTIFICATE", der)}, nil
}

// hostCATemplate is the host CA's certificate template.
func hostCATemplate(serial *big.Int, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "Breadboard host CA (" + HostDomain + ")", Organization: []string{"Breadboard"}},
		NotBefore:                   now.Add(-clockSkewGrace),
		NotAfter:                    now.Add(CALifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"." + HostDomain},
		PermittedURIDomains:         []string{hostClosedSubtree},
		ExcludedURIDomains:          []string{hostClosedSubtree},
		PermittedEmailAddresses:     []string{hostClosedSubtree},
		ExcludedEmailAddresses:      []string{hostClosedSubtree},
		ExcludedIPRanges:            hostExcludedIPs,
	}
}

// isHost reports whether the CA carries exactly the host CA's constraints.
// A CA of an earlier shape (an empty URI or email exclusion) is not one.
func (ca *CA) isHost() bool {
	c := ca.Cert
	closed := []string{hostClosedSubtree}
	return c.IsCA && c.PermittedDNSDomainsCritical &&
		slices.Equal(c.PermittedDNSDomains, []string{"." + HostDomain}) && len(c.ExcludedDNSDomains) == 0 &&
		slices.Equal(c.PermittedURIDomains, closed) && slices.Equal(c.ExcludedURIDomains, closed) &&
		slices.Equal(c.PermittedEmailAddresses, closed) && slices.Equal(c.ExcludedEmailAddresses, closed) &&
		len(c.PermittedIPRanges) == 0 && excludesAllIPs(c.ExcludedIPRanges)
}

// excludesAllIPs reports whether ranges are exactly IPv4 0.0.0.0/0 and IPv6
// ::/0, once each, in either order.
func excludesAllIPs(ranges []*net.IPNet) bool {
	if len(ranges) != 2 {
		return false
	}
	var v4, v6 int
	for _, r := range ranges {
		ones, bits := r.Mask.Size()
		switch {
		case ones != 0:
			return false
		case bits == 32 && len(r.IP) == net.IPv4len && r.IP.Equal(net.IPv4zero):
			v4++
		case bits == 128 && len(r.IP) == net.IPv6len && r.IP.Equal(net.IPv6zero) && r.IP.To4() == nil:
			v6++
		default:
			return false
		}
	}
	return v4 == 1 && v6 == 1
}

// HostUsable reports whether the CA can keep issuing host certificates at
// time now: it must carry exactly the host CA's name constraints and outlive
// a fresh leaf.
func (ca *CA) HostUsable(now time.Time) bool {
	return ca.isHost() && now.Add(LeafLifetime).Before(ca.Cert.NotAfter)
}

// HostNames returns names sorted and without duplicates, the order a host
// certificate lists them in.
func HostNames(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out)
}

// IssueHost signs the host router's certificate for names, valid for
// LeafLifetime: the names sorted as DNS SANs, no URI SAN, extended key usage
// serverAuth only. Every name must be a subdomain of .localhost.
func (ca *CA) IssueHost(names []string, now time.Time) (*Leaf, error) {
	if !ca.isHost() {
		return nil, ErrNotHostCA
	}
	names = HostNames(names)
	if len(names) == 0 {
		return nil, errors.New("pki: a host certificate needs at least one DNS name")
	}
	for _, n := range names {
		if !strings.HasSuffix(n, "."+HostDomain) || n == "."+HostDomain {
			return nil, fmt.Errorf("%w: %q", ErrNotHostName, n)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate host leaf key: %w", err)
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
		Subject:      pkix.Name{CommonName: names[0], Organization: []string{"Breadboard"}},
		DNSNames:     names,
		NotBefore:    now.Add(-clockSkewGrace),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("pki: sign host leaf: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse host leaf: %w", err)
	}
	keyPEM, err := marshalKey(key)
	if err != nil {
		return nil, err
	}
	return &Leaf{CertPEM: pemBlock("CERTIFICATE", der), KeyPEM: keyPEM, Cert: cert}, nil
}

// HostNeedsReissue reports whether an existing host certificate must be
// replaced: it was not signed by ca, its names differ from names (compared
// as a set), it carries a URI SAN, or fewer than ReissueBefore remain before
// it expires.
func (ca *CA) HostNeedsReissue(cert *x509.Certificate, names []string, now time.Time) bool {
	if cert == nil {
		return true
	}
	if err := cert.CheckSignatureFrom(ca.Cert); err != nil {
		return true
	}
	if !slices.Equal(HostNames(cert.DNSNames), HostNames(names)) || len(cert.URIs) > 0 {
		return true
	}
	return cert.NotAfter.Sub(now) < ReissueBefore
}
