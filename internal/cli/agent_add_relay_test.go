package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/testidentity"
	"github.com/thtpark/fdn/internal/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestAgentAdditionRelayProcess(t *testing.T) {
	if os.Getenv("FDN_TEST_AGENT_ADDITION_RELAY") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(RunAgent(context.Background(), os.Args[i+1:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	t.Fatal("raw SSH proxy fixture has no CLI arguments")
}

func TestAgentAdditionViaFoundationUsesNativeSSHEndToEndAndRejectsChangedHostKey(t *testing.T) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(".", ".add-relay-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := foundation.OpenWithFiles(ctx, filepath.Join(root, "server", "foundation.sqlite"),
		foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(func(context.Context, string, []uint32) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	founder, err := testidentity.Register(ctx, store, "", "RelayFounder", 4100)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.NewAuthenticated(store, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(*http.Request) (domain.Agent, error) { return founder, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	worker := config.DefaultWorker(filepath.Join(root, "state"))
	worker.IdentitySocket = filepath.Join(root, "i.sock")
	configuration := filepath.Join(root, "config %h' literal.json")
	if err := config.Write(configuration, worker); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(worker.CredentialFile, config.Credential{AgentID: founder.ID}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", worker.IdentitySocket)
	if err != nil {
		t.Fatal(err)
	}
	proxyCtx, stopProxy := context.WithCancel(ctx)
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- transport.ServeConnections(proxyCtx, listener, func(ctx context.Context, conn net.Conn) error {
			return server.ServeTrustedMessages(ctx, conn, founder.ID)
		}, func(err error) {
			t.Log("synthetic onboarding message connection ended:", domain.AsFault(err).Code)
		})
	}()
	defer func() {
		stopProxy()
		listener.Close()
		if err := <-proxyDone; err != nil {
			t.Error(err)
		}
	}()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, operatorPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	operatorSigner, err := ssh.NewSignerFromKey(operatorPrivate)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM, err := ssh.MarshalPrivateKey(operatorPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "operator-key")
	if err := secure.Create(key, pem.EncodeToMemory(privatePEM)); err != nil {
		t.Fatal(err)
	}
	var authentications atomic.Int64
	sshServer := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		if metadata.User() != "worker" || !bytes.Equal(public.Marshal(), operatorSigner.PublicKey().Marshal()) {
			return nil, errors.New("fixture operator identity mismatch")
		}
		authentications.Add(1)
		return nil, nil
	}}
	sshServer.AddHostKey(hostSigner)
	endpoint, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := endpoint.Accept()
			if err != nil {
				return
			}
			connections.Go(func() {
				defer conn.Close()
				secured, channels, requests, err := ssh.NewServerConn(conn, sshServer)
				if err != nil {
					return
				}
				defer secured.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						var command struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &command) != nil || command.Command != "fixture-relayed-command" {
							_ = request.Reply(false, nil)
							continue
						}
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(channel, "verified end-to-end SSH\n")
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{}))
						channel.Close()
					}
				}
			})
		}
	}()
	defer func() {
		endpoint.Close()
		<-acceptDone
		connections.Wait()
	}()
	target := additionTarget{Host: "127.0.0.1", Port: endpoint.Addr().(*net.TCPAddr).Port,
		User: "worker", Name: "RelayWorker", IdentityFile: key, ViaFoundation: true}
	operation := "add_native_relay"
	state, err := local.Open(ctx, worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Put(ctx, "agent-add:"+operation, agentAddition{
		OperationID: operation, FounderID: founder.ID, Target: target, Stage: "inspected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "proxy %p' executable")
	script := "#!/bin/sh\nexport FDN_TEST_AGENT_ADDITION_RELAY=1\nexec " + additionQuote(executable) +
		" -test.run=^TestAgentAdditionRelayProcess$ -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	proxy := additionProxyCommand(shim, configuration, operation, 15)
	pins := filepath.Join(root, "known_hosts")
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	if err := secure.Create(pins, []byte(knownhosts.Line([]string{address}, hostSigner.PublicKey())+"\n")); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, string, error) {
		args := append([]string{"-o", "BatchMode=yes", "-o", "ProxyCommand=" + proxy, "-o", "ProxyUseFdpass=no"},
			additionSSH(target, pins, false)...)
		args = append(args, "fixture-relayed-command")
		command := exec.CommandContext(ctx, sshPath, args...)
		var diagnostic bytes.Buffer
		command.Stderr = &diagnostic
		output, err := command.Output()
		return output, diagnostic.String(), err
	}
	output, diagnostic, err := run()
	if err != nil || string(output) != "verified end-to-end SSH\n" {
		t.Fatal("native SSH did not traverse the exact Foundation relay helper", string(output), diagnostic, err)
	}
	if authentications.Load() == 0 {
		t.Fatal("the target did not authenticate the Founder's local operator key")
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongHost, err := ssh.NewPublicKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := secure.Write(pins, []byte(knownhosts.Line([]string{address}, wrongHost)+"\n")); err != nil {
		t.Fatal(err)
	}
	if output, diagnostic, err := run(); err == nil || len(output) != 0 || !strings.Contains(diagnostic, "HOST IDENTIFICATION HAS CHANGED") {
		t.Fatal("relayed SSH accepted a changed worker host key", string(output), diagnostic, err)
	}
}
