package transport

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
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

func TestEmbeddedStreamPinnedSSHWithMultipleHostKeys(t *testing.T) {
	testPinnedSSHSession(t, OpenEmbeddedStream, testSSHHostSigner(t, ssh.KeyAlgoECDSA256))
}

func TestEmbeddedStreamPinnedSSHHostAlgorithms(t *testing.T) {
	for _, algorithm := range []string{ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSA} {
		t.Run(algorithm, func(t *testing.T) {
			host := testSSHHostSigner(t, algorithm)
			testPinnedSSHSessionWithHost(t, OpenEmbeddedStream, host, nil, testSSHHostSigner(t, ssh.KeyAlgoED25519))
		})
	}
	t.Run("certificate-authority", func(t *testing.T) {
		host := testSSHHostSigner(t, ssh.KeyAlgoED25519)
		authority := testSSHHostSigner(t, ssh.KeyAlgoED25519)
		certificate := &ssh.Certificate{Key: host.PublicKey(), CertType: ssh.HostCert,
			ValidPrincipals: []string{"127.0.0.1"}, ValidBefore: ssh.CertTimeInfinity}
		if err := certificate.SignCert(rand.Reader, authority); err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewCertSigner(certificate, host)
		if err != nil {
			t.Fatal(err)
		}
		testPinnedSSHSessionWithHost(t, OpenEmbeddedStream, signer, authority.PublicKey(), testSSHHostSigner(t, ssh.KeyAlgoECDSA256))
	})
}

func TestEmbeddedHostKeyAlgorithms(t *testing.T) {
	host := testSSHHostSigner(t, ssh.KeyAlgoED25519)
	ecdsaHost := testSSHHostSigner(t, ssh.KeyAlgoECDSA256)
	rsaHost := testSSHHostSigner(t, ssh.KeyAlgoRSA)
	authority := testSSHHostSigner(t, ssh.KeyAlgoED25519)
	address := "127.0.0.1:2222"
	line := func(key ssh.PublicKey) string {
		return knownhosts.Line([]string{address}, key) + "\n"
	}
	edPin, ecdsaPin := line(host.PublicKey()), line(ecdsaHost.PublicKey())
	other := knownhosts.Line([]string{"other.invalid:2222"}, ecdsaHost.PublicKey()) + "\n"
	caPin := "@cert-authority " + line(authority.PublicKey())
	hashed := knownhosts.HashHostname(knownhosts.Normalize(address)) + " " + string(ssh.MarshalAuthorizedKey(host.PublicKey()))
	for _, test := range []struct {
		name         string
		address      string
		pins         string
		want         []string
		certificates bool
		denied       bool
	}{
		{name: "unrelated-key-first", address: address, pins: other + edPin, want: []string{ssh.KeyAlgoED25519}},
		{name: "hashed-host", address: address, pins: hashed, want: []string{ssh.KeyAlgoED25519}},
		{name: "default-port", address: "127.0.0.1:22",
			pins: knownhosts.Line([]string{"127.0.0.1:22"}, host.PublicKey()) + "\n", want: []string{ssh.KeyAlgoED25519}},
		{name: "ecdsa", address: address, pins: ecdsaPin, want: []string{ssh.KeyAlgoECDSA256}},
		{name: "rsa-sha2", address: address, pins: line(rsaHost.PublicKey()),
			want: []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}},
		{name: "rotation-and-duplicates", address: address, pins: edPin + ecdsaPin + edPin,
			want: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256}},
		{name: "authority", address: address, pins: caPin, certificates: true},
		{name: "raw-and-authority", address: address, pins: caPin + edPin,
			want: []string{ssh.KeyAlgoED25519}, certificates: true},
		{name: "unrelated-authority", address: address, pins: "@cert-authority " + other + edPin,
			want: []string{ssh.KeyAlgoED25519}},
		{name: "unknown-host", address: address, pins: other, denied: true},
		{name: "revoked-key", address: address, pins: edPin + "@revoked " + edPin, denied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "known_hosts")
			if err := secure.Create(path, []byte(test.pins)); err != nil {
				t.Fatal(err)
			}
			verify, err := knownhosts.New(path)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := net.ResolveTCPAddr("tcp", test.address)
			if err != nil {
				t.Fatal(err)
			}
			algorithms, err := embeddedHostKeyAlgorithms([]byte(test.pins), test.address, remote, verify)
			if test.denied {
				if err == nil || domain.AsFault(err).Code != domain.Denied {
					t.Fatal("unauthorized pins selected host-key algorithms", algorithms, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !test.certificates {
				if !slices.Equal(algorithms, test.want) {
					t.Fatal("wrong pinned host-key algorithms", algorithms, test.want)
				}
				return
			}
			if len(algorithms) < len(test.want) || !slices.Equal(algorithms[:len(test.want)], test.want) {
				t.Fatal("raw pins did not retain preference before certificate algorithms", algorithms)
			}
			for _, algorithm := range algorithms[len(test.want):] {
				if !strings.Contains(algorithm, "-cert-") {
					t.Fatal("CA pin selected an unpinned raw host-key algorithm", algorithm)
				}
			}
			for _, algorithm := range []string{ssh.CertAlgoED25519v01, ssh.CertAlgoECDSA256v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01} {
				if !slices.Contains(algorithms, algorithm) {
					t.Fatal("CA pin lost a supported host-certificate algorithm", algorithm, algorithms)
				}
			}
		})
	}
	t.Run("invalid-pins", func(t *testing.T) {
		_, err := embeddedHostKeyAlgorithms([]byte("127.0.0.1 ssh-ed25519 invalid\n"), address,
			&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}, nil)
		if err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("invalid host pins were accepted", err)
		}
	})
	t.Run("verifier-failure", func(t *testing.T) {
		_, err := embeddedHostKeyAlgorithms([]byte(edPin), address,
			&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222},
			func(string, net.Addr, ssh.PublicKey) error { return errors.New("fixture verification failure") })
		if err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("host-pin verifier failure was ignored", err)
		}
	})
}

func testSSHHostSigner(t *testing.T, algorithm string) ssh.Signer {
	t.Helper()
	var key crypto.Signer
	var err error
	switch algorithm {
	case ssh.KeyAlgoED25519:
		_, key, err = ed25519.GenerateKey(rand.Reader)
	case ssh.KeyAlgoECDSA256:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case ssh.KeyAlgoRSA:
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	default:
		t.Fatal("unsupported fixture host-key algorithm", algorithm)
	}
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func testPinnedSSHSession(t *testing.T, open func(context.Context, config.SSH, string) (net.Conn, error), additionalHosts ...ssh.Signer) {
	t.Helper()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	testPinnedSSHSessionWithHost(t, open, hostSigner, nil, additionalHosts...)
}

func testPinnedSSHSessionWithHost(t *testing.T, open func(context.Context, config.SSH, string) (net.Conn, error),
	hostSigner ssh.Signer, authority ssh.PublicKey, additionalHosts ...ssh.Signer) {
	t.Helper()
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
	for _, signer := range additionalHosts {
		serverConfig.AddHostKey(signer)
	}
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
	pin := knownhosts.Line([]string{listener.Addr().String()}, hostSigner.PublicKey()) + "\n"
	if authority != nil {
		pin = "@cert-authority " + knownhosts.Line([]string{listener.Addr().String()}, authority) + "\n"
	}
	if err := secure.Create(pins, []byte(pin)); err != nil {
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
