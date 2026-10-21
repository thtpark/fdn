package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func IssueClient(caPEM, caKeyPEM []byte, public crypto.PublicKey, identity domain.CertificateIdentity, now time.Time, ttl time.Duration) ([]byte, error) {
	uri, err := identityURI(identity)
	if err != nil {
		return nil, err
	}
	if err := signingPublic(public); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		return nil, domain.Fail(domain.Invalid, "client certificate lifetime must be positive")
	}
	ca, err := identityCA(caPEM, now)
	if err != nil {
		return nil, err
	}
	block, err := identityPEM(caKeyPEM, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Invalid, "invalid CA PKCS#8 private key")
	}
	signer, ok := private.(crypto.Signer)
	if !ok {
		return nil, domain.Fail(domain.Invalid, "CA private key cannot sign certificates")
	}
	encoded, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(encoded, ca.RawSubjectPublicKeyInfo) {
		return nil, domain.Fail(domain.Denied, "CA private key does not match pinned trust")
	}
	expiry := now.Add(ttl).Truncate(time.Second)
	if expiry.After(ca.NotAfter) {
		expiry = ca.NotAfter
	}
	if !expiry.After(now) {
		return nil, domain.Fail(domain.Invalid, "client certificate lifetime is outside CA validity")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	serial.SetBit(serial, 128, 1)
	notBefore := now.Add(-time.Minute)
	if notBefore.Before(ca.NotBefore) {
		notBefore = ca.NotBefore
	}
	leaf := &x509.Certificate{SerialNumber: serial,
		Subject: pkix.Name{CommonName: "Foundation " + identity.Purpose}, NotBefore: notBefore, NotAfter: expiry,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{uri}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, signer)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// ParseIdentity reads only the signed, canonical URI SAN, never the subject or HTTP metadata.
// Certificate chain and time verification remain the TLS verifier's responsibility.
func ParseIdentity(cert *x509.Certificate) (domain.CertificateIdentity, error) {
	var identity domain.CertificateIdentity
	if cert == nil || cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage != x509.KeyUsageDigitalSignature ||
		len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(cert.UnknownExtKeyUsage) != 0 ||
		len(cert.UnhandledCriticalExtensions) != 0 || len(cert.URIs) != 1 || cert.URIs[0] == nil ||
		len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 {
		return identity, domain.Fail(domain.Denied, "certificate is not a single-purpose Foundation client identity")
	}
	if err := signingPublic(cert.PublicKey); err != nil {
		return identity, err
	}
	uri := cert.URIs[0]
	if !singleIdentitySAN(cert, uri) {
		return identity, domain.Fail(domain.Denied, "certificate SAN must contain exactly one identity URI")
	}
	if uri.Scheme != "spiffe" || uri.Host != "foundation" || uri.User != nil || uri.Opaque != "" || uri.RawQuery != "" ||
		uri.ForceQuery || uri.Fragment != "" || uri.RawFragment != "" || uri.RawPath != "" {
		return identity, domain.Fail(domain.Denied, "unknown Foundation certificate identity URI")
	}
	parts := strings.Split(uri.Path, "/")
	if (len(parts) != 8 && len(parts) != 9) || parts[0] != "" || parts[1] != "v1" || parts[2] != "instances" || parts[4] != "agents" {
		return identity, domain.Fail(domain.Denied, "malformed Foundation certificate identity URI")
	}
	generation, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return identity, domain.Fail(domain.Denied, "malformed certificate key generation")
	}
	identity = domain.CertificateIdentity{InstanceID: parts[3], AgentID: parts[5], Purpose: parts[6], Generation: generation}
	if len(parts) == 9 {
		identity.OwnerGeneration, err = strconv.ParseInt(parts[8], 10, 64)
		if err != nil {
			return domain.CertificateIdentity{}, domain.Fail(domain.Denied, "malformed certificate owner generation")
		}
	}
	canonical, err := identityURI(identity)
	if err != nil || canonical.String() != uri.String() {
		return domain.CertificateIdentity{}, domain.Fail(domain.Denied, "noncanonical or unknown Foundation certificate identity")
	}
	return identity, nil
}

