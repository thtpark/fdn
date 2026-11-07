package identityservice

import (
	"context"
	"errors"
	"net"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func (s *Service) serveMessagePeer(ctx context.Context, conn net.Conn) (err error) {
	peer, ok := conn.(*peerConnection)
	if !ok || peer.peer.UID != s.config.WorkerUID || peer.peer.GID != s.config.WorkerGID {
		return domain.Fail(domain.Denied, "local identity IPC peer is not the configured tool-runner")
	}
	relays := additionRelayProxy{service: s}
	defer func() { err = errors.Join(err, relays.close()) }()
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
		if result, handled, err := relays.call(call, request); handled {
			return result, message.PublicError(err)
		}
		return s.forwardOwnerMessage(call, request)
	})
	if err != nil {
		return err
	}
	defer session.Close()
	<-session.Done()
	return session.Err()
}

func renewalWatch(request message.Request) bool {
	return (request.Operation == "query.updates" || request.Operation == "query.events") &&
		request.Query["wait"] == "true"
}

func (s *Service) forwardOwnerMessage(call context.Context, request message.Request) ([]byte, error) {
	var previous *ownerLease
	for {
		if err := call.Err(); err != nil {
			return nil, message.PublicError(err)
		}
		lease, err := s.acquireOwnerLease()
		if err != nil {
			return nil, err
		}
		if previous != nil && (previous.authority.Err() != nil ||
			lease.authority.Done() != previous.authority.Done() ||
			lease.ownerGeneration != previous.ownerGeneration) {
			lease.release()
			return nil, domain.Fail(domain.Denied, "identity owner authority changed during watch renewal")
		}
		result, err, renewed := forwardOwnerCall(call, lease, request)
		if renewed {
			previous = lease
			continue
		}
		if err != nil && !lease.expires.After(s.options.Now()) {
			return nil, domain.Fail(domain.Denied, "identity owner certificate expired during the request")
		}
		return result, message.PublicError(err)
	}
}

func forwardOwnerCall(call context.Context, lease *ownerLease, request message.Request) ([]byte, error, bool) {
	defer lease.release()
	return callOwnerMessage(call, lease, request)
}

func callOwnerMessage(call context.Context, lease *ownerLease, request message.Request) ([]byte, error, bool) {
	forward, cancel := context.WithCancelCause(call)
	defer cancel(nil)
	stopOwner := context.AfterFunc(lease.context, func() { cancel(context.Cause(lease.context)) })
	defer stopOwner()
	if renewalWatch(request) {
		stopRenewal := context.AfterFunc(lease.retirement, func() {
			if errors.Is(context.Cause(lease.retirement), errOwnerLeaseRenewed) {
				cancel(errOwnerLeaseRenewed)
			}
		})
		defer stopRenewal()
	}
	result, err := lease.client.ForwardMessage(forward, request)
	renewed := err != nil && call.Err() == nil &&
		errors.Is(context.Cause(forward), errOwnerLeaseRenewed) &&
		(errors.Is(err, context.Canceled) || domain.AsFault(err).Code == domain.Cancelled)
	return result, err, renewed
}
