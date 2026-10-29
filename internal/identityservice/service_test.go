package identityservice

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

func testDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp(filepath.Join("..", ".."), ".id-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})
	return absolute
}

type recordingCrypto struct {
	nativeCryptography
	generated atomic.Int64
	owners    atomic.Int64
	signs     atomic.Int64
}

func (c *recordingCrypto) Generate(name string) (domain.PublicIdentity, []byte, []byte, error) {
	c.generated.Add(1)
	return c.nativeCryptography.Generate(name)
}
func (c *recordingCrypto) GenerateOwner() (crypto.Signer, error) {
	seed := sha256.Sum256(fmt.Appendf(nil, "test-owner-%d", c.owners.Add(1)))
	return ed25519.NewKeyFromSeed(seed[:]), nil
}
func (c *recordingCrypto) SignChallenge(key crypto.Signer, challenge domain.IdentityChallenge) (string, error) {
	c.signs.Add(1)
	return device.SignChallenge(key, challenge)
}

type fixture struct {
	root    string
	config  config.IdentityService
	profile config.ConnectionProfile
	public  domain.PublicIdentity
	crypto  *recordingCrypto
	remote  *fakeRemote
	options Options
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := testDir(t)
	f := &fixture{root: root, now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), crypto: &recordingCrypto{}}
	f.config = config.IdentityService{StateDir: filepath.Join(root, "private"), Socket: filepath.Join(root, "ipc", "i.sock"), WorkerUID: os.Geteuid(), WorkerGID: os.Getegid()}
	if err := os.Mkdir(filepath.Join(root, "public"), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	f.public, err = Prepare(f.config.StateDir, "Agent1", filepath.Join(root, "public", "identity.json"), Options{Cryptography: f.crypto})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.Generate(f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.profile = config.ConnectionProfile{Identity: domain.AgentIdentity{
		AgentID: "agent_0123456789abcdef0123456789abcdef", InstanceID: "instance_0123456789abcdef0123456789abcdef", RequestID: f.public.RequestID,
		SSHUser: "fdna_0123456789abcdef0123", UID: 1501, SSHKey: f.public.SSHKey, AuthKey: f.public.AuthKey, Generation: 1, State: "provisioned"},
		URL: "https://localhost:8443", CA: string(ca.CA), SSHHost: "foundation.example", SSHPort: 22,
		KnownHosts: "foundation.example " + f.public.SSHKey}
	f.remote = &fakeRemote{t: t, profile: f.profile, ca: ca, now: f.now, streams: make(chan *fakeOwner, 20), requests: make(chan *http.Request, 40)}
	f.options = Options{Remote: f.remote, Cryptography: f.crypto, Now: func() time.Time { return f.now }, RetryInterval: time.Millisecond, RenewInterval: 5 * time.Minute}
	return f
}

func nonce(n int64) string {
	seed := sha256.Sum256(fmt.Appendf(nil, "test-nonce-%d", n))
	return base64.RawURLEncoding.EncodeToString(seed[:])
}

type ownerEvent struct {
	certificate domain.OwnerCertificate
	err         error
}

type fakeRemote struct {
	t           *testing.T
	profile     config.ConnectionProfile
	ca          pki.Bundle
	now         time.Time
	nowFn       func() time.Time
	deviceTTL   time.Duration
	mu          sync.Mutex
	challenge   domain.IdentityChallenge
	activations []domain.ActivateIdentityRequest
	loseAck     bool
	wrongID     bool
	wrongCert   bool
	ownerCalls  atomic.Int64
	sequence    atomic.Int64
	streams     chan *fakeOwner
	requests    chan *http.Request
	ordinary    func(*http.Request) (*http.Response, error)
	owner       func(context.Context) (OwnerStream, error)
}

func (r *fakeRemote) currentTime() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return r.now
}

