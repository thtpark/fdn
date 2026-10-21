package ingress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/pki"
)

func socketTestDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".i-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func socketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := Listen(filepath.Join(socketTestDir(t), "s"), -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, client
}

func TestUnixListenerKernelUID(t *testing.T) {
	path := filepath.Join(socketTestDir(t), "s")
	listener, err := Listen(path, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("private socket permissions", info, err)
	}
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx := ConnContext(context.Background(), server)
	if uid, ok := UID(ctx); !ok || uid != uint32(os.Geteuid()) {
		t.Fatalf("kernel UID = %d, %v; expected %d", uid, ok, os.Geteuid())
	}
	if duplicate, err := Listen(path, -1); err == nil {
		duplicate.Close()
		t.Fatal("replaced an existing socket")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket was not removed", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal("close is not idempotent", err)
	}
}

func TestUnixListenerIngressGroup(t *testing.T) {
	root := socketTestDir(t)
	if err := os.Chown(root, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shared", "s")
	listener, err := Listen(path, os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0710}, {path, 0660}} {
		info, err := os.Lstat(item.path)
		if err != nil || info.Mode().Perm() != item.mode || info.Sys().(*syscall.Stat_t).Gid != uint32(os.Getegid()) {
			t.Fatal("ingress group permissions", item.path, info, err)
		}
	}
}

func TestUnixListenerRejectsUnprotectedPaths(t *testing.T) {
	root := socketTestDir(t)
	for _, path := range []string{"relative.sock", "/", root + "/../s", root + "//s", root + "/s\x00", root + "/s\n", root + "/" + strings.Repeat("s", 104)} {
		if listener, err := Listen(path, -1); err == nil {
			listener.Close()
			t.Fatalf("accepted invalid socket path %q", path)
		}
	}
	for _, group := range []int{-2, 1 << 32} {
		if listener, err := Listen(filepath.Join(root, "s"), group); err == nil {
			listener.Close()
			t.Fatal("accepted invalid group", group)
		}
	}
	for _, mode := range []os.FileMode{0777, 0770, 0755, 0750, 0600} {
		t.Run(strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			parent := filepath.Join(root, "m"+strconv.FormatUint(uint64(mode), 8))
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(parent, 0700)
			if listener, err := Listen(filepath.Join(parent, "s"), -1); err == nil {
				listener.Close()
				t.Fatal("accepted unprotected private directory")
			}
		})
	}
	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(alias, "s"), filepath.Join(alias, "nested", "s")} {
		if listener, err := Listen(path, -1); err == nil {
			listener.Close()
			t.Fatal("followed a symlink directory")
		}
	}
	for _, entry := range []string{"file", "link", "directory"} {
		path := filepath.Join(root, entry)
		switch entry {
		case "file":
			if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
		case "link":
			if err := os.Symlink(filepath.Join(root, "file"), path); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if listener, err := Listen(path, -1); err == nil {
			listener.Close()
			t.Fatal("replaced pre-existing path", entry)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("removed pre-existing path", entry, err)
		}
	}
}

func TestUnixListenerClosePreservesReplacement(t *testing.T) {
	path := filepath.Join(socketTestDir(t), "s")
	listener, err := Listen(path, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not the listener"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "not the listener" {
		t.Fatal("closed listener deleted a replacement", err)
	}
}

func TestConnContextTLSAndFailClosed(t *testing.T) {
	server, _ := socketPair(t)
	ctx := ConnContext(context.Background(), tls.Server(server, &tls.Config{MinVersion: tls.VersionTLS13}))
	if uid, ok := UID(ctx); !ok || uid != uint32(os.Geteuid()) {
		t.Fatal("TLS hid the Unix peer identity", uid, ok)
	}
	unknown, remote := net.Pipe()
	defer remote.Close()
	if _, ok := UID(ConnContext(ctx, unknown)); ok {
		t.Fatal("non-Unix transport inherited an identity")
	}
	if _, err := remote.Write([]byte("x")); err == nil {
		t.Fatal("unknown connection was not closed")
	}
	if _, ok := UID(ConnContext(ctx, nil)); ok {
		t.Fatal("nil connection inherited an identity")
	}
	if _, ok := UID(context.Background()); ok {
		t.Fatal("missing credentials authenticated")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := UID(ConnContext(ctx, server)); ok {
		t.Fatal("closed socket inherited an identity")
	}
}

func TestHTTPServerTLSUnixConnContext(t *testing.T) {
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(bundle.Cert, bundle.Key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(bundle.CA)
	path := filepath.Join(socketTestDir(t), "s")
	listener, err := Listen(path, -1)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ConnContext: ConnContext, ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := UID(r.Context())
			if !ok {
				http.Error(w, "missing peer identity", http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, strconv.FormatUint(uint64(uid), 10))
		})}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}))
	}()
	defer func() {
		_ = server.Close()
		select {
		case err := <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("HTTP listener goroutine leaked")
		}
	}()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	request, err := http.NewRequest(http.MethodGet, "https://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Foundation-UID", "4294967295")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != strconv.Itoa(os.Geteuid()) {
		t.Fatal("HTTP context was not bound to the kernel peer", response.StatusCode, string(body), err)
	}
}
