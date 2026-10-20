package pki

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Bundle struct {
	CA    []byte
	CAKey []byte
	Cert  []byte
	Key   []byte
}

func Generate(now time.Time) (Bundle, error) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Bundle{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Bundle{}, err
	}
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Foundation private CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, MaxPathLenZero: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, private)
	if err != nil {
		return Bundle{}, err
	}
	parsedCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Bundle{}, err
	}
	cert, key, err := issueServer(parsedCA, private, now)
	if err != nil {
		return Bundle{}, err
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{CA: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		CAKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}),
		Cert:  cert, Key: key}, nil
}

func issueServer(ca *x509.Certificate, private crypto.Signer, now time.Time) ([]byte, []byte, error) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	expiry := now.AddDate(1, 0, 0)
	if ca.NotAfter.Before(expiry) {
		expiry = ca.NotAfter
	}
	if !expiry.After(now) || !ca.IsCA {
		return nil, nil, domain.Fail(domain.Prerequisite, "CA is expired or invalid; obtain an explicit trust-rotation plan rather than resetting identities")
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: expiry,
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, public, private)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func Renew(dir, certFile, keyFile string, now time.Time) error {
	caPEM, err := secure.Read(filepath.Join(dir, "ca.pem"), domain.MaxBody)
	if err != nil {
		return err
	}
	keyPEM, err := secure.Read(filepath.Join(dir, "ca-key.pem"), domain.MaxBody)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return domain.Fail(domain.Invalid, "invalid CA certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return domain.Fail(domain.Invalid, "invalid CA private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return domain.Fail(domain.Invalid, "unsupported CA key type")
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return err
	}
	if !bytes.Equal(public, ca.RawSubjectPublicKeyInfo) {
		return domain.Fail(domain.Denied, "CA key does not match pinned trust")
	}
	pending := filepath.Join(dir, "tls-renewal.json")
	var plan struct {
		CAHash string `json:"ca_hash"`
		Cert   []byte `json:"cert"`
		Key    []byte `json:"key"`
	}
	err = config.Read(pending, &plan)
	if os.IsNotExist(err) {
		cert, key, err := issueServer(ca, signer, now)
		if err != nil {
			return err
		}
		plan.CAHash, plan.Cert, plan.Key = domain.Hash(string(caPEM)), cert, key
		if err := config.Write(pending, plan); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if plan.CAHash != domain.Hash(string(caPEM)) {
		return domain.Fail(domain.Conflict, "retained renewal belongs to another CA")
	}
	if _, err := tls.X509KeyPair(plan.Cert, plan.Key); err != nil {
		return err
	}
	if err := secure.Write(keyFile, plan.Key); err != nil {
		return err
	}
	if err := secure.Write(certFile, plan.Cert); err != nil {
		return err
	}
	if err := os.Remove(pending); err != nil {
		return err
	}
	return secure.SyncDir(dir)
}

func (b Bundle) Write(dir string) error {
	for _, file := range []struct {
		name string
		data []byte
	}{
		{"ca.pem", b.CA}, {"ca-key.pem", b.CAKey}, {"server.pem", b.Cert}, {"server-key.pem", b.Key},
	} {
		if err := secure.Create(filepath.Join(dir, file.name), file.data); err != nil {
			return err
		}
	}
	return nil
}

func Server(certFile, keyFile string) (*tls.Config, error) {
	cert, err := secure.Read(certFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	key, err := secure.Read(keyFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, nil
}

func Client(caFile, serverName string) (*tls.Config, error) {
	data, err := secure.ReadPublic(caFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "trust bundle too large")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, domain.Fail(domain.Invalid, "trust bundle contains no CA certificate")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: serverName}, nil
}
