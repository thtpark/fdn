package identityservice

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

type additionRelayProxy struct {
	service *Service
	mu      sync.Mutex
	lease   *ownerLease
	ticket  string
	closed  bool
}

func (p *additionRelayProxy) call(ctx context.Context, request message.Request) ([]byte, bool, error) {
	if !strings.HasPrefix(request.Operation, api.AgentAdditionRelayPrefix+".") {
		return nil, false, nil
	}
	p.mu.Lock()
	if request.Operation == api.AgentAdditionRelayOperation {
		if p.closed || p.lease != nil {
			p.mu.Unlock()
			return nil, true, domain.Fail(domain.Conflict, "this local connection already admitted an agent addition relay")
		}
		var input domain.AgentAdditionRelayOpen
		if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
			p.mu.Unlock()
			return nil, true, err
		}
		if !domain.ValidOperation(input.Ticket) {
			p.mu.Unlock()
			return nil, true, domain.Fail(domain.Invalid, "agent addition relay ticket is invalid")
		}
		lease, err := p.service.acquireOwnerLease()
		if err != nil {
			p.mu.Unlock()
			return nil, true, err
		}
		// Keep connection-local tickets on their admitted owner transport across renewal.
		p.lease, p.ticket = lease, input.Ticket
	} else if p.closed || p.lease == nil || request.Target != p.ticket {
		p.mu.Unlock()
		return nil, true, domain.Fail(domain.Denied, "agent addition relay ticket belongs to another local connection")
	}
	lease := p.lease
	p.mu.Unlock()
	result, err, _ := callOwnerMessage(ctx, lease, request)
	return result, true, err
}

func (p *additionRelayProxy) close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	lease, ticket := p.lease, p.ticket
	p.mu.Unlock()
	if lease == nil {
		return nil
	}
	defer lease.release()
	if lease.context.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := lease.client.Message(ctx, message.Request{Operation: api.AgentAdditionRelayPrefix + ".close", Target: ticket})
	return err
}
