package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func identityFixture(t *testing.T) (Bundle, ed25519.PublicKey, ed25519.PrivateKey, domain.CertificateIdentity, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ca, err := Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return ca, public, private, domain.CertificateIdentity{InstanceID: "instance_test", AgentID: "agent_test", Generation: 2, Purpose: "device"}, now
}

func identityLeaf(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("missing certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func identityPair(t *testing.T, certPEM []byte, private ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func signIdentityLeaf(t *testing.T, ca Bundle, template *x509.Certificate, public ed25519.PublicKey) []byte {
	t.Helper()
	block, _ := pem.Decode(ca.CAKey)
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, identityLeaf(t, ca.CA), public, private)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestClientCertificateIdentityAndPurposes(t *testing.T) {
	ca, public, private, identity, now := identityFixture(t)
	for _, purpose := range []string{"device", "owner"} {
		t.Run(purpose, func(t *testing.T) {
			identity := identity
			identity.Purpose = purpose
			if purpose == "owner" {
				identity.OwnerGeneration = 7
			}
			certPEM, err := IssueClient(ca.CA, ca.CAKey, public, identity, now, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			cert := identityLeaf(t, certPEM)
			parsed, err := ParseIdentity(cert)
			if err != nil || parsed != identity {
				t.Fatal("certificate identity changed", parsed, err)
			}
			if cert.KeyUsage != x509.KeyUsageDigitalSignature || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || cert.IsCA || cert.SerialNumber.Sign() <= 0 {
				t.Fatal("wrong client usages")
			}
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(ca.CA)
			if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
				t.Fatal(err)
			}
			if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
				t.Fatal("client certificate accepted as server")
			}
			pair := identityPair(t, certPEM, private)
			if len(pair.Certificate) != 1 || !bytes.Equal(cert.PublicKey.(ed25519.PublicKey), public) {
				t.Fatal("certificate/key mismatch")
			}
		})
	}
	nearExpiry := identityLeaf(t, ca.CA).NotAfter.Add(-time.Minute)
	certPEM, err := IssueClient(ca.CA, ca.CAKey, public, identity, nearExpiry, time.Hour)
	if err != nil || !identityLeaf(t, certPEM).NotAfter.Equal(identityLeaf(t, ca.CA).NotAfter) {
		t.Fatal("client expiry exceeds CA", err)
	}
}

func TestFounderDeviceCertificateIdentityAndPurposes(t *testing.T) {
	ca, public, _, identity, now := identityFixture(t)
	identity.DeviceID = domain.NewID("device")
	for _, purpose := range []string{"device", "owner"} {
		identity.Purpose, identity.OwnerGeneration = purpose, 0
		if purpose == "owner" {
			identity.OwnerGeneration = 3
		}
		cert, err := IssueClient(ca.CA, ca.CAKey, public, identity, now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseIdentity(identityLeaf(t, cert))
		if err != nil || parsed != identity || parsed.BindingID() != identity.DeviceID {
			t.Fatal("device-bound certificate lost its independent enrollment", parsed, err)
		}
	}
}

func TestClientIssuanceRejectsInvalidAuthorityAndIdentity(t *testing.T) {
	ca, public, _, identity, now := identityFixture(t)
	other, err := Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*domain.CertificateIdentity){
		"instance":                func(i *domain.CertificateIdentity) { i.InstanceID = "" },
		"agent":                   func(i *domain.CertificateIdentity) { i.AgentID = "agent/other" },
		"device":                  func(i *domain.CertificateIdentity) { i.DeviceID = "device/other" },
		"generation":              func(i *domain.CertificateIdentity) { i.Generation = 0 },
		"purpose":                 func(i *domain.CertificateIdentity) { i.Purpose = "admin" },
		"owner-generation":        func(i *domain.CertificateIdentity) { i.Purpose = "owner" },
		"device-owner-generation": func(i *domain.CertificateIdentity) { i.OwnerGeneration = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := identity
			mutate(&changed)
			if _, err := IssueClient(ca.CA, ca.CAKey, public, changed, now, time.Hour); err == nil {
				t.Fatal("invalid identity issued")
			}
		})
	}
	for _, test := range []struct {
		name    string
		ca, key []byte
		now     time.Time
		ttl     time.Duration
	}{
		{"mismatch", ca.CA, other.CAKey, now, time.Hour},
		{"not-ca", ca.Cert, ca.Key, now, time.Hour},
		{"future-ca", ca.CA, ca.CAKey, now.Add(-time.Hour), time.Hour},
		{"expired-ca", ca.CA, ca.CAKey, now.AddDate(11, 0, 0), time.Hour},
		{"extra-ca", append(append([]byte{}, ca.CA...), other.CA...), ca.CAKey, now, time.Hour},
		{"ca-preamble", append([]byte("garbage\n"), ca.CA...), ca.CAKey, now, time.Hour},
		{"extra-key", ca.CA, append(append([]byte{}, ca.CAKey...), other.CAKey...), now, time.Hour},
		{"zero-ttl", ca.CA, ca.CAKey, now, 0},
		{"negative-ttl", ca.CA, ca.CAKey, now, -time.Second},
		{"subsecond-ttl", ca.CA, ca.CAKey, now, time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := IssueClient(test.ca, test.key, public, identity, test.now, test.ttl); err == nil {
				t.Fatal("invalid issuance accepted")
			}
		})
	}
	if _, err := IssueClient(ca.CA, ca.CAKey, &rsa.PublicKey{N: big.NewInt(17), E: 3}, identity, now, time.Hour); err == nil {
		t.Fatal("weak public key accepted")
	}
	if _, err := IssueClient(ca.CA, ca.CAKey, nil, identity, now, time.Hour); err == nil {
		t.Fatal("missing public key accepted")
	}
}

