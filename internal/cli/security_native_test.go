package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/platform"
)

type nativeRecordingClient struct {
	mu      sync.Mutex
	uid     uint32
	gid     uint32
	records map[string]platform.ManagedAccount
	created int
	fail    bool
	corrupt string
}

func (c *nativeRecordingClient) Provision(ctx context.Context, request platform.AccountProvisionRequest) (platform.ManagedAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return platform.ManagedAccount{}, err
	}
	canonical, fingerprint, err := platform.CanonicalAccountKey(request.PublicKey)
	if err != nil {
		return platform.ManagedAccount{}, err
	}
	account, exists := c.records[request.AgentID]
	if !exists {
		account = platform.ManagedAccount{AgentID: request.AgentID, Username: request.Username, UID: c.uid + uint32(c.created), GID: c.gid}
		c.created++
	}
	account.PublicKey, account.Fingerprint, account.State, account.Generation = canonical, fingerprint, platform.AccountActive, request.Generation
	c.records[request.AgentID] = account
	if c.fail {
		return platform.ManagedAccount{}, errors.New("recorded lost provisioning acknowledgement")
	}
	switch c.corrupt {
	case "uid":
		account.UID = 0
	case "username":
		account.Username = "root"
	case "other-username":
		account.Username = "another-agent"
	case "fingerprint":
		account.Fingerprint = "SHA256:invalid"
	case "group":
		account.GID++
	case "agent":
		account.AgentID = "agent_ffffffffffffffffffffffffffffffff"
	case "state":
		account.State = platform.AccountPending
	case "generation":
		account.Generation++
	}
	return account, nil
}
func (c *nativeRecordingClient) Disable(_ context.Context, request platform.AccountDisableRequest) (platform.ManagedAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	account := c.records[request.AgentID]
	account.State, account.Generation = platform.AccountDisabled, request.Generation
	c.records[request.AgentID] = account
	if c.fail {
		return platform.ManagedAccount{}, errors.New("recorded disable failure")
	}
	return account, nil
}
func (*nativeRecordingClient) Close() {}

type nativeReadyOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (w *nativeReadyOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	w.once.Do(func() { close(w.ready) })
	return n, err
}

type nativeFixture struct {
	app    *app
	root   string
	args   []string
	public domain.PublicIdentity
	auth   []byte
	client *nativeRecordingClient
}

func nativeFixtureForTest(t *testing.T) nativeFixture {
	t.Helper()
	root, err := os.MkdirTemp(".", ".n-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	group := os.Getegid()
	if group == 0 {
		group = 1
	}
	if err := os.Chown(root, -1, group); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	public, _, auth, err := device.Generate("Founder")
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "public.json")
	data, _ := json.Marshal(public)
	if err := os.WriteFile(request, data, 0600); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	if uid == 0 {
		uid = 220000
	}
	client := &nativeRecordingClient{uid: uid, gid: uint32(group), records: map[string]platform.ManagedAccount{}}
	a := &app{configFile: filepath.Join(root, "s", "server.json"), operation: "native-init", in: strings.NewReader(""), out: io.Discard, diagnostic: io.Discard}
	return nativeFixture{app: a, root: root, public: public, auth: auth, client: client, args: []string{"--state", filepath.Join(root, "s"), "--founder-request", request, "--ingress-socket", filepath.Join(root, "i", "a"), "--join-socket", filepath.Join(root, "i", "j"), "--ingress-group", strconv.Itoa(group), "--account-socket", filepath.Join(root, "helper")}}
}

