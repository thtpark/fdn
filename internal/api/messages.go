package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/upgrade"
)

type operationRequest struct {
	ctx              context.Context
	URL              *url.URL
	ContentLength    int64
	TransferEncoding []string
	target           string
}

func (r *operationRequest) Context() context.Context { return r.ctx }
func (r *operationRequest) PathValue(name string) string {
	if name == "id" {
		return r.target
	}
	return ""
}

func operationFromHTTP(r *http.Request) *operationRequest {
	return &operationRequest{ctx: r.Context(), URL: r.URL, ContentLength: r.ContentLength,
		TransferEncoding: r.TransferEncoding, target: r.PathValue("id")}
}

type messageOperation struct {
	name         string
	method       string
	pattern      string
	encode       func(any) ([]byte, error)
	resultJSON   func([]byte) (json.RawMessage, error)
	fromJSON     func(json.RawMessage) ([]byte, error)
	handle       func(*operationRequest, domain.Agent, []byte) ([]byte, error)
	inputType    reflect.Type
	queryAllowed map[string]bool
	calls        atomic.Uint64
}

type Health = domain.Health

var catalogOnce sync.Once
var catalog map[string]*messageOperation

func operationName(pattern string) string {
	method, path, _ := strings.Cut(pattern, " ")
	prefix := "query."
	if method == "POST" {
		prefix = "command."
	}
	path = strings.TrimPrefix(path, "/v1/")
	path = strings.TrimPrefix(path, "/")
	path = strings.ReplaceAll(path, "{id}", "by_id")
	return prefix + strings.ReplaceAll(path, "/", ".")
}

func operationQueries(pattern string) map[string]bool {
	allowed := map[string]bool{}
	var names []string
	switch pattern {
	case "GET /v1/notes":
		names = []string{"scope", "target"}
	case "GET /v1/note-context":
		names = []string{"project"}
	case "GET /v1/tasks":
		names = []string{"project", "assignee", "status", "include_archived", "limit", "cursor"}
	case "GET /v1/events":
		names = []string{"project", "task", "limit", "since", "wait", "filter"}
	case "GET /v1/updates":
		names = []string{"limit", "since", "wait", "filter"}
	case "GET /v1/messages":
		names = []string{"cursor", "limit", "urgent"}
	case "GET /v1/message-history":
		names = []string{"cursor", "limit", "folder", "state", "peer"}
	case "GET /v1/messages/{id}/thread-page":
		names = []string{"cursor", "limit"}
	case "GET /v1/attention", "GET /v1/tasks/{id}/entries", "GET /v1/agent-operations":
		names = []string{"cursor", "limit"}
	}
	for _, name := range names {
		allowed[name] = true
	}
	return allowed
}

func encodeOperationInput[Q any](input any) ([]byte, error) {
	if input == nil {
		var zero Q
		return message.MarshalPayload(zero)
	}
	if typed, ok := input.(Q); ok {
		return message.MarshalPayload(typed)
	}
	if typed, ok := input.(*Q); ok {
		return message.MarshalPayload(typed)
	}
	var data []byte
	var err error
	if raw, ok := input.(json.RawMessage); ok {
		data = raw
	} else {
		data, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "command exceeds its input limit")
	}
	var value Q
	if err := config.Decode(data, &value); err != nil {
		return nil, err
	}
	return message.MarshalPayload(value)
}

