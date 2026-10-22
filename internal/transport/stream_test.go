package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type streamRunner func(context.Context, process.Spec) error

func (r streamRunner) Run(ctx context.Context, spec process.Spec) error { return r(ctx, spec) }

func streamSSH(t *testing.T) config.SSH {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private ssh")
	identity, pins := filepath.Join(root, "identity"), filepath.Join(root, "known_hosts")
	if err := secure.Create(identity, []byte("fixture-private-key")); err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(pins, []byte("fixture-host-key")); err != nil {
		t.Fatal(err)
	}
	return config.SSH{Host: "foundation.example", User: "agent_test", Port: 22, IdentityFile: identity, KnownHostsFile: pins}
}

func TestStreamArgumentsNoForwardingOrInheritedAuthority(t *testing.T) {
	c := streamSSH(t)
	for _, mode := range []string{"fdn-api", "fdn-join"} {
		args, err := StreamArguments(c, mode)
		if err != nil {
			t.Fatal(err)
		}
		if args[len(args)-1] != mode || args[len(args)-2] != c.User+"@"+c.Host {
			t.Fatal("remote mode changed")
		}
		for _, arg := range args {
			if arg == "-N" || arg == "-L" || arg == "-R" || arg == "-D" || arg == "-A" || arg == "-s" || arg == "-W" {
				t.Fatal("stream configured generic forwarding", arg)
			}
		}
		joined := strings.Join(args, " ")
		for _, required := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "GlobalKnownHostsFile=/dev/null", "ControlMaster=no", "ControlPath=none", "ForwardAgent=no", "ForwardX11=no", "ClearAllForwardings=yes", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "ProxyCommand=none", "SendEnv=-*", "UserKnownHostsFile=\"" + c.KnownHostsFile + "\""} {
			if !strings.Contains(joined, required) {
				t.Fatal("missing isolation option", required)
			}
		}
	}
	for _, mode := range []string{"", "fdn-api extra", "cat", "fdn-join\n", "-L"} {
		if _, err := StreamArguments(c, mode); err == nil {
			t.Fatal("unsupported mode accepted")
		}
	}
	for _, mutate := range []func(*config.SSH){
		func(c *config.SSH) { c.Host = "-oProxyCommand=bad" },
		func(c *config.SSH) { c.User = "agent user" },
		func(c *config.SSH) { c.Port = 0 },
		func(c *config.SSH) { c.Port = 65536 },
		func(c *config.SSH) { c.IdentityFile = "relative" },
		func(c *config.SSH) { c.KnownHostsFile += "\nother" },
		func(c *config.SSH) { c.KnownHostsFile += "%h" },
		func(c *config.SSH) { c.KnownHostsFile += "${HOME}" },
	} {
		changed := c
		mutate(&changed)
		if _, err := StreamArguments(changed, "fdn-api"); err == nil {
			t.Fatal("unsafe SSH configuration accepted")
		}
	}
}

func TestStreamDuplexCloseAndPrivateEnvironment(t *testing.T) {
	c := streamSSH(t)
	t.Setenv("SSH_AUTH_SOCK", "must-not-leak")
	t.Setenv("COPILOT_HOME", "must-not-leak")
	t.Setenv("SECRET_TEST_TOKEN", "must-not-leak")
	done := make(chan struct{})
	runner := streamRunner(func(ctx context.Context, spec process.Spec) error {
		defer close(done)
		if spec.Path != "/usr/bin/ssh" || len(spec.Env) != 3 {
			return errors.New("unexpected process boundary")
		}
		for _, entry := range spec.Env {
			if strings.Contains(entry, "must-not-leak") || strings.HasPrefix(entry, "SSH_") || strings.HasPrefix(entry, "COPILOT_") {
				return errors.New("inherited authority")
			}
		}
		if err := spec.Started(123); err != nil {
			return err
		}
		go io.Copy(spec.Stdout, spec.Stdin)
		<-ctx.Done()
		return ctx.Err()
	})
	conn, err := OpenStream(context.Background(), c, "fdn-api", runner)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	payload := []byte{0, 1, 2, '\n', 255}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil || !bytes.Equal(payload, response) {
		t.Fatal("stream did not preserve binary bytes", err)
	}
	if conn.LocalAddr().Network() != "pipe" || conn.RemoteAddr().Network() != "pipe" {
		t.Fatal("stream used a TCP listener")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel owned process")
	}
	if _, err := conn.Write(payload); err == nil {
		t.Fatal("closed stream is writable")
	}
}

func TestStreamCancellationAndBoundedPrivateStderr(t *testing.T) {
	c := streamSSH(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runner := streamRunner(func(ctx context.Context, spec process.Spec) error {
		defer close(done)
		if _, err := spec.Stderr.Write(bytes.Repeat([]byte("private-output"), 10000)); err != nil {
			return err
		}
		if data, err := spec.Stderr.(*process.BoundedBuffer).Bytes(); err == nil || data != nil {
			return errors.New("stderr was not bounded")
		}
		if err := spec.Started(123); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	conn, err := OpenStream(ctx, c, "fdn-join", runner)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not reach owned runner")
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled stream remained open")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	failed := streamRunner(func(context.Context, process.Spec) error { return errors.New("secret stderr must not escape") })
	if _, err := OpenStream(context.Background(), c, "fdn-api", failed); err == nil || strings.Contains(err.Error(), "secret stderr") || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("SSH startup failure was hidden or leaked diagnostics", err)
	}
	if _, err := OpenStream(ctx, c, "fdn-api", runner); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled open started another process", err)
	}
}

func TestStreamNativeOwnedProcessRoundTrip(t *testing.T) {
	c := streamSSH(t)
	done := make(chan struct{})
	runner := streamRunner(func(ctx context.Context, spec process.Spec) error {
		defer close(done)
		// Exercise native pipe copying and owned-process cancellation without contacting an SSH host.
		spec.Path, spec.Args = "/bin/cat", nil
		return (process.Native{}).Run(ctx, spec)
	})
	conn, err := OpenStream(context.Background(), c, "fdn-api", runner)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	data := []byte("native stream\n")
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(data))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(data, got) {
		t.Fatal("native duplex failed", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned native process did not exit")
	}
}

func TestStreamNativeChildExitPropagatesEOFWithoutClientInput(t *testing.T) {
	c := streamSSH(t)
	done := make(chan struct{})
	runner := streamRunner(func(ctx context.Context, spec process.Spec) error {
		defer close(done)
		spec.Path, spec.Args = "/bin/sh", []string{"-c", "printf ended"}
		return (process.Native{}).Run(ctx, spec)
	})
	conn, err := OpenStream(context.Background(), c, "fdn-join", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "ended" {
		t.Fatalf("native child exit did not close its stream: %q %v", data, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exited native child remained blocked on unused stdin")
	}
}

func TestStreamCleanupFailuresRemainObservable(t *testing.T) {
	c := streamSSH(t)
	runner := streamRunner(func(ctx context.Context, spec process.Spec) error {
		if err := spec.Started(123); err != nil {
			return err
		}
		<-ctx.Done()
		return &process.CleanupError{Err: ctx.Err()}
	})
	conn, err := OpenStream(context.Background(), c, "fdn-api", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatal("failed process cleanup became success", err)
	}
}

var _ net.Conn = (*streamConn)(nil)
