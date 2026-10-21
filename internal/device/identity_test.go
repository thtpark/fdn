package device

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/crypto/ssh"
)

func generated(t *testing.T) (domain.PublicIdentity, []byte, crypto.Signer) {
	t.Helper()
	public, sshPEM, authPEM, err := Generate("worker")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ParseSigner(authPEM)
	if err != nil {
		t.Fatal(err)
	}
	return public, sshPEM, signer
}

func TestPublicIdentityIndependentKeysAndStableRequest(t *testing.T) {
	public, sshPEM, authSigner := generated(t)
	sshFP, authFP, err := Validate(public)
	if err != nil || public.Version != 2 || !validNonce(public.RequestID) || sshFP == authFP {
		t.Fatal("invalid public identity", err)
	}
	sshSigner, err := ssh.ParsePrivateKey(sshPEM)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(sshSigner.PublicKey()) != sshFP {
		t.Fatal("SSH private/public mismatch")
	}
	authKey, err := PublicKey(public.AuthKey)
	if err != nil || !bytes.Equal(authKey.(ed25519.PublicKey), authSigner.Public().(ed25519.PublicKey)) {
		t.Fatal("authentication private/public mismatch", err)
	}
	sshPublic, err := SSHPublicKey(public.SSHKey)
	if err != nil || bytes.Equal(sshPublic.(ed25519.PublicKey), authKey.(ed25519.PublicKey)) {
		t.Fatal("keys are not independent", err)
	}
	data, err := json.Marshal(public)
	if err != nil || bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, authSigner.(ed25519.PrivateKey).Seed()) {
		t.Fatal("public request contains secrets", err)
	}
	var roundtrip domain.PublicIdentity
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip != public {
		t.Fatal("public request did not retain exact request ID/proofs")
	}
	another, _, _, err := Generate("worker")
	if err != nil || another.RequestID == public.RequestID || another.SSHKey == public.SSHKey || another.AuthKey == public.AuthKey {
		t.Fatal("generation reused identity material", err)
	}
}

func TestPublicIdentityCanonicalAliases(t *testing.T) {
	public, _, _ := generated(t)
	sshFP, authFP, err := Validate(public)
	if err != nil {
		t.Fatal(err)
	}
	public.SSHKey = "  " + strings.TrimSpace(public.SSHKey) + "  local key annotation\n"
	public.AuthKey = " \r\n" + strings.ReplaceAll(public.AuthKey, "\n", "\r\n") + "\t\r\n"
	otherSSH, otherAuth, err := Validate(public)
	if err != nil || sshFP != otherSSH || authFP != otherAuth {
		t.Fatal("aliases changed key identity or proof payload", err)
	}
	sshPublic, err := SSHPublicKey(public.SSHKey)
	if err != nil {
		t.Fatal(err)
	}
	sshDERFingerprint, err := Fingerprint(sshPublic)
	if err != nil {
		t.Fatal(err)
	}
	sshPEM, err := encodePublic(sshPublic)
	if err != nil {
		t.Fatal(err)
	}
	sameAuth, err := PublicKey(sshPEM)
	if err != nil {
		t.Fatal(err)
	}
	crossRole, err := Fingerprint(sameAuth)
	if err != nil || sshDERFingerprint != crossRole {
		t.Fatal("cross-role aliases have different fingerprints", err)
	}
}

func TestPublicIdentityProofsBindEveryField(t *testing.T) {
	public, _, _ := generated(t)
	other, _, _ := generated(t)
	cases := map[string]func(*domain.PublicIdentity){
		"version":            func(v *domain.PublicIdentity) { v.Version = 1 },
		"request":            func(v *domain.PublicIdentity) { v.RequestID = other.RequestID },
		"name":               func(v *domain.PublicIdentity) { v.Name = "other worker" },
		"ssh":                func(v *domain.PublicIdentity) { v.SSHKey = other.SSHKey },
		"auth":               func(v *domain.PublicIdentity) { v.AuthKey = other.AuthKey },
		"ssh-proof":          func(v *domain.PublicIdentity) { v.SSHProof = other.SSHProof },
		"auth-proof":         func(v *domain.PublicIdentity) { v.AuthProof = other.AuthProof },
		"swapped-proofs":     func(v *domain.PublicIdentity) { v.SSHProof, v.AuthProof = v.AuthProof, v.SSHProof },
		"missing-proof":      func(v *domain.PublicIdentity) { v.AuthProof = "" },
		"noncanonical-proof": func(v *domain.PublicIdentity) { v.AuthProof += "=" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := public
			mutate(&changed)
			if _, _, err := Validate(changed); err == nil {
				t.Fatal("changed request accepted")
			}
		})
	}
	if bytes.Equal(frame("ab", "c"), frame("a", "bc")) {
		t.Fatal("ambiguous field framing")
	}
}

