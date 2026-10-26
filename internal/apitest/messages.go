// Package apitest adapts existing JSON business fixtures to the binary wire.
// It is not imported by native binaries or used as a transport fallback.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/transport"
)

type Server struct {
	Handler http.Handler
	mu      sync.Mutex
	cancel  context.CancelFunc
	closed  bool
}

func (s *Server) Serve(listener net.Listener) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		cancel()
	}
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()
	return transport.ServeConnections(ctx, listener, func(ctx context.Context, conn net.Conn) error {
		session, err := message.NewSession(ctx, conn, s.call)
		if err != nil {
			return err
		}
		defer session.Close()
		<-session.Done()
		return session.Err()
	}, func(err error) {
		slog.Warn("binary business fixture connection ended", "code", domain.AsFault(err).Code)
	})
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Server) call(ctx context.Context, request message.Request) ([]byte, error) {
	method, selector, err := api.MessageSelector(request)
	if err != nil {
		return nil, err
	}
	input, err := api.DecodeMessageInput(request)
	if err != nil {
		return nil, err
	}
	var body []byte
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	r := httptest.NewRequest(method, "https://identity.local"+selector, bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Accept", "application/json")
	if input != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	s.Handler.ServeHTTP(response, r)
	if response.Header().Get("X-Fdn-API-Version") != strconv.Itoa(domain.APIVersion) {
		return nil, domain.Fail(domain.Prerequisite, "incompatible Foundation API version")
	}
	var envelope api.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		return nil, domain.Fail(domain.Unavailable, "invalid fixture response")
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return nil, domain.Fail(domain.Internal, "failed fixture omitted its error")
		}
		return nil, message.PublicError(envelope.Error)
	}
	if response.Code != http.StatusOK {
		return nil, domain.Fail(domain.Unavailable, "unexpected fixture response status")
	}
	return api.EncodeMessageResult(request, envelope.Data)
}
