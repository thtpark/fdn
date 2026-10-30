package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestStreamPinnedNativeSSHSession(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("native SSH is unavailable")
	}
	testPinnedSSHSession(t, func(ctx context.Context, c config.SSH, mode string) (net.Conn, error) {
		return OpenStream(ctx, c, mode, process.Native{})
	})
}

func TestEmbeddedStreamPinnedSSHWithoutProcesses(t *testing.T) {
	testPinnedSSHSession(t, OpenEmbeddedStream)
}

func testPinnedSSHSession(t *testing.T, open func(context.Context, config.SSH, string) (net.Conn, error)) {
	t.Helper()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSSH, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if metadata.User() != "agent_test" || !bytes.Equal(key.Marshal(), clientSSH.Marshal()) {
			return nil, errors.New("fixture rejected an unregistered key")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections sync.WaitGroup
	accepted := make(chan struct{})
	commands := make(chan string, 4)
	var executions atomic.Int64
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Go(func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if channel.ChannelType() != "session" {
						channel.Reject(ssh.Prohibited, "fixture permits only a fixed command")
						continue
					}
					stream, requests, err := channel.Accept()
					if err != nil {
						return
					}
					defer stream.Close()
					for req := range requests {
						var command struct{ Command string }
						if req.Type != "exec" || ssh.Unmarshal(req.Payload, &command) != nil || (command.Command != "fdn-api" && command.Command != "fdn-join") {
							req.Reply(false, nil)
							continue
						}
						executions.Add(1)
						commands <- command.Command
						req.Reply(true, nil)
						io.Copy(stream, stream)
						stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
				}
			})
		}
	}()
	defer func() {
		listener.Close()
		<-accepted
		connections.Wait()
	}()
	root := filepath.Join(t.TempDir(), "private ssh")
	identity, pins := filepath.Join(root, "identity"), filepath.Join(root, "known_hosts")
	block, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(identity, pem.EncodeToMemory(block)); err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(pins, []byte(knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey())+"\n")); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	c := config.SSH{Host: "127.0.0.1", User: "agent_test", Port: port, IdentityFile: identity, KnownHostsFile: pins}
	for _, mode := range []string{"fdn-api", "fdn-join"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := open(ctx, c, mode)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		payload := []byte("encrypted " + mode + "\n")
		if _, err := conn.Write(payload); err != nil {
			conn.Close()
			cancel()
			t.Fatal(err)
		}
		response := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, response); err != nil || !bytes.Equal(payload, response) {
			conn.Close()
			cancel()
			t.Fatal("native SSH stream failed", err)
		}
		if err := conn.Close(); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		select {
		case command := <-commands:
			if command != mode {
				t.Fatal("native SSH requested the wrong fixed command")
			}
		default:
			t.Fatal("native SSH never executed the fixed command")
		}
	}
	_, wrongPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongHost, err := ssh.NewSignerFromKey(wrongPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := secure.Write(pins, []byte(knownhosts.Line([]string{listener.Addr().String()}, wrongHost.PublicKey())+"\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := open(ctx, c, "fdn-api")
	if err == nil {
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("wrong host pin produced a usable stream")
		}
	}
	if executions.Load() != 2 {
		t.Fatal("wrong host pin reached a remote command")
	}
}
