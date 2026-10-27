package api

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
)

func TestBinaryTunnelUsesPinnedSSHAndReusableMutualTLSSession(t *testing.T) {
	worker, owner, _, trust := tunnelFixture(t)
	trust.NextProtos = []string{MessageALPN}
	var starts, requests atomic.Int64
	stopped := make(chan struct{}, 2)
	runner := tunnelRunner(func(ctx context.Context, spec process.Spec) error {
		starts.Add(1)
		defer func() { stopped <- struct{}{} }()
		input, inputOK := spec.Stdin.(*os.File)
		output, outputOK := spec.Stdout.(*os.File)
		if !inputOK || !outputOK {
			return errors.New("SSH did not use owned file-backed stdio")
		}
		bridge, remote := net.Pipe()
		copied := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(bridge, input); copied <- struct{}{} }()
		go func() { _, _ = io.Copy(output, bridge); copied <- struct{}{} }()
		defer func() {
			bridge.Close()
			remote.Close()
			input.Close()
			output.Close()
			<-copied
			<-copied
		}()
		if err := spec.Started(123); err != nil {
			return err
		}
		secured := tls.Server(remote, trust)
		if err := secured.HandshakeContext(ctx); err != nil {
			return err
		}
		state := secured.ConnectionState()
		principal, err := pki.ParseIdentity(state.PeerCertificates[0])
		if err != nil || principal.Purpose != "owner" || principal.OwnerGeneration != 3 ||
			state.NegotiatedProtocol != MessageALPN || state.Version != tls.VersionTLS13 {
			return errors.New("binary tunnel changed pinned owner TLS identity")
		}
		session, err := message.NewSession(ctx, secured, func(_ context.Context, request message.Request) ([]byte, error) {
			requests.Add(1)
			if request.Operation != "query.healthz" || request.Target != "" || len(request.Query) != 0 || len(request.Payload) != 0 {
				return nil, domain.Fail(domain.Denied, "unexpected binary operation")
			}
			return message.MarshalPayload(Health{APIVersion: domain.APIVersion, SchemaVersion: domain.SchemaVersion, Version: "fixture"})
		})
		if err != nil {
			return err
		}
		defer session.Close()
		<-session.Done()
		return session.Err()
	})
	client, err := newMessageTunnelClient(worker, owner, "fdn-api", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.HTTP != nil || !client.IsMessageClient() {
		t.Fatal("native tunnel retained HTTP or bearer transport")
	}
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		result, err := Call[Health](ctx, client, "GET", "/healthz", nil)
		cancel()
		if err != nil || result.Version != "fixture" {
			t.Fatal("binary SSH request failed", result, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || requests.Load() != 2 {
		t.Fatal("binary calls did not reuse SSH, or transport ping invoked business work", starts.Load(), requests.Load())
	}
	client.Close()
	awaitTunnelStop(t, stopped)
}
