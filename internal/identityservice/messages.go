package identityservice

import (
	"context"
	"net"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func (s *Service) serveMessagePeer(ctx context.Context, conn net.Conn) error {
	peer, ok := conn.(*peerConnection)
	if !ok || peer.peer.UID != s.config.WorkerUID || peer.peer.GID != s.config.WorkerGID {
		return domain.Fail(domain.Denied, "local identity IPC peer is not the configured tool-runner")
	}
	session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
		switch request.Operation {
		case "query.local.status", "query.local.profile":
			if request.Target != "" || len(request.Query) != 0 || len(request.Payload) != 0 {
				return nil, domain.Fail(domain.Invalid, "public identity observations do not accept arguments")
			}
			if request.Operation == "query.local.status" {
				return message.MarshalPayload(s.Status())
			}
			s.mu.RLock()
			profile := s.profile
			s.mu.RUnlock()
			return message.MarshalPayload(profile)
		}
		if err := api.ValidateOrdinaryMessage(request); err != nil {
			return nil, err
		}
		s.mu.RLock()
		lease, problem, ownerExpiry := s.lease, s.status.Problem, s.ownerExpiry
		s.mu.RUnlock()
		now := s.options.Now()
		if problem == domain.Denied || problem == domain.Conflict || (!ownerExpiry.IsZero() && !ownerExpiry.After(now)) {
			return nil, domain.Fail(domain.Denied, "identity owner authority was withdrawn or expired")
		}
		if lease != nil && !lease.expires.After(now) {
			return nil, domain.Fail(domain.Denied, "identity owner certificate expired")
		}
		if lease == nil || lease.context.Err() != nil {
			return nil, domain.Fail(domain.Unavailable, "identity owner connection is not ready")
		}
		forward, cancel := context.WithDeadline(call, lease.expires)
		stop := context.AfterFunc(lease.context, cancel)
		defer stop()
		defer cancel()
		result, err := lease.client.ForwardMessage(forward, request)
		if err != nil && !lease.expires.After(s.options.Now()) {
			return nil, domain.Fail(domain.Denied, "identity owner certificate expired during the request")
		}
		return result, message.PublicError(err)
	})
	if err != nil {
		return err
	}
	defer session.Close()
	<-session.Done()
	return session.Err()
}
