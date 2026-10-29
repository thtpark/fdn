package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// OpenEmbeddedStream uses the same fixed SSH commands and file-backed pins as
// OpenStream, without a subprocess, agent, shell, forwarding, or terminal.
func OpenEmbeddedStream(ctx context.Context, c config.SSH, mode string) (net.Conn, error) {
	if _, err := StreamArguments(c, mode); err != nil {
		return nil, err
	}
	private, err := secure.Read(c.IdentityFile, domain.MaxBody)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return nil, domain.Fail(domain.Denied, "embedded SSH identity is invalid or requires interactive decryption")
	}
	pinned, err := knownhosts.New(c.KnownHostsFile)
	if err != nil {
		return nil, domain.Fail(domain.Denied, "embedded SSH host pins are invalid")
	}
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, domain.Fail(domain.Unavailable, "pinned embedded SSH connection failed")
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	deadline := time.Now().Add(10 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := raw.SetDeadline(deadline); err != nil {
		stop()
		raw.Close()
		return nil, err
	}
	connection, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{
		User: c.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: pinned,
	})
	if err != nil {
		stop()
		raw.Close()
		return nil, domain.Fail(domain.Denied, "embedded SSH authentication or pinned host verification failed")
	}
	client := ssh.NewClient(connection, channels, requests)
	channel, channelRequests, err := client.OpenChannel("session", nil)
	if err != nil {
		stop()
		client.Close()
		return nil, domain.Fail(domain.Unavailable, "embedded SSH session was refused")
	}
	accepted, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{mode}))
	if err != nil || !accepted {
		stop()
		channel.Close()
		client.Close()
		return nil, domain.Fail(domain.Denied, "embedded SSH fixed command was refused")
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		stop()
		channel.Close()
		client.Close()
		return nil, err
	}
	stream := &embeddedStream{channel: channel, client: client, raw: raw, stop: stop}
	go func() {
		for request := range channelRequests {
			if request.Type == "exit-status" {
				var status struct{ Status uint32 }
				if ssh.Unmarshal(request.Payload, &status) != nil || status.Status != 0 {
					stream.fail(domain.Fail(domain.Unavailable, "embedded SSH fixed command ended unsuccessfully"))
				}
			}
			if request.WantReply {
				if err := request.Reply(false, nil); err != nil {
					stream.fail(domain.Fail(domain.Unavailable, "embedded SSH channel response failed"))
				}
			}
		}
	}()
	go func() {
		stderr := process.NewBuffer(8192)
		if _, err := io.Copy(stderr, channel.Stderr()); err != nil && !errors.Is(err, net.ErrClosed) {
			stream.fail(domain.Fail(domain.Unavailable, "embedded SSH stderr failed or exceeded its private limit"))
		}
	}()
	return stream, nil
}

type embeddedStream struct {
	channel ssh.Channel
	client  *ssh.Client
	raw     net.Conn
	stop    func() bool
	once    sync.Once
	mu      sync.Mutex
	failure error
	closed  error
}

func (s *embeddedStream) fail(err error) {
	s.mu.Lock()
	if s.failure == nil {
		s.failure = err
	}
	s.mu.Unlock()
	_ = s.Close()
}
func (s *embeddedStream) Read(data []byte) (int, error) {
	n, err := s.channel.Read(data)
	if err != nil {
		s.mu.Lock()
		if s.failure != nil {
			err = s.failure
		}
		s.mu.Unlock()
	}
	return n, err
}
func (s *embeddedStream) Write(data []byte) (int, error) { return s.channel.Write(data) }
func (s *embeddedStream) Close() error {
	s.once.Do(func() {
		s.stop()
		for _, err := range []error{s.channel.Close(), s.client.Close(), s.raw.Close()} {
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				s.closed = errors.Join(s.closed, err)
			}
		}
	})
	return s.closed
}
func (s *embeddedStream) LocalAddr() net.Addr                { return s.raw.LocalAddr() }
func (s *embeddedStream) RemoteAddr() net.Addr               { return s.raw.RemoteAddr() }
func (s *embeddedStream) SetDeadline(t time.Time) error      { return s.raw.SetDeadline(t) }
func (s *embeddedStream) SetReadDeadline(t time.Time) error  { return s.raw.SetReadDeadline(t) }
func (s *embeddedStream) SetWriteDeadline(t time.Time) error { return s.raw.SetWriteDeadline(t) }