func (r *fakeRemote) checkWorker(c config.Worker) error {
	if c.SSH == nil || c.SSH.User != r.profile.Identity.SSHUser || c.SSH.Host != r.profile.SSHHost || c.SSH.Port != r.profile.SSHPort ||
		c.CAFile != filepath.Join(c.StateDir, "ca.pem") || c.SSH.IdentityFile != filepath.Join(c.StateDir, sshKeyFile) || c.SSH.KnownHostsFile != filepath.Join(c.StateDir, "known_hosts") {
		return errors.New("service did not derive pinned transport using its own assigned SSH account")
	}
	if _, err := secure.Read(c.SSH.IdentityFile, domain.MaxBody); err != nil {
		return err
	}
	return nil
}

func (r *fakeRemote) Challenge(_ context.Context, c config.Worker) (domain.IdentityChallenge, error) {
	if err := r.checkWorker(c); err != nil {
		return domain.IdentityChallenge{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.challenge = domain.IdentityChallenge{Nonce: nonce(r.sequence.Add(1)), AgentID: r.profile.Identity.AgentID, DeviceID: r.profile.Identity.DeviceID, InstanceID: r.profile.Identity.InstanceID,
		Generation: r.profile.Identity.Generation, ExpiresAt: r.currentTime().Add(time.Minute).Unix()}
	if r.wrongID {
		r.challenge.AgentID = "agent_another"
	}
	return r.challenge, nil
}

func (r *fakeRemote) Activate(_ context.Context, c config.Worker, request domain.ActivateIdentityRequest) (domain.IdentityCertificate, error) {
	if err := r.checkWorker(c); err != nil {
		return domain.IdentityCertificate{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activations = append(r.activations, request)
	public, err := device.PublicKey(r.profile.Identity.AuthKey)
	if err != nil {
		return domain.IdentityCertificate{}, err
	}
	if request.Nonce != r.challenge.Nonce {
		return domain.IdentityCertificate{}, errors.New("activation did not use the server challenge")
	}
	if err := device.VerifyChallenge(public, r.challenge, request.Proof); err != nil {
		return domain.IdentityCertificate{}, err
	}
	identity := domain.CertificateIdentity{AgentID: r.profile.Identity.AgentID, DeviceID: r.profile.Identity.DeviceID, InstanceID: r.profile.Identity.InstanceID, Generation: 1, Purpose: "device"}
	if r.wrongCert {
		identity.AgentID = "agent_another"
	}
	ttl := r.deviceTTL
	if ttl == 0 {
		ttl = time.Hour
	}
	certificate, err := pki.IssueClient(r.ca.CA, r.ca.CAKey, public, identity, r.currentTime(), ttl)
	if err != nil {
		return domain.IdentityCertificate{}, err
	}
	if r.loseAck {
		r.loseAck = false
		return domain.IdentityCertificate{}, domain.Fail(domain.Unavailable, "fixture dropped activation acknowledgement")
	}
	active := r.profile.Identity
	active.State = "active"
	return domain.IdentityCertificate{Agent: domain.Agent{ID: active.AgentID, Name: "Agent1", Founder: active.DeviceID != "", Active: true, Mode: "ai"}, Identity: active, Certificate: string(certificate)}, nil
}

func (r *fakeRemote) Owner(ctx context.Context, c config.Worker, cert tls.Certificate) (OwnerStream, error) {
	if err := r.checkWorker(c); err != nil {
		return nil, err
	}
	identity, err := pki.ParseIdentity(cert.Leaf)
	if err != nil || identity.Purpose != "device" {
		return nil, errors.New("owner websocket did not use the device certificate")
	}
	r.ownerCalls.Add(1)
	if r.owner != nil {
		return r.owner(ctx)
	}
	o := &fakeOwner{remote: r, events: make(chan ownerEvent, 8), done: make(chan struct{}), sent: make(chan domain.OwnerRequest, 20)}
	o.challenge = o.nextChallenge(10 * time.Second)
	select {
	case r.streams <- o:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return o, nil
}

func (r *fakeRemote) Client(c config.Worker, cert tls.Certificate) (*api.Client, error) {
	identity, err := pki.ParseIdentity(cert.Leaf)
	if err != nil || identity.Purpose != "owner" {
		return nil, errors.New("ordinary client did not use the ephemeral owner certificate")
	}
	return &api.Client{BaseURL: c.URL, HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if r.ordinary != nil {
			return r.ordinary(request)
		}
		r.requests <- request.Clone(context.Background())
		data, _ := json.Marshal(domain.Agent{ID: r.profile.Identity.AgentID, Active: true})
		envelope, _ := json.Marshal(api.Envelope{OK: true, Data: data, Warnings: []string{}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}, "X-Fdn-Api-Version": {fmt.Sprint(domain.APIVersion)}, "X-Identity-Proof": {"must-not-leak"}}, Body: io.NopCloser(bytes.NewReader(envelope)), Request: request}, nil
	})}}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeOwner struct {
	remote    *fakeRemote
	challenge domain.IdentityChallenge
	events    chan ownerEvent
	done      chan struct{}
	sent      chan domain.OwnerRequest
	once      sync.Once
	readers   atomic.Int64
}

func (o *fakeOwner) nextChallenge(ttl time.Duration) domain.IdentityChallenge {
	i := o.remote.profile.Identity
	return domain.IdentityChallenge{Nonce: nonce(o.remote.sequence.Add(1)), AgentID: i.AgentID, DeviceID: i.DeviceID, InstanceID: i.InstanceID, Generation: i.Generation, ExpiresAt: o.remote.currentTime().Add(ttl).Unix()}
}
func (o *fakeOwner) Challenge(context.Context) (domain.IdentityChallenge, error) {
	return o.challenge, nil
}
func (o *fakeOwner) Send(ctx context.Context, request domain.OwnerRequest) error {
	if request.Nonce != o.challenge.Nonce {
		return domain.Fail(domain.Denied, "owner request reused an old nonce")
	}
	public, err := device.VerifyOwner(request, o.remote.profile.Identity)
	if err != nil {
		return err
	}
	certificate, err := pki.IssueClient(o.remote.ca.CA, o.remote.ca.CAKey, public, domain.CertificateIdentity{
		AgentID: o.remote.profile.Identity.AgentID, DeviceID: o.remote.profile.Identity.DeviceID, InstanceID: o.remote.profile.Identity.InstanceID, Generation: 1, Purpose: "owner", OwnerGeneration: 1}, o.remote.currentTime(), 15*time.Minute)
	if err != nil {
		return err
	}
	o.challenge = o.nextChallenge(15 * time.Minute)
	select {
	case o.sent <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case o.events <- ownerEvent{certificate: domain.OwnerCertificate{Certificate: string(certificate), Generation: 1, Challenge: o.challenge}}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (o *fakeOwner) Receive(ctx context.Context) (domain.OwnerCertificate, error) {
	o.readers.Add(1)
	defer o.readers.Add(-1)
	select {
	case <-ctx.Done():
		return domain.OwnerCertificate{}, ctx.Err()
	case <-o.done:
		return domain.OwnerCertificate{}, context.Canceled
	case event := <-o.events:
		return event.certificate, event.err
	}
}
func (o *fakeOwner) Close() error {
	o.once.Do(func() { close(o.done) })
	return nil
}

type rootlessBoundary struct {
	peer *Peer
}

func (b rootlessBoundary) Listen(c config.IdentityService) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(c.Socket), 0700); err != nil {
		return nil, err
	}
	return net.Listen("unix", c.Socket)
}
func (b rootlessBoundary) Peer(c net.Conn) (Peer, error) {
	if b.peer != nil {
		return *b.peer, nil
	}
	return kernelPeer(c)
}

func eventually(t *testing.T, description string, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-timer.C:
			t.Fatal(description)
		case <-ticker.C:
		}
	}
}

