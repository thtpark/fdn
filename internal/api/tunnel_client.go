package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/transport"
)

func NewTunnelClient(c config.Worker, certificate tls.Certificate, mode string) (*Client, error) {
	return newMessageTunnelClient(c, certificate, mode, process.Native{})
}

const MessageALPN = "fdn-message/2"

func tunnelTrust(c config.Worker, certificate tls.Certificate, mode string) (config.SSH, *tls.Config, string, error) {
	if err := config.URL(c.URL); err != nil {
		return config.SSH{}, nil, "", err
	}
	if c.SSH == nil {
		return config.SSH{}, nil, "", domain.Fail(domain.Prerequisite, "Foundation access requires a pinned SSH stream")
	}
	sshConfig := *c.SSH
	if _, err := transport.StreamArguments(sshConfig, mode); err != nil {
		return config.SSH{}, nil, "", err
	}
	tlsConfig, err := pki.Client(c.CAFile, c.ServerName)
	if err != nil {
		return config.SSH{}, nil, "", err
	}
	if len(certificate.Certificate) == 0 {
		if mode != "fdn-join" || certificate.PrivateKey != nil {
			return config.SSH{}, nil, "", domain.Fail(domain.Prerequisite, "ordinary API access requires an owner certificate")
		}
	} else {
		if err := tunnelCertificate(&certificate, tlsConfig.RootCAs, mode); err != nil {
			return config.SSH{}, nil, "", err
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	origin, _ := url.Parse(c.URL)
	if origin.RawPath != "" || origin.Opaque != "" || origin.ForceQuery {
		return config.SSH{}, nil, "", domain.Fail(domain.Invalid, "Foundation tunnel URL must be a canonical HTTPS origin")
	}
	port := origin.Port()
	if port == "" {
		port = "443"
	}
	address := net.JoinHostPort(origin.Hostname(), port)
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = origin.Hostname()
	}
	return sshConfig, tlsConfig, address, nil
}

func newMessageTunnelClient(c config.Worker, certificate tls.Certificate, mode string, runner process.Runner) (*Client, error) {
	sshConfig, tlsConfig, _, err := tunnelTrust(c, certificate, mode)
	if err != nil {
		return nil, err
	}
	tlsConfig.NextProtos = []string{MessageALPN}
	client := NewMessageClient(strings.TrimRight(c.URL, "/"), func(ctx context.Context) (net.Conn, error) {
		streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stop := context.AfterFunc(ctx, cancel)
		conn, err := transport.OpenStream(streamCtx, sshConfig, mode, runner)
		if err != nil {
			stop()
			cancel()
			return nil, err
		}
		secured := tls.Client(conn, tlsConfig.Clone())
		err = secured.HandshakeContext(ctx)
		stop()
		if err != nil || ctx.Err() != nil {
			cancel()
			_ = secured.Close()
			return nil, domain.Fail(domain.Unavailable, "pinned SSH identity TLS connection failed")
		}
		if secured.ConnectionState().NegotiatedProtocol != MessageALPN {
			cancel()
			_ = secured.Close()
			return nil, domain.Fail(domain.Prerequisite, "Foundation does not support the binary message protocol")
		}
		return &tunnelConnection{Conn: secured, cancel: cancel}, nil
	})
	return client, nil
}

// newTunnelClient is the HTTP business-fixture adapter. Native clients use the
// purpose-bound binary SSH transport and never downgrade to this adapter.
func newTunnelClient(c config.Worker, certificate tls.Certificate, mode string, runner process.Runner) (*Client, error) {
	sshConfig, tlsConfig, address, err := tunnelTrust(c, certificate, mode)
	if err != nil {
		return nil, err
	}
	tlsConfig.NextProtos = []string{"http/1.1"}
	httpTransport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if (network != "tcp" && network != "tcp4" && network != "tcp6") || target != address {
				return nil, domain.Fail(domain.Denied, "SSH transport is bound to its configured Foundation origin")
			}
			// A successful HTTP dial outlives the request context, like net.Dialer.DialContext.
			streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			stop := context.AfterFunc(ctx, cancel)
			conn, err := transport.OpenStream(streamCtx, sshConfig, mode, runner)
			stop()
			if err != nil {
				cancel()
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				cancel()
				conn.Close()
				return nil, err
			}
			return &tunnelConnection{Conn: conn, cancel: cancel}, nil
		},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}
	return &Client{BaseURL: strings.TrimRight(c.URL, "/"),
		HTTP: &http.Client{Transport: httpTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type tunnelConnection struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *tunnelConnection) Close() error {
	c.cancel()
	return c.Conn.Close()
}

func tunnelCertificate(certificate *tls.Certificate, roots *x509.CertPool, mode string) error {
	if len(certificate.Certificate) > 16 {
		return domain.Fail(domain.Invalid, "client certificate chain is too long")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return err
	}
	identity, err := pki.ParseIdentity(leaf)
	if err != nil {
		return err
	}
	if (mode == "fdn-api" && identity.Purpose != "owner") || (mode == "fdn-join" && identity.Purpose != "device") {
		return domain.Fail(domain.Denied, "client certificate purpose does not match the SSH stream")
	}
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok || signer == nil {
		return domain.Fail(domain.Invalid, "client certificate requires its private signing key")
	}
	value := reflect.ValueOf(signer)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice) && value.IsNil() {
		return domain.Fail(domain.Invalid, "client certificate requires its private signing key")
	}
	if key, ok := signer.(ed25519.PrivateKey); ok && len(key) != ed25519.PrivateKeySize {
		return domain.Fail(domain.Invalid, "invalid client Ed25519 private key")
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicDER, leaf.RawSubjectPublicKeyInfo) {
		return domain.Fail(domain.Denied, "client certificate and private key do not match")
	}
	intermediates := x509.NewCertPool()
	for _, der := range certificate.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return domain.Fail(domain.Denied, "client certificate is expired or does not chain to the pinned CA")
	}
	certificate.Leaf = leaf
	return nil
}
