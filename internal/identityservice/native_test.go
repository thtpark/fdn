package identityservice

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
)

func TestNativeOwnerReadLoopServicesFreshPingsAndPolicyViolation(t *testing.T) {
	f := newFixture(t)
	pingResult := make(chan error, 1)
	reject := make(chan struct{})
	admitted := make(chan struct{})
	handlerDone := make(chan struct{})
	serverCtx, cancelServer := context.WithCancel(context.Background())
	left, right := net.Pipe()
	go func() {
		defer close(handlerDone)
		identity := f.profile.Identity
		challenge := domain.IdentityChallenge{Nonce: nonce(10001), AgentID: identity.AgentID, InstanceID: identity.InstanceID,
			Generation: identity.Generation, ExpiresAt: f.now.Add(10 * time.Second).Unix()}
		connection, err := message.NewSession(serverCtx, left, func(_ context.Context, wire message.Request) ([]byte, error) {
			if wire.Operation == api.OwnerChallengeOperation {
				return message.MarshalPayload(challenge)
			}
			var request domain.OwnerRequest
			if err := message.UnmarshalPayload(wire.Payload, &request); err != nil {
				return nil, err
			}
			if request.Nonce != challenge.Nonce {
				return nil, errors.New("owner did not use the fresh server nonce")
			}
			public, err := device.VerifyOwner(request, identity)
			if err != nil {
				return nil, err
			}
			certificate, err := pki.IssueClient(f.remote.ca.CA, f.remote.ca.CAKey, public, domain.CertificateIdentity{
				AgentID: identity.AgentID, InstanceID: identity.InstanceID, Generation: identity.Generation,
				Purpose: "owner", OwnerGeneration: 1}, f.now, 15*time.Minute)
			if err != nil {
				return nil, err
			}
			next := challenge
			next.Nonce, next.ExpiresAt = nonce(10002), f.now.Add(15*time.Minute).Unix()
			close(admitted)
			return message.MarshalPayload(domain.OwnerCertificate{Certificate: string(certificate), Generation: 1, Challenge: next})
		})
		if err != nil {
			pingResult <- err
			return
		}
		defer connection.Close()
		select {
		case <-admitted:
		case <-serverCtx.Done():
			return
		}
		pingCtx, pingCancel := context.WithTimeout(serverCtx, 2*time.Second)
		err = connection.Ping(pingCtx)
		pingCancel()
		pingResult <- err
		select {
		case <-reject:
			_ = connection.Shutdown(domain.Fail(domain.Denied, "identity banned"))
		case <-serverCtx.Done():
		}
	}()
	defer func() { cancelServer(); left.Close(); right.Close() }()
	f.remote.owner = func(ctx context.Context) (OwnerStream, error) {
		client := api.NewMessageClient("fixture://owner", func(context.Context) (net.Conn, error) { return right, nil })
		connection, err := client.MessageSession(ctx)
		if err != nil {
			return nil, err
		}
		return &nativeOwner{connection: connection, client: client, responses: make(chan domain.OwnerCertificate, 1)}, nil
	}
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	select {
	case err := <-pingResult:
		if err != nil {
			t.Fatal("idle owner reader did not answer a fresh server ping", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner message reader abandoned ping servicing")
	}
	close(reject)
	eventually(t, "policy violation did not become fatal denial", func() bool { return service.Status().Problem == domain.Denied })
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Request(context.Background(), "GET", "/v1/me", nil, nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("confirmed owner ban degraded into an unavailable/fallback transport", err)
	}
	stop()
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("native message handler/read loop survived service cancellation")
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
