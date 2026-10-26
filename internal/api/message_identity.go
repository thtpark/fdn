package api

import (
	"context"
	"crypto"
	"crypto/tls"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/message"
)

const OwnerChallengeOperation = "identity.owner.challenge"
const OwnerProveOperation = "identity.owner.prove"

type messageOwnerConnection struct{ session *message.Session }

func (c messageOwnerConnection) Ping(ctx context.Context) error { return c.session.Ping(ctx) }
func (c messageOwnerConnection) CloseNow() error                { return c.session.Close() }
func (c messageOwnerConnection) Close(code websocket.StatusCode, reason string) error {
	fault := domain.Unavailable
	if code == websocket.StatusPolicyViolation {
		fault = domain.Denied
	}
	return c.session.Shutdown(domain.Fail(fault, reason))
}

type joinOwner struct {
	mu        sync.Mutex
	actor     domain.Agent
	identity  domain.AgentIdentity
	challenge domain.IdentityChallenge
	entry     *controllerOwner
	public    crypto.PublicKey
}

func (s *Server) serveJoinMessages(ctx context.Context, conn net.Conn, uid uint32, state tls.ConnectionState) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if len(state.PeerCertificates) > 0 {
		var expire context.CancelFunc
		ctx, expire = context.WithDeadline(ctx, state.PeerCertificates[0].NotAfter)
		defer expire()
	}
	owner := &joinOwner{}
	var session *message.Session
	ready := make(chan struct{})
	handler := func(call context.Context, request message.Request) ([]byte, error) {
		<-ready
		if request.Target != "" || len(request.Query) != 0 || len(request.Payload) > domain.MaxBody {
			return nil, domain.Fail(domain.Invalid, "identity control does not accept target or query overrides")
		}
		switch request.Operation {
		case "query.identity.challenge":
			if len(request.Payload) != 0 {
				return nil, domain.Fail(domain.Invalid, "identity challenge does not accept a body")
			}
			challenge, err := s.Store.ChallengeIdentity(call, int64(uid))
			if err != nil {
				return nil, err
			}
			return message.MarshalPayload(challenge)
		case "command.identity.activate":
			var input domain.ActivateIdentityRequest
			if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
				return nil, err
			}
			identity, err := s.Store.ActivateIdentity(call, int64(uid), input)
			if err != nil {
				return nil, err
			}
			public, err := device.PublicKey(identity.AuthKey)
			if err != nil {
				return nil, err
			}
			certificate, err := s.identity.issue(identity, public, "device")
			if err != nil {
				return nil, err
			}
			actor, err := s.Store.Me(call, identity.AgentID)
			if err != nil {
				return nil, err
			}
			return message.MarshalPayload(domain.IdentityCertificate{Agent: actor, Identity: identity, Certificate: string(certificate)})
		case OwnerChallengeOperation, OwnerProveOperation:
			actor, fence, _, err := s.messagePrincipal(call, uid, state, "device")
			if err != nil {
				return nil, err
			}
			call = foundation.WithIdentityFence(call, fence)
			owner.mu.Lock()
			defer owner.mu.Unlock()
			if request.Operation == OwnerChallengeOperation {
				if len(request.Payload) != 0 || owner.entry != nil {
					return nil, domain.Fail(domain.Conflict, "owner challenge is only available before admission")
				}
				identity, err := s.Store.Identity(call, actor.ID)
				if err != nil {
					return nil, err
				}
				owner.actor, owner.identity = actor, identity
				owner.challenge = ownerChallenge(identity, 10*time.Second)
				return message.MarshalPayload(owner.challenge)
			}
			var input domain.OwnerRequest
			if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
				return nil, err
			}
			if owner.challenge.Nonce == "" || input.Nonce != owner.challenge.Nonce ||
				time.Now().Unix() >= owner.challenge.ExpiresAt || actor.ID != owner.actor.ID {
				return nil, domain.Fail(domain.Denied, "owner challenge is missing, mismatched or expired")
			}
			current, err := s.Store.Identity(call, actor.ID)
			if err != nil {
				return nil, err
			}
			if current.State != "active" || current.Generation != owner.identity.Generation || current.InstanceID != owner.identity.InstanceID {
				return nil, domain.Fail(domain.Denied, "owner key generation changed")
			}
			public, err := device.VerifyOwner(input, current)
			if err != nil {
				return nil, domain.Fail(domain.Denied, "invalid owner possession proof")
			}
			input.OwnerKey, err = canonicalOwner(public)
			if err != nil {
				return nil, err
			}
			if owner.entry == nil {
				entry, identity, err := s.admitOwner(call, ctx, actor, current, input, messageOwnerConnection{session})
				if err != nil {
					return nil, err
				}
				owner.entry, owner.identity, owner.public = entry, identity, public
				current = identity
			} else if owner.entry.ctx.Err() != nil || input.OwnerKey != owner.entry.key ||
				current.OwnerGeneration != owner.entry.generation || current.OwnerKey != owner.entry.key {
				return nil, domain.Fail(domain.Denied, "owner session was retired or changed")
			}
			certificate, err := s.identity.issue(current, public, "owner")
			if err != nil {
				return nil, err
			}
			owner.challenge = ownerChallenge(current, 15*time.Minute)
			return message.MarshalPayload(domain.OwnerCertificate{Certificate: string(certificate),
				Generation: owner.entry.generation, Challenge: owner.challenge})
		default:
			return nil, domain.Fail(domain.Denied, "join ingress only accepts identity admission and owner proof")
		}
	}
	var err error
	session, err = message.NewSession(ctx, conn, handler)
	close(ready)
	if err != nil {
		return err
	}
	defer session.Close()
	<-session.Done()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.entry != nil {
		s.releaseOwner(owner.actor.ID, owner.entry)
	}
	return session.Err()
}
