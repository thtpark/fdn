package api

import (
	"context"
	"net/http"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type healthMonitor struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *Server) startHealthMonitor(interval, timeout time.Duration) *healthMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	monitor := &healthMonitor{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(monitor.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			check, stop := context.WithTimeout(ctx, timeout)
			err := s.Store.SweepHealth(check)
			stop()
			if err != nil && ctx.Err() == nil {
				s.Log.Warn("health observation sweep failed", "code", domain.AsFault(err).Code)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return monitor
}

func (m *healthMonitor) close() {
	if m != nil {
		m.cancel()
		<-m.done
	}
}

func (s *Server) healthRoutes() {
	read(s, "GET /v1/agents/{id}/diagnostics", func(r *http.Request, a domain.Agent) (domain.AgentDiagnostics, error) {
		return s.Store.AgentDiagnostics(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/messages/{id}/diagnostics", func(r *http.Request, a domain.Agent) (domain.MessageDiagnostics, error) {
		return s.Store.MessageDiagnostics(r.Context(), a.ID, r.PathValue("id"))
	})
}