func operationResultJSON[R any](data []byte) (json.RawMessage, error) {
	var result R
	if err := message.UnmarshalPayload(data, &result); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func operationResultFromJSON[R any](data json.RawMessage) ([]byte, error) {
	var result R
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return message.MarshalPayload(result)
}

func registerQuery[R any](s *Server, pattern string, call func(*operationRequest, domain.Agent) (R, error)) {
	spec := &messageOperation{name: operationName(pattern), method: "GET", pattern: pattern,
		resultJSON: operationResultJSON[R], fromJSON: operationResultFromJSON[R], queryAllowed: operationQueries(pattern)}
	spec.encode = func(input any) ([]byte, error) {
		if input != nil {
			return nil, domain.Fail(domain.Invalid, "query does not accept a command body")
		}
		return nil, nil
	}
	spec.handle = func(r *operationRequest, actor domain.Agent, data []byte) ([]byte, error) {
		if len(data) != 0 {
			return nil, domain.Fail(domain.Invalid, "query does not accept a command body")
		}
		result, err := call(r, actor)
		if err != nil {
			return nil, err
		}
		return message.MarshalPayload(result)
	}
	s.addMessageOperation(spec)
}

func registerCommand[Q any, R any](s *Server, pattern string, call func(*operationRequest, domain.Agent, Q) (R, error)) {
	spec := &messageOperation{name: operationName(pattern), method: "POST", pattern: pattern,
		encode: encodeOperationInput[Q], resultJSON: operationResultJSON[R],
		fromJSON:  operationResultFromJSON[R],
		inputType: reflect.TypeFor[Q](), queryAllowed: operationQueries(pattern)}
	spec.handle = func(r *operationRequest, actor domain.Agent, data []byte) ([]byte, error) {
		if len(data) > domain.MaxBody {
			return nil, domain.Fail(domain.Invalid, "command exceeds its input limit")
		}
		var input Q
		if err := message.UnmarshalPayload(data, &input); err != nil {
			return nil, err
		}
		result, err := call(r, actor, input)
		if err != nil {
			return nil, err
		}
		return message.MarshalPayload(result)
	}
	s.addMessageOperation(spec)
}

func (s *Server) addMessageOperation(spec *messageOperation) {
	if s.operations == nil {
		s.operations = map[string]*messageOperation{}
	}
	if _, exists := s.operations[spec.name]; exists {
		panic("duplicate Foundation message operation: " + spec.name)
	}
	s.operations[spec.name] = spec
}

func messageCatalog() map[string]*messageOperation {
	catalogOnce.Do(func() {
		s := &Server{mux: http.NewServeMux()}
		s.routes()
		s.identityRoutes()
		s.healthRoutes()
		s.agentOperationRoutes()
		s.upgradeRoutes()
		s.updateMessageRoutes()
		registerQuery(s, "GET /local/status", func(*operationRequest, domain.Agent) (LocalStatus, error) {
			return LocalStatus{}, nil
		})
		registerQuery(s, "GET /local/profile", func(*operationRequest, domain.Agent) (config.ConnectionProfile, error) {
			return config.ConnectionProfile{}, nil
		})
		registerQuery(s, "GET /v1/identity/challenge", func(*operationRequest, domain.Agent) (domain.IdentityChallenge, error) {
			return domain.IdentityChallenge{}, nil
		})
		registerCommand(s, "POST /v1/identity/activate", func(*operationRequest, domain.Agent, domain.ActivateIdentityRequest) (domain.IdentityCertificate, error) {
			return domain.IdentityCertificate{}, nil
		})
		registerCommand(s, "POST /v1/identity/recover-founder", func(*operationRequest, domain.Agent, domain.RecoverIdentityRequest) (domain.AgentIdentity, error) {
			return domain.AgentIdentity{}, nil
		})
		catalog = s.operations
	})
	return catalog
}

func resolveOperation(method, path string) (*messageOperation, message.Request, error) {
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return nil, message.Request{}, domain.Fail(domain.Invalid, "invalid operation selector")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, message.Request{}, domain.Fail(domain.Invalid, "malformed query arguments")
	}
	for _, spec := range messageCatalog() {
		if spec.method != method {
			continue
		}
		_, pattern, _ := strings.Cut(spec.pattern, " ")
		expected, actual := strings.Split(pattern, "/"), strings.Split(u.Path, "/")
		if len(expected) != len(actual) {
			continue
		}
		target, matches := "", true
		for i := range expected {
			if expected[i] == "{id}" {
				target = actual[i]
				if target == "" || target == "." || target == ".." || len(target) > 128 {
					matches = false
				}
			} else if expected[i] != actual[i] {
				matches = false
			}
		}
		if !matches {
			continue
		}
		request := message.Request{Operation: spec.name, Target: target, Query: map[string]string{}}
		for name, values := range query {
			if !spec.queryAllowed[name] || len(values) != 1 {
				return nil, message.Request{}, domain.Fail(domain.Invalid, "unsupported or repeated query argument")
			}
			request.Query[name] = values[0]
		}
		return spec, request, nil
	}
	return nil, message.Request{}, domain.Fail(domain.NotFound, "unknown command or query")
}

