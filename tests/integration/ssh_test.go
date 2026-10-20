//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestNativeOpenSSHTunnelPinsHostAndCarriesRealTLS(t *testing.T) {
	h := newHarness(t)
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	sshConfig := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if c.User() != "worker" || !bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, fmt.Errorf("fixture key rejected")
		}
		return &ssh.Permissions{}, nil
	}}
	sshConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := url.Parse(h.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	backendPort, err := strconv.Atoi(backend.Port())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := []net.Conn{}
	wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			wg.Go(func() {
				server, channels, requests, err := ssh.NewServerConn(conn, sshConfig)
				if err != nil {
					conn.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "direct-tcpip" {
						incoming.Reject(ssh.UnknownChannelType, "forwarding fixture only")
						continue
					}
					var address struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(incoming.ExtraData(), &address); err != nil || address.Host != "127.0.0.1" || address.Port != uint32(backendPort) {
						incoming.Reject(ssh.Prohibited, "wrong backend")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(requests)
					wg.Go(func() {
						defer channel.Close()
						target, err := net.Dial("tcp", backend.Host)
						if err != nil {
							return
						}
						defer target.Close()
						copyDone := make(chan struct{})
						go func() { io.Copy(channel, target); close(copyDone) }()
						io.Copy(target, channel)
						target.Close()
						channel.Close()
						<-copyDone
					})
				}
			})
		}
	})
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	root := filepath.Join(t.TempDir(), "pins")
	identity, err := ssh.MarshalPrivateKey(clientKey, "fdn local test")
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(root, "identity")
	if err := secure.Create(identityPath, pem.EncodeToMemory(identity)); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(root, "known_hosts")
	if err := secure.Create(pins, []byte(knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, signer.PublicKey())+"\n")); err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localPort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	c := config.SSH{Host: "127.0.0.1", User: "worker", Port: listener.Addr().(*net.TCPAddr).Port, IdentityFile: identityPath, KnownHostsFile: pins, LocalPort: localPort, RemotePort: backendPort}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tunnel, err := transport.Start(ctx, c, process.Native{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := api.NewClient("https://127.0.0.1:"+strconv.Itoa(localPort), filepath.Join(h.root, "ca.pem"), "", h.worker.Credential)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := api.Call[struct {
		ID string `json:"id"`
	}](ctx, client, "GET", "/v1/me", nil)
	if err != nil || agent.ID != h.workerAgent.ID {
		t.Fatal("SSH+TLS identity mismatch", err)
	}
	client.Close()
	if err := tunnel.Close(); err != nil {
		t.Fatal(err)
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if err := secure.Write(pins, []byte(knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, wrongSigner.PublicKey())+"\n")); err != nil {
		t.Fatal(err)
	}
	if tunnel, err := transport.Start(ctx, c, process.Native{}); err == nil {
		tunnel.Close()
		t.Fatal("wrong SSH host pin accepted")
	}
}