func ServerMutual(certFile, keyFile, caFile string) (*tls.Config, error) {
	tlsConfig, err := Server(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := secure.ReadPublic(caFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ca, err := identityCA(caPEM, now)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	chain := tlsConfig.Certificates[0].Certificate
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, err
	}
	if leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 {
		return nil, domain.Fail(domain.Invalid, "mutual TLS server requires a dedicated server certificate")
	}
	if err := signingPublic(leaf.PublicKey); err != nil {
		return nil, err
	}
	intermediates := x509.NewCertPool()
	for _, der := range chain[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return nil, domain.Fail(domain.Denied, "server certificate is expired or does not chain to the pinned CA")
	}
	tlsConfig.Certificates[0].Leaf = leaf
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	tlsConfig.ClientCAs = pool
	tlsConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return domain.Fail(domain.Denied, "verified Foundation client certificate required")
		}
		_, err := ParseIdentity(state.PeerCertificates[0])
		return err
	}
	return tlsConfig, nil
}

func singleIdentitySAN(cert *x509.Certificate, uri *url.URL) bool {
	count := 0
	for _, extension := range cert.Extensions {
		if !extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			continue
		}
		count++
		var names []asn1.RawValue
		rest, err := asn1.Unmarshal(extension.Value, &names)
		if err != nil || len(rest) != 0 || len(names) != 1 || names[0].Class != asn1.ClassContextSpecific ||
			names[0].Tag != 6 || names[0].IsCompound || string(names[0].Bytes) != uri.String() {
			return false
		}
	}
	return count == 1
}

func identityURI(identity domain.CertificateIdentity) (*url.URL, error) {
	if !domain.ValidOperation(identity.InstanceID) || !domain.ValidOperation(identity.AgentID) || identity.Generation < 1 ||
		(identity.Purpose != "device" && identity.Purpose != "owner") ||
		(identity.Purpose == "device" && identity.OwnerGeneration != 0) || (identity.Purpose == "owner" && identity.OwnerGeneration < 1) {
		return nil, domain.Fail(domain.Invalid, "certificate identity requires a valid instance, agent, key generation, and distinct device or owner purpose")
	}
	path := "/v1/instances/" + identity.InstanceID + "/agents/" + identity.AgentID + "/" + identity.Purpose + "/" + strconv.FormatInt(identity.Generation, 10)
	if identity.Purpose == "owner" {
		path += "/" + strconv.FormatInt(identity.OwnerGeneration, 10)
	}
	return &url.URL{Scheme: "spiffe", Host: "foundation", Path: path}, nil
}

func identityCA(data []byte, now time.Time) (*x509.Certificate, error) {
	block, err := identityPEM(data, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Invalid, "invalid pinned CA certificate")
	}
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || len(ca.UnhandledCriticalExtensions) != 0 ||
		now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) || ca.CheckSignatureFrom(ca) != nil {
		return nil, domain.Fail(domain.Prerequisite, "pinned CA is invalid, not yet valid, or expired")
	}
	if err := signingPublic(ca.PublicKey); err != nil {
		return nil, err
	}
	switch ca.SignatureAlgorithm {
	case x509.PureEd25519, x509.ECDSAWithSHA256, x509.ECDSAWithSHA384, x509.ECDSAWithSHA512,
		x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA,
		x509.SHA256WithRSAPSS, x509.SHA384WithRSAPSS, x509.SHA512WithRSAPSS:
		return ca, nil
	default:
		return nil, domain.Fail(domain.Invalid, "pinned CA uses an unsupported or weak signature algorithm")
	}
}

func identityPEM(data []byte, kind string) (*pem.Block, error) {
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "certificate or key exceeds its size limit")
	}
	data = bytes.TrimSpace(data)
	if !bytes.HasPrefix(data, []byte("-----BEGIN "+kind+"-----")) {
		return nil, domain.Fail(domain.Invalid, "unexpected certificate or key PEM format")
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != kind || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
		bytes.Count(data, []byte("-----BEGIN ")) != 1 || bytes.Count(data, []byte("-----END ")) != 1 {
		return nil, domain.Fail(domain.Invalid, "exactly one certificate or unencrypted PKCS#8 key is required")
	}
	return block, nil
}

func signingPublic(public crypto.PublicKey) error {
	switch key := public.(type) {
	case ed25519.PublicKey:
		if len(key) == ed25519.PublicKeySize {
			return nil
		}
	case *ecdsa.PublicKey:
		if key != nil && (key.Curve == elliptic.P256() || key.Curve == elliptic.P384() || key.Curve == elliptic.P521()) &&
			key.X != nil && key.Y != nil && key.Curve.IsOnCurve(key.X, key.Y) {
			return nil
		}
	case *rsa.PublicKey:
		if key != nil && key.N != nil && key.N.Sign() > 0 && key.N.BitLen() >= 2048 && key.N.BitLen() <= 8192 && key.E >= 65537 && key.E%2 == 1 {
			return nil
		}
	}
	return domain.Fail(domain.Invalid, "unsupported or weak certificate signing key")
}
