package identityservice

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
)

func TestNativeOwnerReadLoopServicesFreshPingsAndPolicyViolation(t *testing.T) {
	f := newFixture(t)
	pingResult := make(chan error, 1)
	reject := make(chan struct{})
	handlerDone := make(chan struct{})
	serverCtx, cancelServer := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			pingResult <- err
			return
		}
		ctx, cancel := context.WithCancel(serverCtx)
		requests := make(chan domain.OwnerRequest, 1)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				var request domain.OwnerRequest
				if wsjson.Read(ctx, connection, &request) != nil {
					return
				}
				select {
				case requests <- request:
				case <-ctx.Done():
					return
				}
			}
		}()
		defer func() {
			cancel()
			connection.CloseNow()
			<-readDone
		}()
		identity := f.profile.Identity
		challenge := domain.IdentityChallenge{Nonce: nonce(10001), AgentID: identity.AgentID, InstanceID: identity.InstanceID,
			Generation: identity.Generation, ExpiresAt: f.now.Add(10 * time.Second).Unix()}
		if err := wsjson.Write(ctx, connection, challenge); err != nil {
			pingResult <- err
			return
		}
		var request domain.OwnerRequest
		select {
		case request = <-requests:
		case <-ctx.Done():
			pingResult <- ctx.Err()
			return
		}
		if request.Nonce != challenge.Nonce {
			pingResult <- errors.New("owner did not use the fresh server nonce")
			return
		}
		public, err := device.VerifyOwner(request, identity)
		if err != nil {
			pingResult <- err
			return
		}
		certificate, err := pki.IssueClient(f.remote.ca.CA, f.remote.ca.CAKey, public, domain.CertificateIdentity{
			AgentID: identity.AgentID, InstanceID: identity.InstanceID, Generation: identity.Generation, Purpose: "owner", OwnerGeneration: 1}, f.now, 15*time.Minute)
		if err != nil {
			pingResult <- err
			return
		}
		challenge.Nonce, challenge.ExpiresAt = nonce(10002), f.now.Add(15*time.Minute).Unix()
		if err := wsjson.Write(ctx, connection, domain.OwnerCertificate{Certificate: string(certificate), Generation: 1, Challenge: challenge}); err != nil {
			pingResult <- err
			return
		}
		pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
		err = connection.Ping(pingCtx)
		pingCancel()
		pingResult <- err
		select {
		case <-reject:
			connection.Close(websocket.StatusPolicyViolation, "identity banned")
		case <-ctx.Done():
		}
	}))
	defer func() { cancelServer(); server.Close() }()
	f.remote.owner = func(ctx context.Context) (OwnerStream, error) {
		connection, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPClient: server.Client(), CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			return nil, err
		}
		return &nativeOwner{connection: connection, client: &api.Client{HTTP: server.Client()}}, nil
	}
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	select {
	case err := <-pingResult:
		if err != nil {
			t.Fatal("idle owner reader did not answer a fresh server ping", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner websocket reader abandoned ping servicing")
	}
	close(reject)
	eventually(t, "policy violation did not become fatal denial", func() bool { return service.Status().Problem == domain.Denied })
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Request(context.Background(), http.MethodGet, "/v1/me", nil, nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("confirmed websocket ban degraded into an unavailable/fallback transport", err)
	}
	stop()
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("native websocket handler/read loop survived service cancellation")
	}
}

func TestNativeIdentitySocketPermissionsAndOwnedCleanup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("native identity service intentionally forbids root")
	}
	f := newFixture(t)
	f.config.WorkerUID = os.Geteuid() + 1
	listener, err := (NativeBoundary{}).Listen(f.config)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for path, mode := range map[string]os.FileMode{filepath.Dir(f.config.Socket): 0710, f.config.Socket: 0660} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("native IPC permissions expose the identity boundary", path, err)
		}
	}
	unrelated := filepath.Join(filepath.Dir(f.config.Socket), "unrelated")
	if err := os.WriteFile(unrelated, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f.config.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned socket was abandoned", err)
	}
	if _, err := os.Lstat(unrelated); err != nil {
		t.Fatal("native shutdown removed unrelated state", err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	missing := os.Getegid() + 1
	for {
		found := missing == os.Getegid()
		for _, group := range groups {
			found = found || missing == group
		}
		if !found {
			break
		}
		missing++
	}
	f.config.WorkerGID = missing
	if _, err := (NativeBoundary{}).Listen(f.config); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("native serve attempted chgrp without preprovisioned group membership", err)
	}
	if _, err := os.Lstat(f.config.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed membership preflight published an IPC socket", err)
	}
}