func startService(t *testing.T, f *fixture, boundary Boundary) (*Service, func()) {
	t.Helper()
	if _, err := Connect(context.Background(), f.config, f.profile, f.options); err != nil {
		t.Fatal(err)
	}
	service, err := New(f.config, f.options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.ServeWithBoundary(ctx, boundary) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("identity service left a background function running")
			}
		})
	}
	t.Cleanup(stop)
	eventually(t, "owner did not become ready", func() bool { return service.Status().Ready })
	return service, stop
}

func TestPrepareExactRetryPublicOnlyPrivateStateAndOfflineLock(t *testing.T) {
	f := newFixture(t)
	output := filepath.Join(f.root, "public", "retry.json")
	public, err := Prepare(f.config.StateDir, "Agent1", output, Options{Cryptography: f.crypto})
	if err != nil || !reflect.DeepEqual(public, f.public) || f.crypto.generated.Load() != 1 {
		t.Fatal("prepare rotated a retained identity", err)
	}
	if _, err := Prepare(f.config.StateDir, "renamed", output, Options{Cryptography: f.crypto}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("a different name replaced the identity", err)
	}
	publicBytes, err := secure.Read(output, domain.MaxBody)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE KEY", "ssh_private", "auth_private", "certificate", "token"} {
		if bytes.Contains(publicBytes, []byte(private)) {
			t.Fatal("public preparation output contained protected material", private)
		}
	}
	for _, path := range []string{f.config.StateDir, filepath.Join(f.config.StateDir, deviceFile), filepath.Join(f.config.StateDir, sshKeyFile)} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 || secure.CheckOwner(info) != nil {
			t.Fatal("identity state was not owner-private", path, err)
		}
	}
	lock, err := stateLock(f.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Prepare(f.config.StateDir, "Agent1", output, f.options); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("prepare bypassed the service's exclusive control lock", err)
	}
	if _, err := Connect(context.Background(), f.config, f.profile, f.options); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("connect bypassed the service's exclusive control lock", err)
	}
}