func validateMessageRequest(request message.Request, spec *messageOperation) (*operationRequest, error) {
	if len(request.Target) > 128 || strings.ContainsAny(request.Target, "/\\\x00\r\n") {
		return nil, domain.Fail(domain.Invalid, "invalid operation target")
	}
	_, pattern, _ := strings.Cut(spec.pattern, " ")
	if strings.Contains(pattern, "{id}") != (request.Target != "") {
		return nil, domain.Fail(domain.Invalid, "operation target does not match its contract")
	}
	query := url.Values{}
	for name, value := range request.Query {
		if !spec.queryAllowed[name] || len(value) > 16384 {
			return nil, domain.Fail(domain.Invalid, "unsupported query argument")
		}
		query.Set(name, value)
	}
	return &operationRequest{URL: &url.URL{RawQuery: query.Encode()}, target: request.Target}, nil
}

func (s *Server) callMessage(ctx context.Context, actor domain.Agent, request message.Request) ([]byte, error) {
	spec, ok := s.operations[request.Operation]
	if !ok {
		return nil, domain.Fail(domain.Denied, "operation is not available on ordinary agent ingress")
	}
	r, err := validateMessageRequest(request, spec)
	if err != nil {
		return nil, err
	}
	r.ctx, r.ContentLength = ctx, int64(len(request.Payload))
	spec.calls.Add(1)
	started := time.Now()
	result, err := spec.handle(r, actor, request.Payload)
	code := "ok"
	if err != nil {
		code = string(domain.AsFault(err).Code)
	}
	fields := []any{"request_id", domain.NewID("request"), "actor_id", actor.ID, "operation", spec.name,
		"code", code, "duration_ms", time.Since(started).Milliseconds()}
	if uid, ok := ingress.UID(ctx); ok {
		fields = append(fields, "peer_uid", uid)
	}
	if err != nil && domain.AsFault(err).Code == domain.Internal {
		s.Log.Error("Foundation message failed", append(fields, "error", err)...)
	} else {
		s.Log.Info("Foundation message", fields...)
	}
	if err != nil {
		return nil, message.PublicError(domain.AsFault(err))
	}
	return result, nil
}

func (s *Server) MessageCounts() map[string]uint64 {
	counts := make(map[string]uint64, len(s.operations))
	for name, operation := range s.operations {
		counts[name] = operation.calls.Load()
	}
	return counts
}

// ServeTrustedMessages is the trusted embedding seam used by business fixtures.
// Native listeners always use ServeMessageConnection and kernel-bound mTLS.
func (s *Server) ServeTrustedMessages(ctx context.Context, conn net.Conn, actorID string) error {
	if s.authentication == nil {
		return domain.Fail(domain.Denied, "trusted message embedding is not configured")
	}
	actor, err := s.Store.Me(ctx, actorID)
	if err != nil {
		return err
	}
	relays := newRelayPeer(s, ctx, actor)
	defer relays.close()
	session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
		if strings.HasPrefix(request.Operation, "relay.") {
			return relays.call(call, request)
		}
		return s.callMessage(call, actor, request)
	})
	if err != nil {
		return err
	}
	defer session.Close()
	<-session.Done()
	return session.Err()
}

