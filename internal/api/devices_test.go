package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
)

func TestFounderDeviceAPIAuthorityReplayAndStrictInput(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	public, _, _, err := device.Generate("Founder Phone")
	if err != nil {
		t.Fatal(err)
	}
	req := domain.RegisterIdentityRequest{Command: domain.Command{OperationID: "register-founder-phone"}, Identity: public}
	spoofed := f.target
	spoofed.Founder = true
	for _, actor := range []domain.Agent{f.target, f.delegate, spoofed} {
		envelope, status := f.request(t, actor, http.MethodPost, "/v1/identity/devices", req)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("additional computers accepted for a worker, delegate or spoofed Founder", status, envelope)
		}
		envelope, status = f.request(t, actor, http.MethodGet, "/v1/identity/devices", nil)
		if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
			t.Fatal("non-Founder could inspect Founder device records", status, envelope)
		}
	}
	var enrolled domain.AgentIdentity
	for range 2 {
		envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/identity/devices", req)
		var current domain.AgentIdentity
		if err := json.Unmarshal(envelope.Data, &current); status != http.StatusOK || !envelope.OK || err != nil ||
			current.AgentID != f.founder.ID || current.DeviceID == "" || current.State != "pending" {
			t.Fatal("Founder device approval failed", status, envelope, err)
		}
		if enrolled.DeviceID != "" && enrolled != current {
			t.Fatal("registration replay created another computer")
		}
		enrolled = current
	}
	for _, invalid := range []struct {
		method, path string
		input        any
	}{
		{http.MethodGet, "/v1/identity/devices?actor=agent_other", nil},
		{http.MethodGet, "/v1/identity/devices", map[string]string{"actor": "agent_other"}},
		{http.MethodPost, "/v1/identity/devices?founder=true", req},
		{http.MethodPost, Path("identity", "devices", enrolled.DeviceID, "revoke") + "?force=true",
			domain.RevokeDeviceRequest{Command: domain.Command{OperationID: "refuse-query-revocation"}, Reason: "No force override"}},
	} {
		envelope, status := f.request(t, f.founder, invalid.method, invalid.path, invalid.input)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("Founder device body/query override accepted", invalid.method, invalid.path, status, envelope)
		}
	}
	for _, input := range []any{
		map[string]any{"operation_id": "forged-device-id", "identity": public, "device_id": domain.NewID("device")},
		map[string]any{"operation_id": "forged-founder", "identity": public, "founder": true},
	} {
		envelope, status := f.request(t, f.founder, http.MethodPost, "/v1/identity/devices", input)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("protected registration fields accepted", status, envelope)
		}
	}
	path := Path("identity", "devices", enrolled.DeviceID, "revoke")
	revoke := domain.RevokeDeviceRequest{Command: domain.Command{OperationID: "revoke-founder-phone"}, Reason: "Withdraw this pending computer"}
	envelope, status := f.request(t, f.delegate, http.MethodPost, path, revoke)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("delegate could revoke Founder device", status, envelope)
	}
	for range 2 {
		envelope, status = f.request(t, f.founder, http.MethodPost, path, revoke)
		var revoked domain.AgentIdentity
		if err := json.Unmarshal(envelope.Data, &revoked); status != http.StatusOK || !envelope.OK || err != nil ||
			revoked.DeviceID != enrolled.DeviceID || revoked.State != "revoked" || revoked.Generation != enrolled.Generation+1 {
			t.Fatal("scoped revocation or replay failed", status, envelope, err)
		}
	}
	envelope, status = f.request(t, f.founder, http.MethodPost, "/v1/identity/devices", req)
	if status != http.StatusForbidden || envelope.Error == nil || envelope.Error.Code != domain.Denied {
		t.Fatal("registration replay resurrected a revoked computer", status, envelope)
	}
}

type founderBinaryOwner struct {
	client      *Client
	session     *message.Session
	identity    domain.AgentIdentity
	certificate domain.OwnerCertificate
	private     []byte
}

func binaryFounderDevice(t *testing.T, f *authenticationAPIFixture, name string, uid int64) (domain.AgentIdentity, []byte) {
	t.Helper()
	ctx := context.Background()
	public, _, private, err := device.Generate(name)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := f.store.RegisterFounderDevice(ctx, f.founder.ID, domain.RegisterIdentityRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, Identity: public})
	if err != nil {
		t.Fatal(err)
	}
	identity, err = f.store.BindIdentityAccount(ctx, identity.BindingID(), identity.SSHUser, uid, identity.SSHFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := f.store.ChallengeIdentity(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := device.ParseSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(signer, challenge)
	if err != nil {
		t.Fatal(err)
	}
	identity, err = f.store.ActivateIdentity(ctx, uid, domain.ActivateIdentityRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, Nonce: challenge.Nonce, Proof: proof})
	if err != nil {
		t.Fatal(err)
	}
	return identity, private
}