func TestParseIdentityRejectsMalformedAmbiguousAndUnknownIdentities(t *testing.T) {
	ca, public, _, identity, now := identityFixture(t)
	data, err := IssueClient(ca.CA, ca.CAKey, public, identity, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf := identityLeaf(t, data)
	if _, err := ParseIdentity(nil); err == nil {
		t.Fatal("nil identity accepted")
	}
	for name, mutate := range map[string]func(*x509.Certificate){
		"missing":                  func(c *x509.Certificate) { c.URIs = nil },
		"multiple":                 func(c *x509.Certificate) { c.URIs = append(c.URIs, c.URIs[0]) },
		"subject-only":             func(c *x509.Certificate) { c.URIs = nil; c.Subject.CommonName = "agent_test" },
		"unknown-purpose":          func(c *x509.Certificate) { c.URIs[0].Path = "/v1/instances/instance_test/agents/agent_test/admin/2" },
		"unknown-instance-path":    func(c *x509.Certificate) { c.URIs[0].Path = "/v2/instances/instance_test/agents/agent_test/device/2" },
		"noncanonical-generation":  func(c *x509.Certificate) { c.URIs[0].Path = "/v1/instances/instance_test/agents/agent_test/device/02" },
		"missing-owner-generation": func(c *x509.Certificate) { c.URIs[0].Path = "/v1/instances/instance_test/agents/agent_test/owner/2" },
		"zero-owner-generation":    func(c *x509.Certificate) { c.URIs[0].Path = "/v1/instances/instance_test/agents/agent_test/owner/2/0" },
		"extra-path":               func(c *x509.Certificate) { c.URIs[0].Path += "/extra" },
		"query":                    func(c *x509.Certificate) { c.URIs[0].RawQuery = "agent=other" },
		"empty-query":              func(c *x509.Certificate) { c.URIs[0].ForceQuery = true },
		"fragment":                 func(c *x509.Certificate) { c.URIs[0].Fragment = "other" },
		"escaped": func(c *x509.Certificate) {
			c.URIs[0].RawPath = "/v1/instances/%69nstance_test/agents/agent_test/device/2"
		},
		"wrong-authority":   func(c *x509.Certificate) { c.URIs[0].Host = "other" },
		"user-info":         func(c *x509.Certificate) { c.URIs[0].User = url.User("other") },
		"dns-alias":         func(c *x509.Certificate) { c.DNSNames = []string{"agent_other"} },
		"server-purpose":    func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} },
		"any-purpose":       func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny} },
		"multiple-purposes": func(c *x509.Certificate) { c.ExtKeyUsage = append(c.ExtKeyUsage, x509.ExtKeyUsageServerAuth) },
		"unknown-eku":       func(c *x509.Certificate) { c.UnknownExtKeyUsage = []asn1.ObjectIdentifier{{1, 2, 3, 4}} },
		"ca":                func(c *x509.Certificate) { c.IsCA = true; c.KeyUsage |= x509.KeyUsageCertSign },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *leaf
			uri := *leaf.URIs[0]
			changed.URIs = []*url.URL{&uri}
			changed.ExtKeyUsage = append([]x509.ExtKeyUsage{}, leaf.ExtKeyUsage...)
			mutate(&changed)
			changedData := signIdentityLeaf(t, ca, &changed, public)
			if _, err := ParseIdentity(identityLeaf(t, changedData)); err == nil {
				t.Fatal("malformed/ambiguous identity accepted")
			}
		})
	}
	unknownNames, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(leaf.URIs[0].String())},
		{Class: asn1.ClassContextSpecific, Tag: 8, Bytes: []byte{42, 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownSAN := *leaf
	unknownSAN.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Value: unknownNames}}
	if _, err := ParseIdentity(identityLeaf(t, signIdentityLeaf(t, ca, &unknownSAN, public))); err == nil {
		t.Fatal("unknown extra SAN identity accepted")
	}
	changed := *leaf
	changed.Subject = pkix.Name{CommonName: "spoofed-agent"}
	parsed, err := ParseIdentity(identityLeaf(t, signIdentityLeaf(t, ca, &changed, public)))
	if err != nil || parsed != identity {
		t.Fatal("subject was treated as identity", err)
	}
}