func TestRejectUnsafeSSHAndPEMFormats(t *testing.T) {
	public, sshPrivate, signer := generated(t)
	for _, value := range []string{
		"", "command=fdn " + public.SSHKey, "restrict " + public.SSHKey,
		public.SSHKey + public.SSHKey, "# leading comment\n" + public.SSHKey,
		strings.TrimSpace(public.SSHKey) + " " + strings.TrimSpace(public.SSHKey),
		public.SSHKey + "garbage", public.SSHKey + "\x00", strings.Repeat("x", maxKeyBytes+1),
	} {
		if _, err := SSHPublicKey(value); err == nil {
			t.Fatal("unsafe SSH key accepted")
		}
	}
	block, _ := pem.Decode([]byte(public.AuthKey))
	withHeaders := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Headers: map[string]string{"Comment": "not allowed"}, Bytes: block.Bytes})
	for _, value := range []string{
		"", "preamble\n" + public.AuthKey, public.AuthKey + "trailer",
		"-----BEGIN PUBLIC KEY-----\ninvalid\n-----END PUBLIC KEY-----\n" + public.AuthKey,
		public.AuthKey + public.AuthKey, string(sshPrivate), string(withHeaders),
		strings.Repeat("x", maxKeyBytes+1),
	} {
		if _, err := PublicKey(value); err == nil {
			t.Fatal("unsafe public PEM accepted")
		}
	}
	if _, err := ParseSigner(sshPrivate); err == nil {
		t.Fatal("SSH private key accepted for authentication")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if _, err := ParseSigner(append(privatePEM, privatePEM...)); err == nil {
		t.Fatal("multiple private keys accepted")
	}
	weak, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	weakDER, err := x509.MarshalPKIXPublicKey(&weak.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublicKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: weakDER}))); err == nil {
		t.Fatal("non-Ed25519 authentication key accepted")
	}
	sshKey, err := ssh.NewPublicKey(&weak.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SSHPublicKey(string(ssh.MarshalAuthorizedKey(sshKey))); err == nil {
		t.Fatal("non-Ed25519 SSH key accepted")
	}
	public.AuthKey, err = encodePublic(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err = ssh.NewPublicKey(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	public.SSHKey = string(ssh.MarshalAuthorizedKey(sshKey))
	if _, _, err := Validate(public); err == nil {
		t.Fatal("one key reused for both roles")
	}
}

func TestChallengeAndOwnerProofBinding(t *testing.T) {
	_, _, signer := generated(t)
	_, _, owner := generated(t)
	challenge := domain.IdentityChallenge{InstanceID: "instance_test", AgentID: "agent_test", Generation: 3, Nonce: domain.NewToken(), ExpiresAt: 2000000000}
	proof, err := SignChallenge(signer, challenge)
	if err != nil || VerifyChallenge(signer.Public(), challenge, proof) != nil {
		t.Fatal("activation proof failed", err)
	}
	for _, mutate := range []func(*domain.IdentityChallenge){
		func(c *domain.IdentityChallenge) { c.InstanceID = "instance_other" },
		func(c *domain.IdentityChallenge) { c.AgentID = "agent_other" },
		func(c *domain.IdentityChallenge) { c.Generation++ },
		func(c *domain.IdentityChallenge) { c.Nonce = domain.NewToken() },
		func(c *domain.IdentityChallenge) { c.ExpiresAt++ },
	} {
		changed := challenge
		mutate(&changed)
		if VerifyChallenge(signer.Public(), changed, proof) == nil {
			t.Fatal("activation proof replayed with different binding")
		}
	}
	identity := domain.AgentIdentity{InstanceID: challenge.InstanceID, AgentID: challenge.AgentID, Generation: challenge.Generation}
	req, err := SignOwner(owner, identity, domain.NewToken())
	if err != nil {
		t.Fatal(err)
	}
	public, err := VerifyOwner(req, identity)
	if err != nil || !bytes.Equal(public.(ed25519.PublicKey), owner.Public().(ed25519.PublicKey)) {
		t.Fatal("owner proof failed", err)
	}
	alias := req
	alias.OwnerKey = strings.ReplaceAll(alias.OwnerKey, "\n", "\r\n")
	if _, err := VerifyOwner(alias, identity); err != nil {
		t.Fatal("owner proof did not canonicalize key", err)
	}
	for _, mutate := range []func(*domain.AgentIdentity){
		func(i *domain.AgentIdentity) { i.InstanceID = "instance_other" },
		func(i *domain.AgentIdentity) { i.AgentID = "agent_other" },
		func(i *domain.AgentIdentity) { i.Generation++ },
	} {
		changed := identity
		mutate(&changed)
		if _, err := VerifyOwner(req, changed); err == nil {
			t.Fatal("owner proof replayed with different identity")
		}
	}
	changed := req
	changed.Nonce = domain.NewToken()
	if _, err := VerifyOwner(changed, identity); err == nil {
		t.Fatal("owner proof replayed with different nonce")
	}
	changed = req
	changed.OwnerKey, err = encodePublic(signer.Public())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOwner(changed, identity); err == nil {
		t.Fatal("owner key changed without proof")
	}
	changed = req
	changed.Proof = proof
	if _, err := VerifyOwner(changed, identity); err == nil {
		t.Fatal("activation signature accepted as owner proof")
	}
	if VerifyChallenge(owner.Public(), challenge, req.Proof) == nil {
		t.Fatal("owner signature accepted as activation proof")
	}
	for _, nonce := range []string{"", "nonce", domain.NewToken() + "=", strings.Repeat("x", 4097)} {
		if _, err := SignOwner(owner, identity, nonce); err == nil {
			t.Fatal("malformed nonce accepted")
		}
	}
	for _, invalid := range []crypto.Signer{nil, ed25519.PrivateKey(nil), ed25519.PrivateKey{1, 2}} {
		if _, err := SignOwner(invalid, identity, domain.NewToken()); err == nil {
			t.Fatal("invalid owner signer accepted")
		}
		if _, err := SignChallenge(invalid, challenge); err == nil {
			t.Fatal("invalid challenge signer accepted")
		}
	}
}
