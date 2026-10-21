package device

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/crypto/ssh"
)

const (
	RequestVersion = 2
	maxKeyBytes    = 4096
)

func Generate(name string) (domain.PublicIdentity, []byte, []byte, error) {
	if err := domain.CheckText(name, "agent name", 128, true); err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	sshPublic, sshPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	authPublic, authPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	requestID := make([]byte, 32)
	if _, err := rand.Read(requestID); err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	sshKey, err := ssh.NewPublicKey(sshPublic)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	authKey, err := encodePublic(authPublic)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	public := domain.PublicIdentity{Version: RequestVersion, Name: name,
		RequestID: base64.RawURLEncoding.EncodeToString(requestID),
		SSHKey:    string(ssh.MarshalAuthorizedKey(sshKey)), AuthKey: authKey}
	payload, err := requestPayload(public)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	public.SSHProof, err = sign(sshPrivate, payload)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	public.AuthProof, err = sign(authPrivate, payload)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	sshBlock, err := ssh.MarshalPrivateKey(sshPrivate, "")
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	authDER, err := x509.MarshalPKCS8PrivateKey(authPrivate)
	if err != nil {
		return domain.PublicIdentity{}, nil, nil, err
	}
	return public, pem.EncodeToMemory(sshBlock), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: authDER}), nil
}

func Validate(public domain.PublicIdentity) (string, string, error) {
	payload, err := requestPayload(public)
	if err != nil {
		return "", "", err
	}
	sshKey, err := parseSSH(public.SSHKey)
	if err != nil {
		return "", "", err
	}
	authKey, err := PublicKey(public.AuthKey)
	if err != nil {
		return "", "", err
	}
	if err := verify(sshKey.(ssh.CryptoPublicKey).CryptoPublicKey(), payload, public.SSHProof); err != nil {
		return "", "", err
	}
	if err := verify(authKey, payload, public.AuthProof); err != nil {
		return "", "", err
	}
	authFingerprint, err := Fingerprint(authKey)
	if err != nil {
		return "", "", err
	}
	return ssh.FingerprintSHA256(sshKey), authFingerprint, nil
}

func ParseSigner(authPEM []byte) (crypto.Signer, error) {
	block, err := decodePEM(authPEM, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Invalid, "invalid PKCS#8 authentication private key")
	}
	key, ok := private.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, domain.Fail(domain.Invalid, "authentication private key must be Ed25519")
	}
	return key, nil
}

func PublicKey(value string) (crypto.PublicKey, error) {
	block, err := decodePEM([]byte(value), "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	public, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, domain.Fail(domain.Invalid, "invalid PKIX authentication public key")
	}
	if _, err := ed25519Public(public); err != nil {
		return nil, err
	}
	return public, nil
}

func SSHPublicKey(value string) (crypto.PublicKey, error) {
	key, err := parseSSH(value)
	if err != nil {
		return nil, err
	}
	return key.(ssh.CryptoPublicKey).CryptoPublicKey(), nil
}

// Fingerprint uses canonical PKIX DER for comparisons across SSH and authentication roles.
func Fingerprint(public crypto.PublicKey) (string, error) {
	if _, err := ed25519Public(public); err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(der)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

func SignChallenge(signer crypto.Signer, challenge domain.IdentityChallenge) (string, error) {
	payload, err := challengePayload(challenge)
	if err != nil {
		return "", err
	}
	return sign(signer, payload)
}

func VerifyChallenge(public crypto.PublicKey, challenge domain.IdentityChallenge, proof string) error {
	payload, err := challengePayload(challenge)
	if err != nil {
		return err
	}
	return verify(public, payload, proof)
}

func SignOwner(signer crypto.Signer, identity domain.AgentIdentity, nonce string) (domain.OwnerRequest, error) {
	public, err := signerPublic(signer)
	if err != nil {
		return domain.OwnerRequest{}, err
	}
	key, err := encodePublic(public)
	if err != nil {
		return domain.OwnerRequest{}, err
	}
	req := domain.OwnerRequest{OwnerKey: key, Nonce: nonce}
	payload, _, err := ownerPayload(req, identity)
	if err != nil {
		return domain.OwnerRequest{}, err
	}
	req.Proof, err = sign(signer, payload)
	if err != nil {
		return domain.OwnerRequest{}, err
	}
	return req, nil
}

func VerifyOwner(req domain.OwnerRequest, identity domain.AgentIdentity) (crypto.PublicKey, error) {
	payload, public, err := ownerPayload(req, identity)
	if err != nil {
		return nil, err
	}
	if err := verify(public, payload, req.Proof); err != nil {
		return nil, err
	}
	return public, nil
}

func requestPayload(public domain.PublicIdentity) ([]byte, error) {
	if public.Version != RequestVersion || !validNonce(public.RequestID) {
		return nil, domain.Fail(domain.Invalid, "public identity requires version 2 and a random 32-byte request ID")
	}
	if err := domain.CheckText(public.Name, "agent name", 128, true); err != nil {
		return nil, err
	}
	sshKey, err := parseSSH(public.SSHKey)
	if err != nil {
		return nil, err
	}
	authKey, err := PublicKey(public.AuthKey)
	if err != nil {
		return nil, err
	}
	sshPublic := sshKey.(ssh.CryptoPublicKey).CryptoPublicKey().(ed25519.PublicKey)
	if bytes.Equal(sshPublic, authKey.(ed25519.PublicKey)) {
		return nil, domain.Fail(domain.Invalid, "SSH and authentication keys must be independent")
	}
	authDER, err := x509.MarshalPKIXPublicKey(authKey)
	if err != nil {
		return nil, err
	}
	return frame("foundation.device.request.v2", strconv.Itoa(public.Version), public.Name, public.RequestID,
		string(sshKey.Marshal()), string(authDER)), nil
}

func challengePayload(challenge domain.IdentityChallenge) ([]byte, error) {
	if err := validateBinding(challenge.InstanceID, challenge.AgentID, challenge.Generation, challenge.Nonce); err != nil {
		return nil, err
	}
	if challenge.ExpiresAt <= 0 {
		return nil, domain.Fail(domain.Invalid, "identity challenge requires an expiry")
	}
	return frame("foundation.device.activation.v2", challenge.InstanceID, challenge.AgentID,
		strconv.FormatInt(challenge.Generation, 10), challenge.Nonce, strconv.FormatInt(challenge.ExpiresAt, 10)), nil
}

func ownerPayload(req domain.OwnerRequest, identity domain.AgentIdentity) ([]byte, crypto.PublicKey, error) {
	if err := validateBinding(identity.InstanceID, identity.AgentID, identity.Generation, req.Nonce); err != nil {
		return nil, nil, err
	}
	public, err := PublicKey(req.OwnerKey)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return nil, nil, err
	}
	return frame("foundation.device.owner.v2", identity.InstanceID, identity.AgentID,
		strconv.FormatInt(identity.Generation, 10), string(der), req.Nonce), public, nil
}