func mutualHandshake(t *testing.T, server, client *tls.Config) (error, error, uint16) {
	t.Helper()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	deadline := time.Now().Add(3 * time.Second)
	left.SetDeadline(deadline)
	right.SetDeadline(deadline)
	serverTLS := tls.Server(left, server)
	clientTLS := tls.Client(right, client)
	done := make(chan error, 1)
	go func() { done <- serverTLS.Handshake() }()
	clientErr := clientTLS.Handshake()
	serverErr := <-done
	return serverErr, clientErr, clientTLS.ConnectionState().Version
}

func TestServerMutualRequiresVerifiedSignedClientIdentity(t *testing.T) {
	ca, public, private, identity, now := identityFixture(t)
	root := filepath.Join(t.TempDir(), "private")
	if err := ca.Write(root); err != nil {
		t.Fatal(err)
	}
	serverTLS, err := ServerMutual(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"), filepath.Join(root, "ca.pem"))
	if err != nil || serverTLS.MinVersion != tls.VersionTLS13 || serverTLS.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("invalid server TLS configuration", err)
	}
	clientTLS, err := Client(filepath.Join(root, "ca.pem"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := IssueClient(ca.CA, ca.CAKey, public, identity, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair := identityPair(t, certPEM, private)
	clientTLS.Certificates = []tls.Certificate{pair}
	serverErr, clientErr, version := mutualHandshake(t, serverTLS, clientTLS)
	if serverErr != nil || clientErr != nil || version != tls.VersionTLS13 {
		t.Fatal("valid mutual TLS failed", serverErr, clientErr)
	}
	other, err := Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	wrongCert, err := IssueClient(other.CA, other.CAKey, public, identity, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrong := identityPair(t, wrongCert, private)
	expired := *identityLeaf(t, certPEM)
	expired.NotBefore, expired.NotAfter = now.Add(-2*time.Hour), now.Add(-time.Hour)
	expiredPair := identityPair(t, signIdentityLeaf(t, ca, &expired, public), private)
	unknown := *identityLeaf(t, certPEM)
	unknown.URIs = []*url.URL{{Scheme: "spiffe", Host: "other", Path: "/identity"}}
	unknownPair := identityPair(t, signIdentityLeaf(t, ca, &unknown, public), private)
	for name, pair := range map[string]*tls.Certificate{"missing": nil, "wrong-ca": &wrong, "expired": &expiredPair, "unknown-identity": &unknownPair} {
		t.Run(name, func(t *testing.T) {
			client := clientTLS.Clone()
			client.Certificates = nil
			client.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				if pair == nil {
					return &tls.Certificate{}, nil
				}
				return pair, nil
			}
			serverErr, _, _ := mutualHandshake(t, serverTLS, client)
			if serverErr == nil {
				t.Fatal("unverified client accepted")
			}
		})
	}
	legacy := clientTLS.Clone()
	legacy.MinVersion, legacy.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if serverErr, clientErr, _ := mutualHandshake(t, serverTLS, legacy); serverErr == nil && clientErr == nil {
		t.Fatal("TLS 1.2 accepted")
	}
	if err := secure.Write(filepath.Join(root, "server-key.pem"), other.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := ServerMutual(filepath.Join(root, "server.pem"), filepath.Join(root, "server-key.pem"), filepath.Join(root, "ca.pem")); err == nil {
		t.Fatal("mismatched server key accepted")
	}
}
