package identityservice

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

type peerContextKey struct{}

var ordinaryRoutes = map[string][]string{
	http.MethodGet: {
		"/healthz", "/v1/me", "/v1/identity/status", "/v1/agents", "/v1/agents/*", "/v1/agents/*/screens", "/v1/agents/*/diagnostics", "/v1/ownership", "/v1/projects", "/v1/projects/*",
		"/v1/tasks", "/v1/tasks/*", "/v1/tasks/*/entries", "/v1/tasks/*/runs", "/v1/history/*", "/v1/attention", "/v1/events",
		"/v1/messages", "/v1/messages/*", "/v1/messages/*/diagnostics", "/v1/messages/*/thread", "/v1/sessions", "/v1/sessions/*", "/v1/sessions/*/relay/requester", "/v1/sessions/*/relay/target",
		"/v1/assistance", "/v1/assistance/*", "/v1/agent-operations", "/v1/agent-operations/*", "/v1/upgrades/context", "/v1/message-requests",
	},
	http.MethodPost: {
		"/v1/identity/register", "/v1/identity/*/recover",
		"/v1/heartbeat", "/v1/agents/*/screens", "/v1/agents/*/mode", "/v1/agents/*/revoke", "/v1/agents/*/authentication", "/v1/agents/*/diagnostics", "/v1/agents/*/repairs", "/v1/agents/*/status", "/v1/agent-operations/*/result", "/v1/projects", "/v1/projects/*/change", "/v1/project-creation-grants", "/v1/projects/*/delegates",
		"/v1/tasks", "/v1/tasks/*/change", "/v1/tasks/*/entries", "/v1/tasks/*/claim", "/v1/tasks/*/report", "/v1/entries/*", "/v1/attention/*",
		"/v1/messages", "/v1/messages/*/receipt", "/v1/messages/*/read", "/v1/messages/*/request", "/v1/sessions", "/v1/sessions/*", "/v1/assistance", "/v1/assistance/requests", "/v1/assistance/*/revoke", "/v1/upgrades/authorize", "/v1/upgrades/lease",
	},
}

func ordinaryPath(method string, u *url.URL) bool {
	if u == nil || u.IsAbs() || u.Host != "" || u.Opaque != "" || u.User != nil || u.Fragment != "" ||
		strings.ContainsAny(u.EscapedPath(), "%\\\x00\r\n") || strings.Contains(u.Path, "//") || len(u.RawQuery) > 16384 {
		return false
	}
	parts := strings.Split(u.Path, "/")
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
				return false
			}
		}
	}
	for _, pattern := range ordinaryRoutes[method] {
		expected := strings.Split(pattern, "/")
		if len(parts) != len(expected) {
			continue
		}
		match := true
		for i := range parts {
			if expected[i] != "*" && expected[i] != parts[i] {
				match = false
				break
			}
		}
		if match {
			switch pattern {
			case "/v1/agent-operations":
				query, err := url.ParseQuery(u.RawQuery)
				if err != nil {
					return false
				}
				for name, values := range query {
					if (name != "limit" && name != "cursor") || len(values) != 1 {
						return false
					}
				}
			case "/v1/agent-operations/*", "/v1/agent-operations/*/result", "/v1/agents/*/diagnostics", "/v1/agents/*/repairs", "/v1/messages/*/read", "/v1/messages/*/thread", "/v1/messages/*/request", "/v1/agents/*/status", "/v1/upgrades/context", "/v1/upgrades/authorize", "/v1/upgrades/lease", "/v1/message-requests":
				return u.RawQuery == ""
			}
			return true
		}
	}
	return false
}