func TestConnectLostAcknowledgementReplaysExactActivation(t *testing.T) {
	f := newFixture(t)
	f.remote.loseAck = true
	if _, err := Connect(context.Background(), f.config, f.profile, f.options); domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal(err)
	}
	status, err := Connect(context.Background(), f.config, f.profile, f.options)
	if err != nil || !status.Connected || status.Ready || status.Agent.ID != f.profile.Identity.AgentID {
		t.Fatal(status, err)
	}
	if len(f.remote.activations) != 2 || !reflect.DeepEqual(f.remote.activations[0], f.remote.activations[1]) || f.crypto.signs.Load() != 1 {
		t.Fatal("lost acknowledgement changed command/nonce/proof or repeated the challenge signature")
	}
	if _, err := os.Lstat(filepath.Join(f.config.StateDir, activationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("acknowledged activation remained pending", err)
	}
	if _, err := Connect(context.Background(), f.config, f.profile, f.options); err != nil || len(f.remote.activations) != 2 {
		t.Fatal("exact connect retry replaced a valid device certificate", err)
	}
	changed := f.profile
	changed.SSHHost = "another.example"
	if _, err := Connect(context.Background(), f.config, changed, f.options); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("trust was silently repinned", err)
	}
}

func TestConnectRejectsForeignIdentityChallengeAndCertificate(t *testing.T) {
	for _, which := range []string{"profile-key", "unprovisioned", "challenge", "certificate"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			profile := f.profile
			switch which {
			case "profile-key":
				profile.Identity.AuthKey = "another-public-key"
			case "unprovisioned":
				profile.Identity.State, profile.Identity.UID = "pending", -1
			case "challenge":
				f.remote.wrongID = true
			case "certificate":
				f.remote.wrongCert = true
			}
			if _, err := Connect(context.Background(), f.config, profile, f.options); domain.AsFault(err).Code != domain.Denied {
				t.Fatal("untrusted identity material was accepted", err)
			}
			if which != "certificate" && f.crypto.signs.Load() != 0 {
				t.Fatal("service signed a challenge for foreign/unprovisioned identity")
			}
			if _, err := os.Lstat(filepath.Join(f.config.StateDir, certificateFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unverified device certificate was retained", err)
			}
		})
	}
}

