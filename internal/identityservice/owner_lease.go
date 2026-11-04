package identityservice

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

var errOwnerLeaseRenewed = errors.New("owner lease replaced by a validated renewal")

type ownerLease struct {
	client          *api.Client
	authority       context.Context
	ownerGeneration int64
	context         context.Context
	expires         time.Time
	cancel          context.CancelFunc
	retirement      context.Context
	notifyRetired   context.CancelCauseFunc
	mu              sync.Mutex
	active          int
	retired         bool
}

func newOwnerLease(parent context.Context, client *api.Client, generation int64, expires time.Time, lifetime time.Duration, closed *sync.WaitGroup) *ownerLease {
	ctx, cancel := context.WithTimeout(parent, lifetime)
	retirement, notifyRetired := context.WithCancelCause(ctx)
	lease := &ownerLease{
		client: client, authority: parent, ownerGeneration: generation,
		context: ctx, expires: expires, cancel: cancel,
		retirement: retirement, notifyRetired: notifyRetired,
	}
	closed.Add(1)
	go func() {
		defer closed.Done()
		<-ctx.Done()
		client.Close()
	}()
	return lease
}

func (lease *ownerLease) acquire() bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.retired || lease.context.Err() != nil {
		return false
	}
	lease.active++
	return true
}

func (lease *ownerLease) release() {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.active--
	if lease.retired && lease.active == 0 {
		lease.cancel()
	}
}

func (lease *ownerLease) retire() {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.retired = true
	lease.notifyRetired(errOwnerLeaseRenewed)
	if lease.active == 0 {
		lease.cancel()
	}
}

func (s *Service) acquireOwnerLease() (*ownerLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.options.Now()
	if s.status.Problem == domain.Denied || s.status.Problem == domain.Conflict ||
		!s.ownerExpiry.IsZero() && !s.ownerExpiry.After(now) ||
		s.lease != nil && !s.lease.expires.After(now) {
		return nil, domain.Fail(domain.Denied, "identity owner authority was withdrawn or expired")
	}
	if s.lease == nil || !s.lease.acquire() {
		return nil, domain.Fail(domain.Unavailable, "identity owner connection is not ready")
	}
	return s.lease, nil
}