func localResponse(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
	envelope := api.Envelope{OK: err == nil, Warnings: []string{}}
	status := http.StatusOK
	if err != nil {
		envelope.Error = domain.AsFault(err)
		status = domain.HTTPStatus(err)
	} else {
		envelope.Data, err = json.Marshal(value)
		if err != nil {
			envelope.OK, envelope.Error, status = false, domain.AsFault(err), http.StatusInternalServerError
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

func (s *Service) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := r.Context().Value(peerContextKey{}).(Peer)
		if !ok || peer.UID != s.config.WorkerUID || peer.GID != s.config.WorkerGID {
			localResponse(w, nil, domain.Fail(domain.Denied, "local identity IPC peer is not the configured tool-runner"))
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/local/status" && r.URL.RawQuery == "" && r.URL.RawPath == "" && !r.URL.IsAbs() {
			localResponse(w, s.Status(), nil)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/local/profile" && r.URL.RawQuery == "" && r.URL.RawPath == "" && !r.URL.IsAbs() {
			s.mu.RLock()
			profile := s.profile
			s.mu.RUnlock()
			localResponse(w, profile, nil)
			return
		}
		if !ordinaryPath(r.Method, r.URL) {
			localResponse(w, nil, domain.Fail(domain.Denied, "local worker proxy denies identity transport, certificate and service-control routes"))
			return
		}
		if r.ContentLength > domain.MaxBody {
			localResponse(w, nil, domain.Fail(domain.Invalid, "local request exceeds the body limit"))
			return
		}
		upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if upgrade && (r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/relay/")) {
			localResponse(w, nil, domain.Fail(domain.Denied, "websocket upgrades are only available on ordinary session relays"))
			return
		}
		s.mu.RLock()
		lease, problem, ownerExpiry := s.lease, s.status.Problem, s.ownerExpiry
		s.mu.RUnlock()
		if problem == domain.Denied || problem == domain.Conflict || (!ownerExpiry.IsZero() && !ownerExpiry.After(s.options.Now())) {
			localResponse(w, nil, domain.Fail(domain.Denied, "identity owner authority was revoked, banned or expired; execution must stop"))
			return
		}
		if lease != nil && !lease.expires.After(s.options.Now()) {
			localResponse(w, nil, domain.Fail(domain.Denied, "identity owner certificate has expired; execution must stop"))
			return
		}
		if lease == nil || lease.context.Err() != nil {
			localResponse(w, nil, domain.Fail(domain.Unavailable, "identity owner connection is not ready"))
			return
		}
		base, err := url.Parse(lease.client.BaseURL)
		if err != nil {
			localResponse(w, nil, domain.Fail(domain.Unavailable, "identity owner transport is unavailable"))
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(lease.context, cancel)
		defer stop()
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, domain.MaxBody)
		proxy := &httputil.ReverseProxy{
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(base)
				request.Out.Host = base.Host
				request.Out.Trailer = nil
				request.Out.Header = make(http.Header)
				for _, name := range []string{"Accept", "Content-Type"} {
					for _, value := range request.In.Header.Values(name) {
						request.Out.Header.Add(name, value)
					}
				}
				if upgrade {
					request.Out.Header.Set("Connection", "Upgrade")
					request.Out.Header.Set("Upgrade", "websocket")
					for _, name := range []string{"Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
						for _, value := range request.In.Header.Values(name) {
							request.Out.Header.Add(name, value)
						}
					}
				}
			},
			Transport: leaseTransport{transport: lease.client.HTTP.Transport, context: lease.context},
			ErrorLog:  log.New(io.Discard, "", 0),
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				code := domain.Unavailable
				if s.Status().Problem == domain.Denied {
					code = domain.Denied
				}
				localResponse(w, nil, domain.Fail(code, "Foundation request failed; reconcile operation_id before retrying writes"))
			},
			ModifyResponse: func(response *http.Response) error {
				header := make(http.Header)
				for _, name := range []string{"Content-Type", "Content-Length", "X-Fdn-API-Version", "Cache-Control", "X-Content-Type-Options", "Connection", "Upgrade", "Sec-WebSocket-Accept", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
					for _, value := range response.Header.Values(name) {
						header.Add(name, value)
					}
				}
				response.Header = header
				return nil
			},
		}
		proxy.ServeHTTP(w, r)
	})
}

type leaseTransport struct {
	transport http.RoundTripper
	context   context.Context
}

func (t leaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.transport.RoundTrip(request)
	if err != nil || response.Body == nil {
		return response, err
	}
	body := response.Body
	stop := context.AfterFunc(t.context, func() { _ = body.Close() })
	if duplex, ok := body.(io.ReadWriteCloser); ok {
		response.Body = &leaseDuplexBody{ReadWriteCloser: duplex, stop: stop}
	} else {
		response.Body = &leaseBody{ReadCloser: body, stop: stop}
	}
	return response, nil
}

type leaseBody struct {
	io.ReadCloser
	stop func() bool
}

func (b *leaseBody) Close() error {
	b.stop()
	return b.ReadCloser.Close()
}

type leaseDuplexBody struct {
	io.ReadWriteCloser
	stop func() bool
}

func (b *leaseDuplexBody) Close() error {
	b.stop()
	return b.ReadWriteCloser.Close()
}