func TestOwnerReconnectRenewalSameKeyNewStartNewKeyAndNoPersistentProof(t *testing.T) {
	f := newFixture(t)
	f.options.RenewInterval = 10 * time.Millisecond
	service, stop := startService(t, f, rootlessBoundary{})
	first := <-f.remote.streams
	request := <-first.sent
	var renewal domain.OwnerRequest
	select {
	case renewal = <-first.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not renew over the live read-serviced stream")
	}
	if request.OwnerKey != renewal.OwnerKey || request.Nonce == renewal.Nonce || request.Proof == renewal.Proof {
		t.Fatal("renewal did not use the same ephemeral owner key and a fresh server nonce")
	}
	first.events <- ownerEvent{err: domain.Fail(domain.Unavailable, "fixture disconnect")}
	var second *fakeOwner
	select {
	case second = <-f.remote.streams:
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not reconnect")
	}
	reconnected := <-second.sent
	if request.OwnerKey != reconnected.OwnerKey || request.Nonce == reconnected.Nonce {
		t.Fatal("reconnect replaced the owner key or replayed a captured proof")
	}
	eventually(t, "reconnected owner never became ready", func() bool { return service.Status().Ready })
	stop()
	if first.readers.Load() != 0 || second.readers.Load() != 0 {
		t.Fatal("service left an owner read loop running")
	}
	files, err := os.ReadDir(f.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(f.config.StateDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(request.OwnerKey)) || bytes.Contains(data, []byte(request.Proof)) || bytes.Contains(data, []byte(renewal.Proof)) {
			t.Fatal("owner proof/key escaped memory", file.Name())
		}
	}
	_, stopAgain := startService(t, f, rootlessBoundary{})
	third := <-f.remote.streams
	newRequest := <-third.sent
	if request.OwnerKey == newRequest.OwnerKey || f.crypto.owners.Load() != 2 {
		t.Fatal("service restart reused a persisted owner key")
	}
	stopAgain()
}

func TestWorkerProxyStripsCredentialsDeniesTransportControlAndReturnsPublicStatus(t *testing.T) {
	f := newFixture(t)
	_, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.LocalStatus(context.Background())
	if err != nil || !status.Connected || !status.Ready || status.Agent.ID != f.profile.Identity.AgentID {
		t.Fatal(status, err)
	}
	var profile config.ConnectionProfile
	if err := client.Request(context.Background(), http.MethodGet, "/local/profile", nil, &profile); err != nil || !reflect.DeepEqual(profile, f.profile) {
		t.Fatal("public profile unavailable", err)
	}
	var observed domain.Agent
	if err := client.Request(context.Background(), http.MethodGet, "/v1/me", nil, &observed); err != nil {
		t.Fatal(err)
	}
	if client.HTTP != nil {
		t.Fatal("runner IPC retained an HTTP/header channel")
	}
	forwarded := <-f.remote.requests
	for _, name := range []string{"Authorization", "Proxy-Authorization", "X-Fdn-Agent", "X-Identity", "X-Client-Cert", "Forwarded", "X-Forwarded-User"} {
		if forwarded.Header.Get(name) != "" {
			t.Fatal("proxy forwarded worker-supplied identity metadata", name)
		}
	}
	if forwarded.Header.Get("Accept") != "application/json" || forwarded.Host != "localhost:8443" {
		t.Fatal("proxy did not preserve the ordinary request under its fixed remote origin")
	}
	for _, path := range []string{"/v1/identity/owner", "/v1/identity/challenge", "/v1/identity/activate", "/v1/identity/certificate", "/v1/identities", "/v1/enroll", "/v1/invitations", "/local/prepare", "/local/connect", "/local/memory", "/v1/tasks/../identity/owner", "/v1/%69dentity/owner", "/v1/tasks/%2e%2e/identity/owner", "/v1//identity/owner"} {
		if err := client.Request(context.Background(), http.MethodPost, path, domain.Command{OperationID: "denied"}, nil); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("worker accessed transport/identity service control", path, err)
		}
	}
	for _, path := range []string{"/v1/identity/register", "/v1/identity/agent_123/recover", "/v1/agents/agent_1/revoke"} {
		if err := client.Request(context.Background(), http.MethodPost, path, domain.Command{OperationID: "business"}, nil); err != nil {
			t.Fatal("Founder business registration/recovery/revocation must remain server-authorized", path, err)
		}
		<-f.remote.requests
	}
}