func (s *Server) ServeMessageConnection(ctx context.Context, conn net.Conn, purpose string) error {
	ctx = ingress.ConnContext(ctx, conn)
	uid, ok := ingress.UID(ctx)
	if !ok {
		return domain.Fail(domain.Denied, "kernel-bound Unix ingress is required")
	}
	if purpose == "maintenance" {
		if uid != 0 {
			return rejectMessageConnection(ctx, conn, domain.Fail(domain.Denied, "local root operator required"))
		}
		return s.serveMaintenanceMessages(ctx, conn)
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return domain.Fail(domain.Denied, "verified mutual TLS inside pinned SSH is required")
	}
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := tlsConn.HandshakeContext(handshake)
	cancel()
	if err != nil {
		return domain.Fail(domain.Denied, "identity TLS handshake failed")
	}
	state := tlsConn.ConnectionState()
	if state.NegotiatedProtocol != MessageALPN {
		return domain.Fail(domain.Prerequisite, "binary message protocol negotiation is required")
	}
	if purpose == "join" {
		return s.serveJoinMessages(ctx, conn, uid, state)
	}
	actor, identity, owner, err := s.messagePrincipal(ctx, uid, state, "owner")
	if err != nil {
		return rejectMessageConnection(ctx, conn, err)
	}
	ctx = foundation.WithIdentityFence(ctx, identity)
	ctx, cancel = context.WithDeadline(ctx, state.PeerCertificates[0].NotAfter)
	defer cancel()
	relays := newRelayPeer(s, ctx, actor)
	defer relays.close()
	session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
		if strings.HasPrefix(request.Operation, "relay.") {
			return relays.call(call, request)
		}
		return s.callMessage(call, actor, request)
	})
	if err != nil {
		return err
	}
	defer session.Close()
	stop := context.AfterFunc(owner, func() {
		_ = session.Shutdown(domain.Fail(domain.Denied, "identity owner authority was withdrawn"))
	})
	defer stop()
	<-session.Done()
	return session.Err()
}

func rejectMessageConnection(ctx context.Context, conn net.Conn, fault error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	session, err := message.NewSession(ctx, conn, func(context.Context, message.Request) ([]byte, error) {
		return nil, fault
	})
	if err == nil {
		defer session.Close()
		<-session.Done()
	}
	return fault
}

func (s *Server) messagePrincipal(ctx context.Context, uid uint32, state tls.ConnectionState, purpose string) (domain.Agent, domain.CertificateIdentity, context.Context, error) {
	denied := domain.Fail(domain.Denied, "a verified account-bound identity is required")
	if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return domain.Agent{}, domain.CertificateIdentity{}, nil, denied
	}
	cert := state.PeerCertificates[0]
	if time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return domain.Agent{}, domain.CertificateIdentity{}, nil, denied
	}
	identity, err := pki.ParseIdentity(cert)
	if err != nil || identity.Purpose != purpose {
		return domain.Agent{}, domain.CertificateIdentity{}, nil, denied
	}
	actor, err := s.Store.ValidateCertificate(ctx, int64(uid), identity, cert.PublicKey)
	if err != nil {
		return actor, identity, nil, err
	}
	if purpose != "owner" {
		return actor, identity, nil, nil
	}
	owner := s.identity.owner(actor.ID, identity)
	if owner == nil || owner.Err() != nil {
		return actor, identity, nil, denied
	}
	return actor, identity, owner, nil
}

func (s *Server) updateMessageRoutes() {
	read(s, "GET /v1/updates", func(r *operationRequest, actor domain.Agent) (domain.Page[domain.Update], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Update]{}, err
		}
		query := r.URL.Query()
		filter, err := domain.ParseUpdateFilter(query.Get("filter"))
		if err != nil {
			return domain.Page[domain.Update]{}, err
		}
		wait := query.Get("wait")
		if wait != "" && wait != "true" && wait != "false" {
			return domain.Page[domain.Update]{}, domain.Fail(domain.Invalid, "invalid update wait argument")
		}
		if wait == "true" {
			if query.Get("filter") != "" {
				return s.Store.WaitScopedUpdates(r.Context(), actor.ID, query.Get("since"), limit, filter)
			}
			return s.Store.WaitUpdates(r.Context(), actor.ID, query.Get("since"), limit)
		}
		return s.Store.ScopedUpdates(r.Context(), actor.ID, query.Get("since"), limit, filter)
	})
}

