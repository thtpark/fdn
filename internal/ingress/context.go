// Package ingress provides the native, local-only Foundation ingress boundary.
package ingress

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
)

var ErrNoPeerUID = errors.New("ingress requires a kernel-authenticated Unix peer")

type peerKey struct{}
type peerIdentity struct {
	uid uint32
	ok  bool
}

// ConnContext is an http.Server.ConnContext hook. Only actual Unix connections,
// optionally wrapped by crypto/tls, carry an identity. Failed/unknown transports
// are closed and any identity inherited from the input context is masked.
func ConnContext(ctx context.Context, conn net.Conn) context.Context {
	uid, err := connectionUID(conn)
	identity := peerIdentity{uid: uid, ok: err == nil}
	if err != nil && conn != nil {
		_ = conn.Close()
	}
	return context.WithValue(ctx, peerKey{}, identity)
}

// UID returns the kernel peer UID, never an HTTP header or SSH-supplied value.
func UID(ctx context.Context) (uint32, bool) {
	identity, ok := ctx.Value(peerKey{}).(peerIdentity)
	return identity.uid, ok && identity.ok
}

func connectionUID(conn net.Conn) (uint32, error) {
	for depth := 0; depth < 8; depth++ {
		if wrapped, ok := conn.(*tls.Conn); ok && wrapped != nil {
			conn = wrapped.NetConn()
			continue
		}
		unixConn, ok := conn.(*net.UnixConn)
		if !ok || unixConn == nil {
			return 0, ErrNoPeerUID
		}
		raw, err := unixConn.SyscallConn()
		if err != nil {
			return 0, errors.Join(ErrNoPeerUID, err)
		}
		var uid uint32
		var credentialErr error
		if err := raw.Control(func(fd uintptr) {
			uid, credentialErr = kernelPeerUID(int(fd))
		}); err != nil {
			return 0, errors.Join(ErrNoPeerUID, err)
		}
		if credentialErr != nil {
			return 0, errors.Join(ErrNoPeerUID, credentialErr)
		}
		return uid, nil
	}
	return 0, ErrNoPeerUID
}
