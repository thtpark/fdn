package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func AttachTerminal(ctx context.Context, client *api.Client, id string, input, output *os.File) (err error) {
	conn, err := Dial(ctx, client, id, "requester")
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(input.Fd()), state)) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(output, conn); done <- err; cancel() }()
	buffer := make([]byte, 4096)
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		_, pollErr := unix.Poll(fds, 100)
		if pollErr != nil {
			if errors.Is(pollErr, unix.EINTR) {
				continue
			}
			err = pollErr
			break
		}
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			n, readErr := input.Read(buffer)
			if n > 0 {
				if bytes.IndexByte(buffer[:n], 0x1d) >= 0 {
					cancel()
					break
				}
				if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
					err = writeErr
					break
				}
			}
			if readErr != nil {
				err = readErr
				break
			}
		}
	}
	conn.Close()
	copyErr := <-done
	if errors.Is(copyErr, net.ErrClosed) || errors.Is(copyErr, io.EOF) {
		copyErr = nil
	}
	return errors.Join(err, copyErr)
}

func viewerArguments(port int) []string {
	return []string{"-AcceptClipboard=0", "-SendClipboard=0", "127.0.0.1::" + strconv.Itoa(port)}
}

func AttachVNC(ctx context.Context, client *api.Client, id string, diagnostic io.Writer) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	address := listener.Addr().String()
	if _, err := fmt.Fprintf(diagnostic, "Opening the consented VNC viewer at %s; authentication stays in the human UI.\n", address); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	viewerDone := make(chan error, 1)
	port := listener.Addr().(*net.TCPAddr).Port
	path, args := "vncviewer", viewerArguments(port)
	environment := []string{}
	for _, name := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	go func() {
		viewerDone <- (process.Native{}).Run(ctx, process.Spec{Path: path, Args: args, Env: process.Environment(environment...), Stderr: diagnostic})
	}()
	accepted := make(chan net.Conn, 1)
	acceptError := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptError <- err
		} else {
			accepted <- conn
		}

	}()
	var conn net.Conn
	select {
	case <-ctx.Done():
		listener.Close()
		cancel()
		<-viewerDone
		return ctx.Err()
	case err := <-acceptError:
		cancel()
		<-viewerDone
		return err
	case err := <-viewerDone:
		if err != nil {
			return domain.Fail(domain.Prerequisite, "native VNC viewer could not start; install an existing trusted viewer")
		}
		return domain.Fail(domain.Prerequisite, "VNC viewer exited before connecting")
	case conn = <-accepted:
	}
	defer conn.Close()
	relay, err := Dial(ctx, client, id, "requester")
	if err != nil {
		cancel()
		<-viewerDone
		return err
	}
	err = Bridge(ctx, relay, conn)
	cancel()
	select {
	case <-viewerDone:
	case <-time.After(5 * time.Second):
		return errors.Join(err, domain.Fail(domain.Prerequisite, "viewer control process did not stop"))
	}
	return err
}
