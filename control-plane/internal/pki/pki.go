// Package pki is the control plane's certificate authority: it signs
// Pomerium's server certificate and each worker's client certificate.
// Pomerium admits a connection only with a client certificate this CA
// issued, and a route only for the certificates its worker list names.
package pki

import (
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
	"net"
	"os"
	"path/filepath"
	"time"
)

// Validity of what the CA issues.
const (
	CAValidity     = 10 * 365 * 24 * time.Hour
	LeafValidity   = 2 * 365 * 24 * time.Hour
	clockTolerance = 5 * time.Minute
)

// CA is the control plane's certificate authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// NewCA makes a CA named name.
func NewCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-clockTolerance),
		NotAfter:              now.Add(CAValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// Leaf is an issued certificate and its key.
type Leaf struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// IssueServer issues the certificate Pomerium serves for each host name
// or IP address in names.
func (ca *CA) IssueServer(names []string) (*Leaf, error) {
	if len(names) == 0 {
		return nil, errors.New("a server certificate needs at least one host name")
	}
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: names[0]},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	return ca.issue(tmpl)
}

// IssueWorker issues the client certificate of worker name. The name is
// the certificate's common name and its only DNS subject alternative
// name, which Pomerium sets as the worker's identity header.
func (ca *CA) IssueWorker(name string) (*Leaf, error) {
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: name, OrganizationalUnit: []string{"worker"}},
		DNSNames:    []string{name},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return ca.issue(tmpl)
}

func (ca *CA) issue(tmpl *x509.Certificate) (*Leaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	if tmpl.SerialNumber, err = serialNumber(); err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl.NotBefore = now.Add(-clockTolerance)
	tmpl.NotAfter = now.Add(LeafValidity)
	if tmpl.NotAfter.After(ca.Cert.NotAfter) {
		tmpl.NotAfter = ca.Cert.NotAfter
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Leaf{Cert: cert, Key: key}, nil
}

// Fingerprint is the SHA-256 of a certificate's DER, in lower-case hex:
// the form Pomerium's client_certificate fingerprint matcher takes.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// CertPEM encodes a certificate.
func CertPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// KeyPEM encodes a key as PKCS#8.
func KeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// WritePair writes a certificate and its key as certFile (0644) and
// keyFile (0600), creating their directories.
func WritePair(certFile, keyFile string, cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	k, err := KeyPEM(key)
	if err != nil {
		return err
	}
	if err := writeFile(keyFile, k, 0o600); err != nil {
		return err
	}
	return writeFile(certFile, CertPEM(cert), 0o644)
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCert reads one PEM certificate.
func LoadCert(file string) (*x509.Certificate, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the operator's files
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", file)
	}
	return x509.ParseCertificate(block.Bytes)
}

// LoadKey reads one PEM ECDSA key, PKCS#8 or SEC 1.
func LoadKey(file string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the operator's files
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s holds no PEM key", file)
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s: want an ECDSA key, got %T", file, k)
		}
		return ek, nil
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	}
	return nil, fmt.Errorf("%s: unsupported PEM block %q", file, block.Type)
}

// LoadCA reads the CA from its certificate and key files.
func LoadCA(certFile, keyFile string) (*CA, error) {
	cert, err := LoadCert(certFile)
	if err != nil {
		return nil, err
	}
	key, err := LoadKey(keyFile)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("%s is not a CA certificate", certFile)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("%s does not hold the key of %s", keyFile, certFile)
	}
	return &CA{Cert: cert, Key: key}, nil
}

// Verify checks that cert was issued by ca for client authentication.
func (ca *CA) Verify(cert *x509.Certificate) error {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	_, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}