func (s *Server) serveMaintenanceMessages(ctx context.Context, conn net.Conn) error {
	session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
		if request.Operation == "query.maintenance.requests" {
			if request.Target != "" || len(request.Query) != 0 || len(request.Payload) != 0 {
				return nil, domain.Fail(domain.Denied, "Foundation maintenance inbox accepts no caller scope or body")
			}
			items, err := s.Store.SystemMessageRequests(call)
			if err != nil {
				return nil, err
			}
			return message.MarshalPayload(items)
		}
		if request.Operation == "command.maintenance.request-result" {
			if request.Target != "" || len(request.Query) != 0 {
				return nil, domain.Fail(domain.Denied, "unknown Foundation maintenance result selector")
			}
			var input domain.MessageWorkerCommand
			if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
				return nil, err
			}
			if input.MessageID == "" || input.Report == nil {
				return nil, domain.Fail(domain.Invalid, "maintenance result requires its exact request and decision")
			}
			result, err := s.Store.ReportSystemMessageAction(call, input.MessageID, *input.Report)
			if err != nil {
				return nil, err
			}
			return message.MarshalPayload(result)
		}
		leaseSpec, _, err := resolveOperation("POST", "/v1/upgrades/lease")
		if err != nil {
			return nil, err
		}
		if request.Operation == leaseSpec.name {
			if request.Target != "" || len(request.Query) != 0 {
				return nil, domain.Fail(domain.Denied, "unknown private upgrade lease selector")
			}
			var input domain.UpgradeLeaseRequest
			if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
				return nil, err
			}
			lease, err := s.Store.LeaseUpgrade(call, "", input, true, func(lease domain.UpgradeLease) (string, error) {
				return upgrade.SignLease(lease, s.identity.key)
			})
			if err != nil {
				return nil, err
			}
			return message.MarshalPayload(lease)
		}
		spec, _, err := resolveOperation("POST", "/v1/identity/recover-founder")
		if err != nil {
			return nil, err
		}
		if request.Operation != spec.name || request.Target != "" || len(request.Query) != 0 {
			return nil, domain.Fail(domain.Denied, "unknown private maintenance command")
		}
		var input domain.RecoverIdentityRequest
		if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
			return nil, err
		}
		identity, err := s.Store.RecoverFounderOffline(call, input)
		if err == nil {
			identity, err = s.provisionIdentity(call, identity)
		}
		if err != nil {
			return nil, err
		}
		return message.MarshalPayload(identity)
	})
	if err != nil {
		return err
	}
	defer session.Close()
	<-session.Done()
	return session.Err()
}

func ValidateOrdinaryMessage(request message.Request) error {
	switch request.Operation {
	case "query.local.status", "query.local.profile", "query.identity.challenge",
		"command.identity.activate", "command.identity.recover-founder":
		return domain.Fail(domain.Denied, "private identity and maintenance operations are not runner capabilities")
	case "relay.open", "relay.read", "relay.write", "relay.close":
		if len(request.Target) > 128 || len(request.Query) != 0 || len(request.Payload) > 64<<10 {
			return domain.Fail(domain.Invalid, "invalid bounded relay message")
		}
		return nil
	}
	_, err := DecodeMessageInput(request)
	if err != nil && domain.AsFault(err).Code == domain.NotFound {
		return domain.Fail(domain.Denied, "operation is not an ordinary runner capability")
	}
	return err
}

func MessageSelector(request message.Request) (string, string, error) {
	spec, ok := messageCatalog()[request.Operation]
	if !ok {
		return "", "", domain.Fail(domain.NotFound, "unknown message operation")
	}
	r, err := validateMessageRequest(request, spec)
	if err != nil {
		return "", "", err
	}
	_, path, _ := strings.Cut(spec.pattern, " ")
	path = strings.ReplaceAll(path, "{id}", request.Target)
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	return spec.method, path, nil
}

func EncodeMessageResult(request message.Request, result json.RawMessage) ([]byte, error) {
	spec, ok := messageCatalog()[request.Operation]
	if !ok {
		return nil, domain.Fail(domain.NotFound, "unknown message operation")
	}
	return spec.fromJSON(result)
}
