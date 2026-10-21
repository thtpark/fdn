//go:build integration && (darwin || linux)

package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
)

func TestUnixIdentityBoundaryRejectsSpoofedCredentials(t *testing.T) {
	h := newIdentityHarness(t, true)
	deviceCertificate := h.activate(t)
	deviceClient := h.client(t, h.joinSocket, deviceCertificate)
	owner := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	identityMe(t, owner.client, h.identity.AgentID)
	if h.peerUID.Load() != h.identity.UID {
		t.Fatal("request did not carry the actual Unix peer UID")
	}
	ctx := context.Background()
	invitation, token := domain.NewToken(), domain.NewToken()
	if _, err := h.store.Invite(ctx, h.identity.AgentID, domain.InvitationRequest{Command: command(), TokenHash: domain.Hash(invitation), ExpiresAt: time.Now().Unix() + 60}); err != nil {
		t.Fatal(err)
	}
	legacy, err := h.store.Enroll(ctx, domain.EnrollRequest{Command: command(), Name: "legacy bearer", InvitationToken: invitation, CredentialHash: domain.Hash(token)})
	if err != nil {
		t.Fatal(err)
	}
	if authenticated, err := h.store.Authenticate(ctx, token); err != nil || authenticated.ID != legacy.ID {
		t.Fatal("negative test did not seed a valid legacy bearer", err)
	}
	request, err := http.NewRequest("GET", owner.client.BaseURL+"/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	for _, header := range []string{"X-Fdn-Agent-ID", "X-Fdn-Identity", "X-Agent-ID", "X-Forwarded-User", "X-Remote-User"} {
		request.Header.Set(header, legacy.ID)
	}
	request.Header.Set("X-Fdn-UID", "0")
	response, err := owner.client.HTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope api.Envelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var authenticated domain.Agent
	if err := json.Unmarshal(envelope.Data, &authenticated); err != nil || !envelope.OK || authenticated.ID != h.identity.AgentID {
		t.Fatal("spoofed headers/bearer changed the certificate/account principal", err)
	}
	for _, path := range []string{"/v1/me", "/healthz"} {
		noCertificate := h.client(t, h.apiSocket, tls.Certificate{})
		req, err := http.NewRequest("GET", noCertificate.BaseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = request.Header.Clone()
		if reply, err := noCertificate.HTTP.Do(req); err == nil {
			reply.Body.Close()
			t.Fatal("bearer/header-only request passed the mTLS boundary", path)
		}
	}
	info := domain.CertificateIdentity{InstanceID: h.identity.InstanceID, AgentID: h.identity.AgentID,
		Generation: h.identity.Generation, OwnerGeneration: owner.certificate.Generation, Purpose: "owner"}
	for _, test := range []struct {
		name   string
		change func(*domain.CertificateIdentity)
	}{
		{"other-instance", func(c *domain.CertificateIdentity) { c.InstanceID = domain.NewID("instance") }},
		{"other-agent", func(c *domain.CertificateIdentity) { c.AgentID = legacy.ID }},
		{"retired-device-generation", func(c *domain.CertificateIdentity) { c.Generation++ }},
		{"unadmitted-owner-generation", func(c *domain.CertificateIdentity) { c.OwnerGeneration++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ci := info
			test.change(&ci)
			client := h.client(t, h.apiSocket, h.issue(t, owner.key, ci, time.Now(), time.Minute))
			_, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
			identityCode(t, err, domain.Denied)
		})
	}
	t.Run("certificate-key-not-enrolled", func(t *testing.T) {
		client := h.client(t, h.apiSocket, h.issue(t, newOwnerKey(t), info, time.Now(), time.Minute))
		_, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
		identityCode(t, err, domain.Denied)
	})
	t.Run("device-is-not-owner", func(t *testing.T) {
		client := h.client(t, h.apiSocket, deviceCertificate)
		_, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
		identityCode(t, err, domain.Denied)
	})
	t.Run("registered-other-account", func(t *testing.T) {
		keys := newIdentityKeys(t, "other registered account")
		other, err := h.store.RegisterIdentity(ctx, h.identity.AgentID, domain.RegisterIdentityRequest{Command: command(), Identity: keys.public})
		if err != nil {
			t.Fatal(err)
		}
		other = h.seedActivate(t, other, keys, h.identity.UID+100001)
		ci := domain.CertificateIdentity{InstanceID: other.InstanceID, AgentID: other.AgentID, Generation: other.Generation, Purpose: "device"}
		client := h.client(t, h.joinSocket, h.issue(t, keys.auth, ci, time.Now(), time.Minute))
		_, reply, err := websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: client.HTTP})
		if err == nil || reply == nil || reply.StatusCode != http.StatusForbidden {
			t.Fatal("another registered UID's certificate authenticated this Unix account", err)
		}
	})
	for _, test := range []struct {
		name        string
		certificate tls.Certificate
	}{
		{"expired-certificate", h.issue(t, owner.key, info, time.Now().Add(-2*time.Hour), time.Hour)},
		{"future-certificate", h.issue(t, owner.key, info, time.Now().Add(time.Hour), time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := h.client(t, h.apiSocket, test.certificate)
			if reply, err := client.HTTP.Get(client.BaseURL + "/v1/me"); err == nil {
				reply.Body.Close()
				t.Fatal("TLS accepted a certificate outside its validity window")
			}
		})
	}
	t.Run("untrusted-client-ca", func(t *testing.T) {
		foreign, err := pki.Generate(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		certificate, err := pki.IssueClient(foreign.CA, foreign.CAKey, owner.key.Public(), info, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		client := h.client(t, h.apiSocket, identityTLSCertificate(t, certificate, owner.key))
		if reply, err := client.HTTP.Get(client.BaseURL + "/v1/me"); err == nil {
			reply.Body.Close()
			t.Fatal("TLS accepted a client certificate from an untrusted CA")
		}
	})
	t.Run("certificate-private-key-mismatch", func(t *testing.T) {
		certificate := h.issue(t, owner.key, info, time.Now(), time.Minute)
		certificate.PrivateKey = newOwnerKey(t)
		client := h.client(t, h.apiSocket, certificate)
		if reply, err := client.HTTP.Get(client.BaseURL + "/v1/me"); err == nil {
			reply.Body.Close()
			t.Fatal("TLS accepted a certificate without its matching private key")
		}
	})
	t.Run("tls-without-unix-peer", func(t *testing.T) {
		server := httptest.NewUnstartedServer(h.service.Handler())
		var err error
		server.TLS, err = pki.ServerMutual(filepath.Join(h.root, "trust", "server.pem"), filepath.Join(h.root, "trust", "server-key.pem"), filepath.Join(h.root, "trust", "ca.pem"))
		if err != nil {
			t.Fatal(err)
		}
		server.StartTLS()
		defer server.Close()
		config, err := pki.Client(filepath.Join(h.root, "trust", "ca.pem"), "")
		if err != nil {
			t.Fatal(err)
		}
		config.Certificates = []tls.Certificate{h.issue(t, owner.key, info, time.Now(), time.Minute)}
		transport := &http.Transport{TLSClientConfig: config, Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &api.Client{BaseURL: server.URL, HTTP: &http.Client{Transport: transport}}
		_, err = api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
		identityCode(t, err, domain.Denied)
	})
	for _, path := range []string{"/v1/enroll", "/v1/invitations"} {
		req, err := http.NewRequest("POST", owner.client.BaseURL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		reply, err := owner.client.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, reply.Body)
		reply.Body.Close()
		if reply.StatusCode != http.StatusNotFound {
			t.Fatal("retired bearer enrollment route remains exposed", path, reply.StatusCode)
		}
	}
	identityMe(t, owner.client, h.identity.AgentID)
}

func TestUnixIdentityActivationRequiresFreshProof(t *testing.T) {
	h := newIdentityHarness(t, true)
	ctx := context.Background()
	challenge := func() domain.IdentityChallenge {
		t.Helper()
		value, err := api.Call[domain.IdentityChallenge](ctx, h.join, "GET", "/v1/identity/challenge", nil)
		if err != nil || value.AgentID != h.identity.AgentID || value.InstanceID != h.identity.InstanceID || value.Generation != h.identity.Generation {
			t.Fatal("challenge is not account/instance/generation-bound", value, err)
		}
		return value
	}
	first, second := challenge(), challenge()
	if first.Nonce == second.Nonce {
		t.Fatal("challenge was reused")
	}
	for _, invalid := range []struct {
		name      string
		challenge domain.IdentityChallenge
		wrongKey  bool
	}{
		{"replaced-nonce", first, false},
		{"wrong-key", second, true},
		{"wrong-instance", func() domain.IdentityChallenge { c := second; c.InstanceID = domain.NewID("instance"); return c }(), false},
		{"wrong-agent", func() domain.IdentityChallenge { c := second; c.AgentID = domain.NewID("agent"); return c }(), false},
		{"wrong-generation", func() domain.IdentityChallenge { c := second; c.Generation++; return c }(), false},
		{"changed-expiry", func() domain.IdentityChallenge { c := second; c.ExpiresAt++; return c }(), false},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			key := h.keys.auth
			if invalid.wrongKey {
				key = newOwnerKey(t)
			}
			proof, err := device.SignChallenge(key, invalid.challenge)
			if err != nil {
				t.Fatal(err)
			}
			_, err = api.Call[domain.IdentityCertificate](ctx, h.join, "POST", "/v1/identity/activate", domain.ActivateIdentityRequest{Command: command(), Nonce: invalid.challenge.Nonce, Proof: proof})
			identityCode(t, err, domain.Denied)
			identity, err := h.store.Identity(ctx, h.identity.AgentID)
			if err != nil || identity.State != "provisioned" {
				t.Fatal("failed proof activated identity", identity, err)
			}
		})
	}
	proof, err := device.SignChallenge(h.keys.auth, second)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ActivateIdentityRequest{Command: command(), Nonce: second.Nonce, Proof: proof}
	activated, err := api.Call[domain.IdentityCertificate](ctx, h.join, "POST", "/v1/identity/activate", req)
	if err != nil || activated.Identity.State != "active" {
		t.Fatal(activated.Identity, err)
	}
	replay, err := api.Call[domain.IdentityCertificate](ctx, h.join, "POST", "/v1/identity/activate", req)
	if err != nil || replay.Identity != activated.Identity {
		t.Fatal("exact activation retry lost its original identity", err)
	}
	req.Command = command()
	_, err = api.Call[domain.IdentityCertificate](ctx, h.join, "POST", "/v1/identity/activate", req)
	identityCode(t, err, domain.Denied)
	pair := identityTLSCertificate(t, []byte(activated.Certificate), h.keys.auth)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ci, err := pki.ParseIdentity(leaf)
	if err != nil || ci.Purpose != "device" || ci.OwnerGeneration != 0 || ci.AgentID != h.identity.AgentID {
		t.Fatal(ci, err)
	}
	_, err = api.Call[domain.Agent](ctx, h.client(t, h.apiSocket, pair), "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
	_, err = api.Call[domain.Agent](ctx, h.join, "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
}

func TestUnixIdentityRegistrationRequiresFounderSignedKeys(t *testing.T) {
	for _, founder := range []bool{true, false} {
		name := "delegate-denied"
		if founder {
			name = "founder-signed-registration"
		}
		t.Run(name, func(t *testing.T) {
			h := newIdentityHarness(t, founder)
			deviceClient := h.client(t, h.joinSocket, h.activate(t))
			owner := h.openOwner(t, deviceClient, newOwnerKey(t), true)
			ctx := context.Background()
			if !founder {
				project, err := h.store.CreateProject(ctx, h.founder.AgentID, domain.ProjectRequest{Command: command(), Name: "delegated but cannot enroll"})
				if err != nil {
					t.Fatal(err)
				}
				req := domain.GrantRequest{Command: command(), AgentID: h.identity.AgentID}
				req.ExpectedRevision = project.Revision
				if _, err := h.store.Delegate(ctx, h.founder.AgentID, project.ID, req); err != nil {
					t.Fatal(err)
				}
			}
			keys := newIdentityKeys(t, "target")
			req := domain.RegisterIdentityRequest{Command: command(), Identity: keys.public}
			registered, err := api.Call[domain.AgentIdentity](ctx, owner.client, "POST", "/v1/identity/register", req)
			if !founder {
				identityCode(t, err, domain.Denied)
				_, err = h.store.Invite(ctx, h.identity.AgentID, domain.InvitationRequest{Command: command(), TokenHash: domain.Hash(domain.NewToken()), ExpiresAt: time.Now().Unix() + 60})
				identityCode(t, err, domain.Denied)
				return
			}
			if err != nil || registered.State != "pending" || registered.UID != -1 || registered.ApprovedBy != h.identity.AgentID || registered.SSHKey != keys.public.SSHKey || registered.AuthKey != keys.public.AuthKey {
				t.Fatal(registered, err)
			}
			replay, err := api.Call[domain.AgentIdentity](ctx, owner.client, "POST", "/v1/identity/register", req)
			if err != nil || replay != registered {
				t.Fatal("registration retry changed identity", err)
			}
			for _, mutate := range []func(*domain.PublicIdentity){
				func(p *domain.PublicIdentity) { p.Name += " changed" },
				func(p *domain.PublicIdentity) { p.AuthProof = "invalid" },
				func(p *domain.PublicIdentity) { p.SSHProof = "invalid" },
				func(p *domain.PublicIdentity) { p.SSHKey = keys.public.SSHKey },
			} {
				bad := newIdentityKeys(t, "invalid target").public
				mutate(&bad)
				_, err := api.Call[domain.AgentIdentity](ctx, owner.client, "POST", "/v1/identity/register", domain.RegisterIdentityRequest{Command: command(), Identity: bad})
				identityCode(t, err, domain.Invalid)
			}
			_, err = api.Call[domain.AgentIdentity](ctx, owner.client, "POST", "/v1/identity/register", map[string]any{"operation_id": command().OperationID, "identity": keys.public, "uid": h.identity.UID})
			identityCode(t, err, domain.Invalid)
		})
	}
}

func TestUnixOwnerLeaseRejectsUnprovenDuplicates(t *testing.T) {
	h := newIdentityHarness(t, false)
	deviceClient := h.client(t, h.joinSocket, h.activate(t))
	original := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var previous domain.OwnerRequest
	for _, kind := range []string{"wrong-proof", "replayed-nonce", "unverified-device"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "unverified-device" {
				_, response, err := websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: h.join.HTTP})
				if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
					t.Fatal("unverified device reached owner admission", err)
				}
			} else {
				conn, challenge := h.ownerChallenge(t, deviceClient)
				req, err := device.SignOwner(newOwnerKey(t), h.identity, challenge.Nonce)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "wrong-proof" {
					previous = req
					req.Proof = "invalid"
				} else {
					req = previous
				}
				if err := wsjson.Write(ctx, conn, req); err != nil {
					t.Fatal(err)
				}
				var rejected domain.OwnerCertificate
				if err := wsjson.Read(ctx, conn, &rejected); err == nil || ctx.Err() != nil {
					t.Fatal("unproven contender acquired an owner certificate", err)
				}
			}
			current, err := h.store.Identity(ctx, h.identity.AgentID)
			if err != nil || current.State != "active" || current.OwnerGeneration != original.certificate.Generation {
				t.Fatal("an unproven duplicate changed/banned the original", current, err)
			}
			identityMe(t, original.client, h.identity.AgentID)
		})
	}
}