func openFounderBinaryOwner(t *testing.T, f *authenticationAPIFixture, trust string, identity domain.AgentIdentity, private []byte) *founderBinaryOwner {
	t.Helper()
	auth, err := device.ParseSigner(private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := f.server.identity.issue(identity, auth.Public(), "device")
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(certPEM, private)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := pki.ServerMutual(filepath.Join(trust, "server.pem"), filepath.Join(trust, "server-key.pem"), filepath.Join(trust, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	serverTLS.NextProtos = []string{MessageALPN}
	clientTLS, err := pki.Client(filepath.Join(trust, "ca.pem"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	clientTLS.NextProtos, clientTLS.Certificates = []string{MessageALPN}, []tls.Certificate{certificate}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	left, right := net.Pipe()
	serverConn := tls.Server(left, serverTLS)
	done := make(chan error, 1)
	go func() {
		if err := serverConn.HandshakeContext(ctx); err != nil {
			done <- err
			return
		}
		done <- f.server.serveJoinMessages(ctx, serverConn, uint32(identity.UID), serverConn.ConnectionState())
	}()
	client := NewMessageClient("https://localhost", func(call context.Context) (net.Conn, error) {
		connection := tls.Client(right, clientTLS)
		if err := connection.HandshakeContext(call); err != nil {
			return nil, err
		}
		return connection, nil
	})
	t.Cleanup(func() {
		client.Close()
		cancel()
		left.Close()
		right.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("device owner handler survived fixture shutdown")
		}
	})
	challengeData, err := client.Message(ctx, message.Request{Operation: OwnerChallengeOperation})
	if err != nil {
		t.Fatal(err)
	}
	var challenge domain.IdentityChallenge
	if err := message.UnmarshalPayload(challengeData, &challenge); err != nil || challenge.DeviceID != identity.DeviceID {
		t.Fatal("binary owner challenge selected another device", err)
	}
	_, _, ownerPrivate, err := device.Generate("ephemeral Founder connection")
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := device.ParseSigner(ownerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	request, err := device.SignOwner(ownerKey, identity, challenge.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := message.MarshalPayload(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.Message(ctx, message.Request{Operation: OwnerProveOperation, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var issued domain.OwnerCertificate
	if err := message.UnmarshalPayload(data, &issued); err != nil {
		t.Fatal(err)
	}
	session, err := client.MessageSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &founderBinaryOwner{client: client, session: session, identity: identity, certificate: issued, private: ownerPrivate}
}

func TestFounderDeviceBinaryOwnersRenewAndRevokeIndependently(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	trust := filepath.Join(t.TempDir(), "trust")
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(trust); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ConfigureIdentity(IdentityOptions{CAFile: filepath.Join(trust, "ca.pem"), CAKeyFile: filepath.Join(trust, "ca-key.pem")}); err != nil {
		t.Fatal(err)
	}
	mac, macKey := binaryFounderDevice(t, f, "Founder Mac", 4200)
	phone, phoneKey := binaryFounderDevice(t, f, "Founder Phone", 4201)
	owners := []*founderBinaryOwner{
		openFounderBinaryOwner(t, f, trust, mac, macKey),
		openFounderBinaryOwner(t, f, trust, phone, phoneKey),
		openFounderBinaryOwner(t, f, trust, mac, macKey),
		openFounderBinaryOwner(t, f, trust, phone, phoneKey),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, owner := range owners {
		if err := owner.session.Ping(ctx); err != nil {
			t.Fatal("another device displaced a live Founder connection", err)
		}
		signer, err := device.ParseSigner(owner.private)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := tls.X509KeyPair([]byte(owner.certificate.Certificate), owner.private)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(leaf.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		ci, err := pki.ParseIdentity(parsed)
		if err != nil || ci.DeviceID != owner.identity.DeviceID || ci.OwnerGeneration != owner.certificate.Generation {
			t.Fatal("owner certificate lost its exact device/connection", ci, err)
		}
		if _, err := f.store.ValidateCertificate(ctx, owner.identity.UID, ci, signer.Public()); err != nil {
			t.Fatal("a simultaneous Founder owner certificate is not valid", err)
		}
		if _, err := f.store.Me(foundation.WithIdentityFence(ctx, ci), f.founder.ID); err != nil {
			t.Fatal("transaction fence rejected a simultaneous Founder owner", err)
		}
		request, err := device.SignOwner(signer, owner.identity, owner.certificate.Challenge.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := message.MarshalPayload(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := owner.client.Message(ctx, message.Request{Operation: OwnerProveOperation, Payload: payload})
		if err != nil {
			t.Fatal("another Founder connection broke certificate renewal", err)
		}
		var renewed domain.OwnerCertificate
		if err := message.UnmarshalPayload(data, &renewed); err != nil || renewed.Generation != owner.certificate.Generation {
			t.Fatal("renewal advanced another connection's owner generation", renewed.Generation, err)
		}
	}
	if err := f.server.identity.denyRetiredDevices(ctx, f.founder.ID, "Delayed recovery callback must not close current device generations"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range owners {
		if err := owner.session.Ping(ctx); err != nil {
			t.Fatal("delayed retirement closed a current Founder device", err)
		}
	}
	envelope, status := f.request(t, f.founder, http.MethodPost, Path("identity", "devices", phone.DeviceID, "revoke"),
		domain.RevokeDeviceRequest{Command: domain.Command{OperationID: "binary-device-revoke"}, Reason: "Revoke just the phone"})
	if status != http.StatusOK || !envelope.OK {
		t.Fatal("binary device revocation failed", status, envelope)
	}
	for _, index := range []int{1, 3} {
		select {
		case <-owners[index].session.Done():
		case <-ctx.Done():
			t.Fatal("revocation left a phone owner connected")
		}
	}
	for _, index := range []int{0, 2} {
		if err := owners[index].session.Ping(ctx); err != nil {
			t.Fatal("phone revocation closed the Mac", err)
		}
	}
	fresh, _, _, err := device.Generate("Recovered Founder")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := f.store.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{
		Command: domain.Command{OperationID: "recover-all-Founder-devices"}, Reason: "Explicit whole-Founder recovery fixture", Identity: fresh})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.provisionIdentity(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 2} {
		select {
		case <-owners[index].session.Done():
		case <-ctx.Done():
			t.Fatal("whole-Founder recovery left an old Mac owner connected")
		}
	}
	t.Log("real TLS/protobuf and signed per-device owner proofs with distinct logical UIDs; native kernel multi-UID/SSH provisioning is not exercised")
}