func TestRevocationIsFatalDenialAndCancelsOwnedInFlightStreams(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	closed := make(chan struct{})
	f.remote.ordinary = func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		close(started)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: &observedBody{ReadCloser: reader, writer: writer, closed: closed}, Request: request}, nil
	}
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	owner := <-f.remote.streams
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_ = client.Request(context.Background(), http.MethodGet, "/v1/events", nil, nil)
	}()
	<-started
	owner.events <- ownerEvent{err: domain.Fail(domain.Denied, "confirmed duplicate owner ban")}
	eventually(t, "revocation did not produce a fatal non-ready status", func() bool { return service.Status().Problem == domain.Denied && !service.Status().Ready })
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation left an owned outbound body/stream open")
	}
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation left a proxy request running")
	}
	for range 3 {
		if err := client.Request(context.Background(), http.MethodGet, "/v1/me", nil, nil); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("worker guard would continue approved execution on a generic unavailable response", err)
		}
	}
	if f.remote.ownerCalls.Load() != 1 || service.Status().Connected {
		t.Fatal("a banned identity retried or fell back to the device/API transport")
	}
}

type observedBody struct {
	io.ReadCloser
	writer *io.PipeWriter
	closed chan struct{}
	once   sync.Once
}

func (b *observedBody) Close() error {
	b.once.Do(func() {
		b.ReadCloser.Close()
		b.writer.Close()
		close(b.closed)
	})
	return nil
}

func TestNativeServeRequiresCurrentUserAndPrivatePathsRejectSymlinks(t *testing.T) {
	f := newFixture(t)
	if os.Geteuid() != 0 {
		if err := CheckNativeAccount(f.config); err != nil {
			t.Fatal("native service rejected its own installing user", err)
		}
	}
	foreign := f.config
	foreign.WorkerUID = os.Geteuid() + 1
	if err := CheckNativeAccount(foreign); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native service accepted a legacy foreign runner UID", err)
	}
	service, err := New(foreign, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Serve(context.Background()); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native serve bypassed its current-user boundary", err)
	}
	link := filepath.Join(f.root, "linked-state")
	if err := os.Symlink(f.config.StateDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(link, "Agent1", filepath.Join(f.root, "public", "link.json"), f.options); err == nil {
		t.Fatal("symlinked private identity state was accepted")
	}
	if err := os.Chmod(filepath.Join(f.root, "public"), 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(f.config.StateDir, "Agent1", filepath.Join(f.root, "public", "writable.json"), f.options); err == nil {
		t.Fatal("worker-writable public output ancestor was accepted")
	}
}

func TestKernelPeerCredentialsCannotBeOverriddenByHTTPIdentityHeaders(t *testing.T) {
	f := newFixture(t)
	service, stop := startService(t, f, rootlessBoundary{peer: &Peer{UID: os.Geteuid() + 1, GID: os.Getegid()}})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Request(context.Background(), http.MethodGet, "/local/status", nil, nil); err == nil {
		t.Fatal("wire metadata bypassed a mismatching kernel-peer result")
	}
	if !service.Status().Ready {
		t.Fatal("unauthorized worker connection changed the identity's owner lifecycle")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(f.root, "p.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connected := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err != nil {
			connected <- nil
			return
		}
		connected <- connection
	}()
	connection, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	accepted := <-connected
	if accepted == nil {
		t.Fatal("Unix fixture accept failed")
	}
	defer accepted.Close()
	peer, err := (NativeBoundary{}).Peer(accepted)
	if err != nil || peer.UID != os.Geteuid() || peer.GID != os.Getegid() {
		t.Fatal("native IPC did not use actual OS peer credentials", peer, err)
	}
}

func TestOrdinaryProxyRoutePolicyFailsClosed(t *testing.T) {
	for _, target := range []string{"https://elsewhere/v1/me", "//elsewhere/v1/me", "/v1/tasks/../me", "/v1/tasks/a%2fb", "/v1/tasks/a\\b", "/v1/identity/register/extra", "/v1/tasks/task_1/", "/v1/tasks/task_1\n"} {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err == nil && ordinaryPath(http.MethodGet, request.URL) {
			t.Fatal("noncanonical or unknown proxy route was accepted", strings.ReplaceAll(target, "\n", "\\n"))
		}
	}
	if ordinaryPath(http.MethodDelete, mustRequest(t, "/v1/tasks/task_1").URL) {
		t.Fatal("unknown method was accepted")
	}
}

func mustRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