func TestNativeFoundationInitPublicIdentityAndExactSagaReplay(t *testing.T) {
	fixture := nativeFixtureForTest(t)
	fixture.client.fail = true
	if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); err == nil {
		t.Fatal("lost native acknowledgement disappeared")
	}
	c, err := config.LoadServer(fixture.app.configFile)
	if err != nil {
		t.Fatal("pending initialization did not persist configuration", err)
	}
	if c.IngressSocket == "" || c.JoinSocket == "" {
		t.Fatal("secure init omitted Unix ingress")
	}
	ca, err := os.ReadFile(filepath.Join(c.StateDir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := foundation.Open(context.Background(), filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	identities, err := store.ManagedIdentities(context.Background())
	_ = store.Close()
	if err != nil || len(identities) != 1 || identities[0].State != "pending" || identities[0].UID != -1 {
		t.Fatal("partial init granted access or lost identity", identities, err)
	}
	id := identities[0].AgentID
	fixture.client.fail = false
	store, err = foundation.Open(context.Background(), filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileNativePending(context.Background(), store, fixture.client, c, map[string]bool{}); err != nil {
		_ = store.Close()
		t.Fatal("pending OS/SQLite saga did not reconcile", err)
	}
	_ = store.Close()
	fixture.app.operation = "new-default-operation"
	value, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client)
	if err != nil {
		t.Fatal(err)
	}
	identity := value.(map[string]any)["identity"].(domain.AgentIdentity)
	if identity.AgentID != id || identity.State != "provisioned" || fixture.client.created != 1 || fixture.app.operation != "native-init" {
		t.Fatal("saga replay reset identity", identity, fixture.client.created)
	}
	if current, err := os.ReadFile(filepath.Join(c.StateDir, "ca.pem")); err != nil || !bytes.Equal(current, ca) {
		t.Fatal("saga replay replaced trust", err)
	}
	if _, err := os.Lstat(filepath.Join(c.StateDir, "init-plan.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed private initialization plan retained", err)
	}
	if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); err != nil {
		t.Fatal("completed init was not idempotent", err)
	}
	other, _, _, err := device.Generate("Different")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(other)
	if err := os.WriteFile(filepath.Join(fixture.root, "public.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("replaced immutable Founder initialization", err)
	}
}

func TestNativeFoundationRejectsLegacyInitAndUnboundProvisionResults(t *testing.T) {
	for _, flags := range [][]string{{"--listen", "127.0.0.1:8443"}, {"--bootstrap-file", "bootstrap.json"}} {
		fixture := nativeFixtureForTest(t)
		if _, err := fixture.app.initNativeFoundation(context.Background(), append(fixture.args, flags...), fixture.client); err == nil {
			t.Fatal("legacy initialization admitted", flags)
		}
		if _, err := os.Lstat(filepath.Join(fixture.root, "s")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid init changed state", err)
		}
	}
	for _, corruption := range []string{"uid", "username", "other-username", "fingerprint", "group", "agent", "state", "generation"} {
		t.Run(corruption, func(t *testing.T) {
			fixture := nativeFixtureForTest(t)
			fixture.client.corrupt = corruption
			if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); domain.AsFault(err).Code != domain.Denied {
				t.Fatal("mismatched native result bound", err)
			}
			store, err := foundation.Open(context.Background(), filepath.Join(fixture.root, "s", "foundation.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			identities, err := store.ManagedIdentities(context.Background())
			if err != nil || len(identities) != 1 || identities[0].UID != -1 || identities[0].State != "pending" {
				t.Fatal("mismatched result granted registry authority", identities, err)
			}
		})
	}
	fixture := nativeFixtureForTest(t)
	if _, err := fixture.app.foundation(context.Background(), []string{"bootstrap", "--output", "unused"}); err == nil {
		t.Fatal("bearer bootstrap leaf retained")
	}
	legacy := struct {
		config.Server
		Listen string `json:"listen"`
	}{Server: config.Server{StateDir: filepath.Join(fixture.root, "old"), CertFile: filepath.Join(fixture.root, "old", "server.pem"), KeyFile: filepath.Join(fixture.root, "old", "server-key.pem")}, Listen: "127.0.0.1:8443"}
	if err := config.Write(fixture.app.configFile, legacy); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(fixture.app.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.serveNativeFoundation(context.Background(), nil, fixture.client); err == nil {
		t.Fatal("production served a TCP/legacy configuration")
	}
	if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); err == nil {
		t.Fatal("native initialization overwrote legacy configuration")
	}
	if current, err := os.ReadFile(fixture.app.configFile); err != nil || !bytes.Equal(current, original) {
		t.Fatal("legacy configuration changed", err)
	}
}

func nativeHTTPClient(socket string, tlsConfig *tls.Config) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func legacyNativeResponse(t *testing.T, client *http.Client, method, path string, input any) (int, api.Envelope) {
	t.Helper()
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, "https://localhost"+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer untrusted")
	request.Header.Set("X-Foundation-UID", "0")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope api.Envelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, envelope
}

func nativeMessageClient(socket string, tlsConfig *tls.Config) *api.Client {
	return api.NewMessageClient("fixture://native", func(ctx context.Context) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil || tlsConfig == nil {
			return conn, err
		}
		trust := tlsConfig.Clone()
		trust.NextProtos = []string{api.MessageALPN}
		secured := tls.Client(conn, trust)
		if err := secured.HandshakeContext(ctx); err != nil {
			_ = secured.Close()
			return nil, err
		}
		return secured, nil
	})
}

func nativeResponse(t *testing.T, client *api.Client, method, path string, input any) (int, api.Envelope) {
	t.Helper()
	var data json.RawMessage
	if err := client.Request(context.Background(), method, path, input, &data); err != nil {
		return domain.HTTPStatus(err), api.Envelope{Error: domain.AsFault(err), Warnings: []string{}}
	}
	return http.StatusOK, api.Envelope{OK: true, Data: data, Warnings: []string{}}
}

func TestNativeFoundationUnixMutualTLSJoinOwnerAndMaintenance(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("rootless peer UID fixture requires a non-root account")
	}
	fixture := nativeFixtureForTest(t)
	if _, err := fixture.app.initNativeFoundation(context.Background(), fixture.args, fixture.client); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadServer(fixture.app.configFile)
	if err != nil {
		t.Fatal(err)
	}
	ready := &nativeReadyOutput{ready: make(chan struct{})}
	fixture.app.out = ready
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := fixture.app.serveNativeFoundation(ctx, nil, fixture.client); done <- err }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(12 * time.Second):
			t.Error("native server workers leaked")
		}
	}()
	select {
	case <-ready.ready:
	case err := <-done:
		t.Fatalf("native listener failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native listener not ready")
	}
	ca, err := os.ReadFile(filepath.Join(c.StateDir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	baseTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, ServerName: "localhost"}
	unauthenticated := nativeMessageClient(c.IngressSocket, baseTLS)
	defer unauthenticated.Close()
	if err := unauthenticated.Request(context.Background(), "GET", "/healthz", nil, nil); err == nil {
		t.Fatal("ordinary API accepted missing client certificate")
	}
	join := nativeMessageClient(c.JoinSocket, baseTLS.Clone())
	defer join.Close()
	status, envelope := nativeResponse(t, join, http.MethodGet, "/v1/me", nil)
	if status != http.StatusForbidden || envelope.OK {
		t.Fatal("join exposed ordinary API", status)
	}
	status, envelope = nativeResponse(t, join, http.MethodGet, "/v1/identity/challenge", nil)
	var challenge domain.IdentityChallenge
	if err := json.Unmarshal(envelope.Data, &challenge); err != nil || status != http.StatusOK {
		t.Fatal("kernel-bound join challenge", status, err, envelope.Error)
	}
	signer, err := device.ParseSigner(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := device.SignChallenge(signer, challenge)
	if err != nil {
		t.Fatal(err)
	}
	status, envelope = nativeResponse(t, join, http.MethodPost, "/v1/identity/activate", domain.ActivateIdentityRequest{Command: domain.Command{OperationID: "native-activate"}, Nonce: challenge.Nonce, Proof: proof})
	var activation domain.IdentityCertificate
	if err := json.Unmarshal(envelope.Data, &activation); err != nil || status != http.StatusOK || activation.Identity.State != "active" {
		t.Fatal("native activation", status, err, envelope.Error)
	}
	certificate, err := tls.X509KeyPair([]byte(activation.Certificate), fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	deviceTLS := baseTLS.Clone()
	deviceTLS.Certificates = []tls.Certificate{certificate}
	deviceAPI := nativeMessageClient(c.IngressSocket, deviceTLS)
	defer deviceAPI.Close()
	status, envelope = nativeResponse(t, deviceAPI, http.MethodGet, "/v1/me", nil)
	if status != http.StatusForbidden || envelope.OK {
		t.Fatal("device certificate bypassed live owner", status)
	}
	ownerClient := nativeMessageClient(c.JoinSocket, deviceTLS.Clone())
	defer ownerClient.Close()
	ownerCtx, stopOwner := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopOwner()
	data, err := ownerClient.Message(ownerCtx, message.Request{Operation: api.OwnerChallengeOperation})
	if err != nil {
		t.Fatal(err)
	}
	if err := message.UnmarshalPayload(data, &challenge); err != nil {
		t.Fatal(err)
	}
	_, ownerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest, err := device.SignOwner(ownerKey, activation.Identity, challenge.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := message.MarshalPayload(ownerRequest)
	if err != nil {
		t.Fatal(err)
	}
	data, err = ownerClient.Message(ownerCtx, message.Request{Operation: api.OwnerProveOperation, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var ownerCertificate domain.OwnerCertificate
	if err := message.UnmarshalPayload(data, &ownerCertificate); err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	ownerPair, err := tls.X509KeyPair([]byte(ownerCertificate.Certificate), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	if err != nil {
		t.Fatal(err)
	}
	ownerTLS := baseTLS.Clone()
	ownerTLS.Certificates = []tls.Certificate{ownerPair}
	ordinary := nativeMessageClient(c.IngressSocket, ownerTLS)
	defer ordinary.Close()
	status, envelope = nativeResponse(t, ordinary, http.MethodGet, "/v1/me", nil)
	var actor domain.Agent
	if err := json.Unmarshal(envelope.Data, &actor); err != nil || status != http.StatusOK || actor.ID != activation.Agent.ID {
		t.Fatal("live account/owner binding", status, err, envelope.Error)
	}
	ownerClient.Close()
	deadline := time.Now().Add(time.Second)
	for {
		status, envelope = nativeResponse(t, ordinary, http.MethodGet, "/v1/me", nil)
		if status == http.StatusForbidden {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected owner retained ordinary access", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	maintenance := nativeMessageClient(filepath.Join(c.StateDir, "maintenance.sock"), nil)
	defer maintenance.Close()
	if err := maintenance.Request(context.Background(), "POST", "/v1/identity/recover-founder", domain.RecoverIdentityRequest{}, nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("non-root reached maintenance", err)
	}
	for path, mode := range map[string]os.FileMode{c.IngressSocket: 0660, c.JoinSocket: 0660, filepath.Join(c.StateDir, "maintenance.sock"): 0600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("native endpoint permissions", path, info, err)
		}
	}
}

func TestNativeRootConfigurationAndPrivilegeGates(t *testing.T) {
	fixture := nativeFixtureForTest(t)
	path := filepath.Join(fixture.root, "gateway.json")
	options := ingress.ProxyOptions{APISocket: filepath.Join(fixture.root, "a"), JoinSocket: filepath.Join(fixture.root, "j"), ServerUID: uint32(os.Geteuid())}
	data, _ := json.Marshal(options)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	var decoded ingress.ProxyOptions
	if err := readOwnedNativeConfig(path, &decoded, uint32(os.Geteuid())); err != nil || decoded != options {
		t.Fatal("protected configuration fixture", err)
	}
	if os.Geteuid() != 0 {
		if err := readRootNativeConfig(path, &decoded); err == nil {
			t.Fatal("gateway accepted non-root configuration")
		}
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if err := readOwnedNativeConfig(path, &decoded, uint32(os.Geteuid())); err == nil {
		t.Fatal("writable gateway configuration accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(fixture.root, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := readOwnedNativeConfig(alias, &decoded, uint32(os.Geteuid())); err == nil {
		t.Fatal("symlink gateway configuration accepted")
	}
	if err := nativeCurrentIngressReady(os.Getegid()); err == nil {
		t.Fatal("DB primary group reused as ingress")
	}
	if os.Geteuid() != 0 {
		if _, err := fixture.app.nativeAccounts(context.Background(), []string{"serve", "--input", path}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("unprivileged root helper", err)
		}
		if _, err := fixture.app.recoverNativeFounder(context.Background(), []string{"--input", filepath.Join(fixture.root, "public.json"), "--reason", "operator recovery"}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("unprivileged maintenance client", err)
		}
	}
}
