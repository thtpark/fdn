//go:build !linux && !darwin

package identityservice

import (
	"net"

	"github.com/thtpark/fdn/internal/domain"
)

func kernelPeer(net.Conn) (Peer, error) {
	return Peer{}, domain.Fail(domain.Prerequisite, "native identity IPC requires Linux or macOS peer credentials")
}