func validateBinding(instance, agent string, generation int64, nonce string) error {
	if !domain.ValidOperation(instance) || !domain.ValidOperation(agent) || generation < 1 || !validNonce(nonce) {
		return domain.Fail(domain.Invalid, "identity proof requires an instance, agent, positive key generation, and random 32-byte nonce")
	}
	return nil
}

func validNonce(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func frame(fields ...string) []byte {
	var payload bytes.Buffer
	for _, field := range fields {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		payload.Write(size[:])
		payload.WriteString(field)
	}
	return payload.Bytes()
}

func sign(signer crypto.Signer, payload []byte) (string, error) {
	public, err := signerPublic(signer)
	if err != nil {
		return "", err
	}
	signature, err := signer.Sign(rand.Reader, payload, crypto.Hash(0))
	if err != nil {
		return "", err
	}
	if err := verify(public, payload, base64.RawURLEncoding.EncodeToString(signature)); err != nil {
		return "", domain.Fail(domain.Invalid, "signer returned an invalid Ed25519 signature")
	}
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

func verify(public crypto.PublicKey, payload []byte, proof string) error {
	key, err := ed25519Public(public)
	if err != nil {
		return err
	}
	if len(proof) != base64.RawURLEncoding.EncodedLen(ed25519.SignatureSize) {
		return domain.Fail(domain.Denied, "invalid identity proof")
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(proof)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != proof || !ed25519.Verify(key, payload, signature) {
		return domain.Fail(domain.Denied, "invalid identity proof")
	}
	return nil
}

func encodePublic(public crypto.PublicKey) (string, error) {
	if _, err := ed25519Public(public); err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

func signerPublic(signer crypto.Signer) (crypto.PublicKey, error) {
	if signer == nil {
		return nil, domain.Fail(domain.Invalid, "Ed25519 signer is required")
	}
	value := reflect.ValueOf(signer)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice) && value.IsNil() {
		return nil, domain.Fail(domain.Invalid, "Ed25519 signer is required")
	}
	if key, ok := signer.(ed25519.PrivateKey); ok && len(key) != ed25519.PrivateKeySize {
		return nil, domain.Fail(domain.Invalid, "invalid Ed25519 private key")
	}
	public := signer.Public()
	if _, err := ed25519Public(public); err != nil {
		return nil, err
	}
	return public, nil
}

func ed25519Public(public crypto.PublicKey) (ed25519.PublicKey, error) {
	key, ok := public.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, domain.Fail(domain.Invalid, "public key must be Ed25519")
	}
	return key, nil
}

func decodePEM(value []byte, kind string) (*pem.Block, error) {
	if len(value) > maxKeyBytes {
		return nil, domain.Fail(domain.Invalid, "identity key exceeds its size limit")
	}
	value = bytes.TrimSpace(value)
	if !bytes.HasPrefix(value, []byte("-----BEGIN "+kind+"-----")) {
		return nil, domain.Fail(domain.Invalid, "identity key has an unsupported PEM format")
	}
	block, rest := pem.Decode(value)
	if block == nil || block.Type != kind || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
		bytes.Count(value, []byte("-----BEGIN ")) != 1 || bytes.Count(value, []byte("-----END ")) != 1 {
		return nil, domain.Fail(domain.Invalid, "identity key must contain exactly one unencrypted PEM block")
	}
	return block, nil
}

func parseSSH(value string) (ssh.PublicKey, error) {
	if len(value) > maxKeyBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return nil, domain.Fail(domain.Invalid, "SSH public key is invalid or oversized")
	}
	line := strings.TrimSpace(value)
	if strings.ContainsAny(line, "\r\n") {
		return nil, domain.Fail(domain.Invalid, "SSH public key must contain exactly one key")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 || key.Type() != ssh.KeyAlgoED25519 {
		return nil, domain.Fail(domain.Invalid, "SSH public key must be a plain Ed25519 key without options")
	}
	public, ok := key.(ssh.CryptoPublicKey)
	if !ok {
		return nil, domain.Fail(domain.Invalid, "unsupported SSH public key")
	}
	if _, err := ed25519Public(public.CryptoPublicKey()); err != nil {
		return nil, err
	}
	fields := strings.Fields(comment)
	for i := range fields {
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[i:], " "))); err == nil {
			return nil, domain.Fail(domain.Invalid, "SSH public key contains a trailing extra key")
		}
	}
	return key, nil
}
