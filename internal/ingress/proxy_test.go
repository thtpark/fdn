package ingress

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type gatewayOutput struct {
	bytes.Buffer
	closed bool
}

func (w *gatewayOutput) Close() error { w.closed = true; return nil }

func proxyFixture(t *testing.T) (ProxyOptions, net.Listener, net.Listener) {
	t.Helper()
	root := socketTestDir(t)
	options := ProxyOptions{APISocket: filepath.Join(root, "a"), JoinSocket: filepath.Join(root, "j"), ServerUID: uint32(os.Geteuid()), IdleTimeout: time.Second, MaxDuration: 3 * time.Second}
	api, err := Listen(options.APISocket, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	join, err := Listen(options.JoinSocket, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = join.Close() })
	return options, api, join
}

func joinGatewayWorker(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("gateway worker did not stop")
		return nil
	}
}

func TestGatewayExactSelectorsAndHalfClose(t *testing.T) {
	for _, selector := range []string{"fdn-api", "fdn-join"} {
		t.Run(selector, func(t *testing.T) {
			options, api, join := proxyFixture(t)
			listener := api
			if selector == "fdn-join" {
				listener = join
			}
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				request, err := io.ReadAll(conn)
				if err == nil && string(request) != "request" {
					err = errors.New("incorrect gateway request")
				}
				if err == nil {
					_, err = io.WriteString(conn, selector)
				}
				done <- err
			}()
			output := &gatewayOutput{}
			err := Proxy(context.Background(), selector, io.NopCloser(strings.NewReader("request")), output, options)
			if err != nil || output.String() != selector || !output.closed {
				t.Fatal("fixed selector/half-close failed", output.String(), err)
			}
			if err := joinGatewayWorker(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayRejectsCommandsAndUntrustedEndpoints(t *testing.T) {
	for _, selector := range []string{"", " fdn-api", "fdn-api ", "fdn-api\n", "FDN-API", "fdn-api --socket /x", "fdn-join; id", "sftp", "internal-sftp", "direct-tcpip", "/bin/sh", "127.0.0.1:8080", "fdn-api\x00"} {
		output := &gatewayOutput{}
		if err := Proxy(context.Background(), selector, io.NopCloser(strings.NewReader("")), output, ProxyOptions{}); !errors.Is(err, ErrSelector) || !output.closed {
			t.Fatalf("selector %q was not rejected: %v", selector, err)
		}
	}
	options, _, _ := proxyFixture(t)
	for _, change := range []func(*ProxyOptions){
		func(o *ProxyOptions) { o.APISocket = "127.0.0.1:8080" },
		func(o *ProxyOptions) { o.JoinSocket = o.APISocket },
		func(o *ProxyOptions) { o.ServerUID++ },
		func(o *ProxyOptions) { o.MaxBytes = -1 },
		func(o *ProxyOptions) { o.MaxBytes = 1<<30 + 1 },
		func(o *ProxyOptions) { o.MaxDuration = -time.Second },
		func(o *ProxyOptions) { o.DialTimeout = time.Hour },
		func(o *ProxyOptions) { o.IdleTimeout = time.Hour },
	} {
		invalid := options
		change(&invalid)
		if err := Proxy(context.Background(), "fdn-api", io.NopCloser(strings.NewReader("")), &gatewayOutput{}, invalid); err == nil {
			t.Fatal("accepted unsafe endpoint/options", invalid)
		}
	}
	alias := filepath.Join(filepath.Dir(options.APISocket), "alias")
	if err := os.Symlink(options.APISocket, alias); err != nil {
		t.Fatal(err)
	}
	options.APISocket = alias
	if err := Proxy(context.Background(), "fdn-api", io.NopCloser(strings.NewReader("")), &gatewayOutput{}, options); err == nil {
		t.Fatal("dialed a symlink socket")
	}
}

func TestGatewayByteLimits(t *testing.T) {
	for _, upstream := range []bool{true, false} {
		t.Run(map[bool]string{true: "input", false: "output"}[upstream], func(t *testing.T) {
			options, listener, _ := proxyFixture(t)
			options.MaxBytes = 4
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				if upstream {
					data, err := io.ReadAll(conn)
					if len(data) > 4 {
						err = errors.New("gateway forwarded beyond its limit")
					}
					done <- err
				} else {
					_, _ = io.WriteString(conn, "too many bytes")
					done <- nil
				}
			}()
			input := ""
			if upstream {
				input = "too many bytes"
			}
			output := &gatewayOutput{}
			if err := Proxy(context.Background(), "fdn-api", io.NopCloser(strings.NewReader(input)), output, options); !errors.Is(err, ErrProxyLimit) {
				t.Fatal("missing byte-limit failure", err)
			}
			if output.Len() > 4 {
				t.Fatal("gateway wrote beyond the output limit")
			}
			if err := joinGatewayWorker(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayCancellationIdleAndLifetimeJoinWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for _, mode := range []string{"cancel-input", "cancel-output", "idle", "lifetime"} {
		t.Run(mode, func(t *testing.T) {
			for attempt := 0; attempt < 5; attempt++ {
				options, listener, _ := proxyFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				input, inputWriter := io.Pipe()
				outputReader, output := io.Pipe()
				t.Cleanup(func() {
					cancel()
					_ = input.Close()
					_ = inputWriter.Close()
					_ = outputReader.Close()
					_ = output.Close()
				})
				accepted := make(chan struct{})
				serverDone := make(chan error, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						close(accepted)
						serverDone <- err
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
					close(accepted)
					if mode == "cancel-output" {
						if _, err := io.WriteString(conn, "blocked output"); err != nil {
							serverDone <- err
							return
						}
					}
					_, err = io.Copy(io.Discard, conn)
					serverDone <- err
				}()
				if mode == "idle" {
					options.IdleTimeout = 20 * time.Millisecond
				}
				if mode == "lifetime" {
					options.MaxDuration = 20 * time.Millisecond
				}
				proxyDone := make(chan error, 1)
				go func() { proxyDone <- Proxy(ctx, "fdn-api", input, output, options) }()
				<-accepted
				if mode == "cancel-output" {
					// Read only one byte so cancellation interrupts a pending output write.
					ready := make(chan error, 1)
					go func() {
						var first [1]byte
						_, err := io.ReadFull(outputReader, first[:])
						ready <- err
					}()
					if err := joinGatewayWorker(t, ready); err != nil {
						t.Fatal("gateway output did not become ready", err)
					}
				}
				if strings.HasPrefix(mode, "cancel") {
					cancel()
				}
				err := joinGatewayWorker(t, proxyDone)
				cancel()
				_ = inputWriter.Close()
				_ = outputReader.Close()
				if mode == "idle" && !errors.Is(err, ErrIdleTimeout) || mode == "lifetime" && !errors.Is(err, context.DeadlineExceeded) || strings.HasPrefix(mode, "cancel") && !errors.Is(err, context.Canceled) {
					t.Fatal("wrong gateway cancellation result", mode, err)
				}
				// Linux may reset a Unix peer closed with unread output during cancellation.
				if err := joinGatewayWorker(t, serverDone); err != nil && !(mode == "cancel-output" && errors.Is(err, syscall.ECONNRESET)) {
					t.Fatal(err)
				}
			}
		})
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count := runtime.NumGoroutine(); count > baseline+1 {
		t.Fatalf("gateway workers leaked: before=%d after=%d", baseline, count)
	}
}

func TestGatewayCopyShortWrite(t *testing.T) {
	writer := shortGatewayWriter{}
	if err := copyBounded(writer, strings.NewReader("bytes"), 1024, make(chan struct{}, 1)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write was hidden", err)
	}
}

type shortGatewayWriter struct{}

func (shortGatewayWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestGatewayInheritedNativeStdinCancellation(t *testing.T) {
	if os.Getenv("FDN_GATEWAY_STDIO_CHILD") == "1" {
		uid, err := strconv.ParseUint(os.Getenv("FDN_GATEWAY_PEER"), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		err = Proxy(ctx, "fdn-api", os.Stdin, os.Stdout, ProxyOptions{APISocket: os.Getenv("FDN_GATEWAY_SOCKET"), JoinSocket: os.Getenv("FDN_GATEWAY_JOIN"), ServerUID: uint32(uid)})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		return
	}
	options, listener, _ := proxyFixture(t)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, err = io.Copy(io.Discard, conn)
		serverDone <- err
	}()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestGatewayInheritedNativeStdinCancellation$")
	command.Env = []string{"FDN_GATEWAY_STDIO_CHILD=1", "FDN_GATEWAY_PEER=" + strconv.Itoa(os.Geteuid()), "FDN_GATEWAY_SOCKET=" + options.APISocket, "FDN_GATEWAY_JOIN=" + options.JoinSocket}
	command.Stdin, command.Stdout = input, io.Discard
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		t.Fatalf("inherited SSH stdio cancellation did not join copy workers: %v: %s", err, diagnostic.String())
	}
	if err := joinGatewayWorker(t, serverDone); err != nil {
		t.Fatal(err)
	}
}