func TestUnixOwnerLeaseRequiresFreshRenewal(t *testing.T) {
	h := newIdentityHarness(t, true)
	deviceClient := h.client(t, h.joinSocket, h.activate(t))
	owner := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := device.SignOwner(owner.key, h.identity, owner.certificate.Challenge.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, owner.conn, req); err != nil {
		t.Fatal(err)
	}
	select {
	case renewed := <-owner.updates:
		if renewed.Generation != owner.certificate.Generation || renewed.Challenge.Nonce == owner.certificate.Challenge.Nonce {
			t.Fatal("renewal did not rotate the challenge while preserving the owner lease")
		}
	case err := <-owner.errors:
		t.Fatal("valid renewal failed", err)
	case <-ctx.Done():
		t.Fatal("valid renewal timed out")
	}
	identityMe(t, owner.client, h.identity.AgentID)
	if err := wsjson.Write(ctx, owner.conn, req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.errors:
	case <-ctx.Done():
		t.Fatal("replayed renewal nonce kept the owner channel alive")
	}
	_, err = api.Call[domain.Agent](context.Background(), owner.client, "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
	identity, err := h.store.Identity(context.Background(), h.identity.AgentID)
	if err != nil || identity.State != "active" {
		t.Fatal("failed renewal falsely banned identity", err)
	}
}

func TestUnixOwnerLeaseEndsWhenDeviceCertificateExpires(t *testing.T) {
	h := newIdentityHarness(t, false)
	h.activate(t)
	ci := domain.CertificateIdentity{InstanceID: h.identity.InstanceID, AgentID: h.identity.AgentID,
		Generation: h.identity.Generation, Purpose: "device"}
	short := h.issue(t, h.keys.auth, ci, time.Now(), 4*time.Second)
	leaf, err := x509.ParseCertificate(short.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	deviceClient := h.client(t, h.joinSocket, short)
	owner := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	identityMe(t, owner.client, h.identity.AgentID)
	ctx, cancel := context.WithDeadline(context.Background(), leaf.NotAfter.Add(2*time.Second))
	defer cancel()
	select {
	case <-owner.errors:
	case <-ctx.Done():
		t.Fatal("device certificate expiry did not retire its live owner lease")
	}
	_, err = api.Call[domain.Agent](context.Background(), owner.client, "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
	current, err := h.store.Identity(context.Background(), h.identity.AgentID)
	if err != nil || current.State != "active" {
		t.Fatal("certificate expiry falsely banned the identity", err)
	}
}

func TestUnixOwnerReconnectAndCrashDoNotBan(t *testing.T) {
	for _, kind := range []string{"same-key-retry", "closed-owner", "unresponsive-owner", "server-restart"} {
		t.Run(kind, func(t *testing.T) {
			h := newIdentityHarness(t, false)
			deviceCertificate := h.activate(t)
			deviceClient := h.client(t, h.joinSocket, deviceCertificate)
			original := h.openOwner(t, deviceClient, newOwnerKey(t), kind != "unresponsive-owner")
			key := newOwnerKey(t)
			if kind == "same-key-retry" {
				key = original.key
			}
			if kind == "closed-owner" {
				original.close()
				identityEventually(t, "closed owner lease retirement", func() bool {
					_, err := api.Call[domain.Agent](context.Background(), original.client, "GET", "/v1/me", nil)
					return err != nil && domain.AsFault(err).Code == domain.Denied
				})
			}
			if kind == "server-restart" {
				h.stop(t)
				if err := h.store.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				h.store, err = foundation.Open(context.Background(), filepath.Join(h.root, "state", "foundation.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				h.start(t)
				_, err = api.Call[domain.Agent](context.Background(), original.client, "GET", "/v1/me", nil)
				identityCode(t, err, domain.Denied)
			}
			replacement := h.openOwner(t, deviceClient, key, true)
			identityMe(t, replacement.client, h.identity.AgentID)
			current, err := h.store.Identity(context.Background(), h.identity.AgentID)
			if err != nil || current.State != "active" || current.Generation != h.identity.Generation {
				t.Fatal("retry/dead owner triggered duplicate ban", current, err)
			}
			if kind == "same-key-retry" {
				if replacement.certificate.Generation != original.certificate.Generation {
					t.Fatal("same-owner retry advanced the ownership generation")
				}
			} else {
				if replacement.certificate.Generation <= original.certificate.Generation {
					t.Fatal("fresh owner key did not fence the previous owner certificate")
				}
				_, err := api.Call[domain.Agent](context.Background(), original.client, "GET", "/v1/me", nil)
				identityCode(t, err, domain.Denied)
			}
		})
	}
}

func TestUnixOwnerEquivalentPublicKeyEncodingDoesNotBan(t *testing.T) {
	h := newIdentityHarness(t, true)
	deviceClient := h.client(t, h.joinSocket, h.activate(t))
	original := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	conn, challenge := h.ownerChallenge(t, deviceClient)
	req, err := device.SignOwner(original.key, h.identity, challenge.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	req.OwnerKey = "\n" + strings.ReplaceAll(req.OwnerKey, "\n", "\r\n") + "\n"
	if _, err := device.VerifyOwner(req, h.identity); err != nil {
		t.Fatal("equivalent public key encoding changed the cryptographic proof", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, req); err != nil {
		t.Fatal(err)
	}
	var replacement domain.OwnerCertificate
	if err := wsjson.Read(ctx, conn, &replacement); err != nil || replacement.Generation != original.certificate.Generation {
		t.Fatal("same key with equivalent PEM encoding was treated as an independent duplicate", err)
	}
	identityMe(t, original.client, h.identity.AgentID)
	current, err := h.store.Identity(ctx, h.identity.AgentID)
	if err != nil || current.State != "active" || strings.Contains(current.OwnerKey, "\r") {
		t.Fatal("owner key was not stored canonically without a ban", current, err)
	}
}

func TestUnixConfirmedDuplicateBansOriginalAndFounder(t *testing.T) {
	requireBanClose := func(t *testing.T, controller string, err error) {
		t.Helper()
		var closeError websocket.CloseError
		if !errors.As(err, &closeError) || closeError.Code != websocket.StatusPolicyViolation || closeError.Reason != "duplicate identity banned; explicit recovery required" {
			t.Fatalf("%s controller did not receive the explicit ban policy close: %v", controller, err)
		}
	}
	for _, founder := range []bool{false, true} {
		name := "worker"
		if founder {
			name = "Founder"
		}
		t.Run(name, func(t *testing.T) {
			h := newIdentityHarness(t, founder)
			deviceCertificate := h.activate(t)
			deviceClient := h.client(t, h.joinSocket, deviceCertificate)
			original := h.openOwner(t, deviceClient, newOwnerKey(t), true)
			identityMe(t, original.client, h.identity.AgentID)
			before := h.identity
			conn, challenge := h.ownerChallenge(t, deviceClient)
			contenderKey := newOwnerKey(t)
			req, err := device.SignOwner(contenderKey, h.identity, challenge.Nonce)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := device.VerifyOwner(req, h.identity); err != nil {
				t.Fatal("contender was not a valid independent owner proof", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := wsjson.Write(ctx, conn, req); err != nil {
				t.Fatal(err)
			}
			var refused domain.OwnerCertificate
			requireBanClose(t, "contender", wsjson.Read(ctx, conn, &refused))
			identityEventually(t, "durable duplicate ban", func() bool {
				current, err := h.store.Identity(context.Background(), before.AgentID)
				return err == nil && current.State == "banned"
			})
			banned, err := h.store.Identity(ctx, before.AgentID)
			if err != nil || banned.Generation <= before.Generation || banned.OwnerGeneration <= before.OwnerGeneration || banned.OwnerKey != "" || banned.BanReason == "" {
				t.Fatal("ban did not revoke original device and owner generations", banned, err)
			}
			select {
			case err := <-original.errors:
				requireBanClose(t, "original", err)
			case <-ctx.Done():
				t.Fatal("the original live owner survived the duplicate ban")
			}
			_, err = api.Call[domain.Agent](ctx, original.client, "GET", "/v1/me", nil)
			identityCode(t, err, domain.Denied)
			_, err = api.Call[domain.IdentityChallenge](ctx, h.join, "GET", "/v1/identity/challenge", nil)
			identityCode(t, err, domain.Denied)
			_, response, err := websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: deviceClient.HTTP})
			if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
				t.Fatal("banned original device certificate was reusable", err)
			}
			if founder {
				_, err := h.store.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: command(), Reason: "old keys cannot recover Founder", Identity: h.keys.public})
				identityCode(t, err, domain.Conflict)
				fresh := newIdentityKeys(t, "recovered Founder")
				recovered, err := h.store.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: command(), Reason: "trusted offline fresh-key recovery fixture, not a root authorization test", Identity: fresh.public})
				if err != nil || recovered.AgentID != before.AgentID || recovered.State != "pending" || recovered.UID != -1 {
					t.Fatal(recovered, err)
				}
				h.keys = fresh
				h.identity, err = h.store.BindIdentityAccount(ctx, recovered.AgentID, recovered.SSHUser, before.UID, recovered.SSHFingerprint)
				if err != nil {
					t.Fatal(err)
				}
				replacementDevice := h.client(t, h.joinSocket, h.activate(t))
				replacement := h.openOwner(t, replacementDevice, newOwnerKey(t), true)
				identityMe(t, replacement.client, before.AgentID)
				_, err = api.Call[domain.Agent](ctx, original.client, "GET", "/v1/me", nil)
				identityCode(t, err, domain.Denied)
			}
		})
	}
}

func TestUnixRevokedCertificatesRequireFreshKeyRecovery(t *testing.T) {
	h := newIdentityHarness(t, false)
	deviceCertificate := h.activate(t)
	deviceClient := h.client(t, h.joinSocket, deviceCertificate)
	original := h.openOwner(t, deviceClient, newOwnerKey(t), true)
	before, oldKeys := h.identity, h.keys
	ctx := context.Background()
	if _, err := h.store.Revoke(ctx, h.founder.AgentID, before.AgentID, domain.AgentRequest{Command: command()}); err != nil {
		t.Fatal(err)
	}
	_, err := api.Call[domain.Agent](ctx, original.client, "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
	_, err = api.Call[domain.IdentityChallenge](ctx, h.join, "GET", "/v1/identity/challenge", nil)
	identityCode(t, err, domain.Denied)
	_, response, err := websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: deviceClient.HTTP})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("revoked device certificate was reusable", err)
	}
	_, err = h.store.RegisterIdentity(ctx, h.founder.AgentID, domain.RegisterIdentityRequest{Command: command(), ExistingAgentID: before.AgentID, Identity: newIdentityKeys(t, "no migration revival").public})
	identityCode(t, err, domain.Denied)
	_, err = h.store.RecoverIdentity(ctx, h.founder.AgentID, before.AgentID, domain.RecoverIdentityRequest{Command: command(), Reason: "old keys are not recovery", Identity: oldKeys.public})
	identityCode(t, err, domain.Conflict)
	fresh := newIdentityKeys(t, "explicitly recovered worker")
	recovered, err := h.store.RecoverIdentity(ctx, h.founder.AgentID, before.AgentID, domain.RecoverIdentityRequest{Command: command(), Reason: "Founder explicitly authorizes fresh keys", Identity: fresh.public})
	if err != nil || recovered.AgentID != before.AgentID || recovered.Generation <= before.Generation || recovered.State != "pending" || recovered.UID != -1 {
		t.Fatal("recovery did not preserve identity and require reprovisioning", recovered, err)
	}
	_, err = h.store.BindIdentityAccount(ctx, recovered.AgentID, recovered.SSHUser, before.UID+100002, recovered.SSHFingerprint)
	identityCode(t, err, domain.Conflict)
	h.identity, err = h.store.BindIdentityAccount(ctx, recovered.AgentID, recovered.SSHUser, before.UID, recovered.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	h.keys = fresh
	freshDevice := h.client(t, h.joinSocket, h.activate(t))
	replacement := h.openOwner(t, freshDevice, newOwnerKey(t), true)
	identityMe(t, replacement.client, before.AgentID)
	_, err = api.Call[domain.Agent](ctx, original.client, "GET", "/v1/me", nil)
	identityCode(t, err, domain.Denied)
	_, response, err = websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: deviceClient.HTTP})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("pre-recovery device certificate became valid again", err)
	}
	forged := domain.CertificateIdentity{InstanceID: h.identity.InstanceID, AgentID: before.AgentID, Generation: h.identity.Generation, Purpose: "device"}
	oldKeyNewGeneration := h.client(t, h.joinSocket, h.issue(t, oldKeys.auth, forged, time.Now(), time.Minute))
	_, response, err = websocket.Dial(ctx, "wss://localhost/v1/identity/owner", &websocket.DialOptions{HTTPClient: oldKeyNewGeneration.HTTP})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatal("retired key was accepted under a forged fresh-generation certificate", err)
	}
	if err := h.store.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
