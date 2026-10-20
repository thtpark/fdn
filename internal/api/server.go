package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/relay"
)

type Envelope struct {
	OK          bool            `json:"ok"`
	Data        json.RawMessage `json:"data,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
	Warnings    []string        `json:"warnings"`
	Error       *domain.Fault   `json:"error,omitempty"`
}

type Server struct {
	Store *foundation.Store
	Log   *slog.Logger
	Relay *relay.Broker
	mux   *http.ServeMux
}

func New(store *foundation.Store, log *slog.Logger) (*Server, error) {
	if err := store.RetireSessions(context.Background()); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{Store: store, Log: log, Relay: relay.New(store), mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

func (s *Server) Close() error { return s.Relay.Close() }

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "TLS is required"))
			return
		}
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "https://"+r.Host {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "cross-origin requests are not accepted"))
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(r *http.Request) (domain.Agent, error) {
	header := r.Header.Values("Authorization")
	if len(header) != 1 {
		return domain.Agent{}, domain.Fail(domain.Denied, "Bearer authentication required")
	}
	token, ok := strings.CutPrefix(header[0], "Bearer ")
	if !ok {
		return domain.Agent{}, domain.Fail(domain.Denied, "Bearer authentication required")
	}
	return s.Store.Authenticate(r.Context(), token)
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, value any, op string, err error) {
	w.Header().Set("Content-Type", "application/json")
	envelope := Envelope{OK: err == nil, OperationID: op, Warnings: []string{}}
	status := http.StatusOK
	if err != nil {
		envelope.Error = domain.AsFault(err)
		status = domain.HTTPStatus(err)
		if envelope.Error.Code == domain.Internal {
			s.Log.Error("API operation failed", "path", r.URL.EscapedPath(), "error", err)
		}
	} else {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			s.respond(w, r, nil, op, marshalErr)
			return
		}
		envelope.Data = data
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		s.Log.Warn("API response delivery failed", "path", r.URL.EscapedPath(), "error", err)
	}
}

func readBody(w http.ResponseWriter, r *http.Request, value any) (string, error) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return "", domain.Fail(domain.Invalid, "Content-Type application/json required")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxBody))
	if err != nil || !utf8.Valid(data) {
		return "", domain.Fail(domain.Invalid, "body must be bounded UTF-8 JSON")
	}
	if err := config.Decode(data, value); err != nil {
		return "", err
	}
	var meta domain.Command
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", domain.Fail(domain.Invalid, "invalid command metadata")
	}
	return meta.OperationID, nil
}

func body[Q any, R any](s *Server, pattern string, public bool, call func(*http.Request, domain.Agent, Q) (R, error)) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var actor domain.Agent
		var err error
		if !public {
			actor, err = s.authenticate(r)
			if err != nil {
				s.respond(w, r, nil, "", err)
				return
			}
		}
		var req Q
		op, err := readBody(w, r, &req)
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		result, err := call(r, actor, req)
		s.respond(w, r, result, op, err)
	})
}

func read[R any](s *Server, pattern string, call func(*http.Request, domain.Agent) (R, error)) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		actor, err := s.authenticate(r)
		if err != nil {
			s.respond(w, r, nil, "", err)
			return
		}
		result, err := call(r, actor)
		s.respond(w, r, result, "", err)
	})
}

func queryLimit(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 200 {
		return 0, domain.Fail(domain.Invalid, "limit must be 1-200")
	}
	return n, nil
}

func (s *Server) routes() {
	body(s, "POST /v1/projects/{id}/change", false, func(r *http.Request, a domain.Agent, q domain.ProjectChange) (domain.Project, error) {
		return s.Store.ChangeProject(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/ownership", func(r *http.Request, a domain.Agent) ([]domain.Task, error) {
		return s.Store.Ownership(r.Context(), a.ID)
	})
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.respond(w, r, map[string]any{"api_version": domain.APIVersion, "schema_version": domain.SchemaVersion, "version": domain.Version}, "", s.Store.Ping(r.Context()))
	})
	body(s, "POST /v1/enroll", true, func(r *http.Request, _ domain.Agent, q domain.EnrollRequest) (domain.Agent, error) {
		return s.Store.Enroll(r.Context(), q)
	})
	read(s, "GET /v1/me", func(r *http.Request, a domain.Agent) (domain.Agent, error) { return s.Store.Me(r.Context(), a.ID) })
	read(s, "GET /v1/agents", func(r *http.Request, a domain.Agent) ([]domain.Agent, error) {
		return s.Store.Agents(r.Context(), a.ID)
	})
	body(s, "POST /v1/heartbeat", false, func(r *http.Request, a domain.Agent, _ domain.Command) (domain.Agent, error) {
		return s.Store.Heartbeat(r.Context(), a.ID)
	})
	body(s, "POST /v1/invitations", false, func(r *http.Request, a domain.Agent, q domain.InvitationRequest) (domain.Invitation, error) {
		return s.Store.Invite(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/agents/{id}/revoke", false, func(r *http.Request, a domain.Agent, q domain.AgentRequest) (domain.Agent, error) {
		return s.Store.Revoke(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/projects", func(r *http.Request, a domain.Agent) ([]domain.Project, error) {
		return s.Store.Projects(r.Context(), a.ID)
	})
	read(s, "GET /v1/projects/{id}", func(r *http.Request, a domain.Agent) (domain.Project, error) {
		return s.Store.Project(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/projects", false, func(r *http.Request, a domain.Agent, q domain.ProjectRequest) (domain.Project, error) {
		return s.Store.CreateProject(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/project-creation-grants", false, func(r *http.Request, a domain.Agent, q domain.GrantRequest) (map[string]bool, error) {
		return s.Store.GrantCreation(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/projects/{id}/delegates", false, func(r *http.Request, a domain.Agent, q domain.GrantRequest) (domain.Project, error) {
		return s.Store.Delegate(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/tasks", func(r *http.Request, a domain.Agent) (domain.Page[domain.Task], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Task]{}, err
		}
		archived := false
		if raw := r.URL.Query().Get("include_archived"); raw != "" {
			archived, err = strconv.ParseBool(raw)
			if err != nil {
				return domain.Page[domain.Task]{}, domain.Fail(domain.Invalid, "invalid include_archived boolean")
			}
		}
		q := r.URL.Query()
		return s.Store.Tasks(r.Context(), a.ID, domain.ListQuery{ProjectID: q.Get("project"), AssigneeID: q.Get("assignee"), Status: domain.Status(q.Get("status")), IncludeArchived: archived, Limit: limit, Cursor: q.Get("cursor")})
	})
	read(s, "GET /v1/tasks/{id}", func(r *http.Request, a domain.Agent) (domain.Task, error) {
		return s.Store.Task(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/tasks", false, func(r *http.Request, a domain.Agent, q domain.TaskRequest) (domain.Task, error) {
		return s.Store.CreateTask(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/tasks/{id}/change", false, func(r *http.Request, a domain.Agent, q domain.TaskChange) (domain.Task, error) {
		return s.Store.ChangeTask(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/history/{id}", func(r *http.Request, a domain.Agent) ([]domain.History, error) {
		return s.Store.Histories(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/tasks/{id}/entries", func(r *http.Request, a domain.Agent) (domain.Page[domain.Entry], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Entry]{}, err
		}
		return s.Store.Entries(r.Context(), a.ID, r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	})
	body(s, "POST /v1/tasks/{id}/entries", false, func(r *http.Request, a domain.Agent, q domain.EntryRequest) (domain.Entry, error) {
		return s.Store.CreateEntry(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/entries/{id}", false, func(r *http.Request, a domain.Agent, q domain.EntryRequest) (domain.Entry, error) {
		return s.Store.EditEntry(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/attention", func(r *http.Request, a domain.Agent) (domain.Page[domain.Attention], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Attention]{}, err
		}
		return s.Store.Attention(r.Context(), a.ID, r.URL.Query().Get("cursor"), limit)
	})
	body(s, "POST /v1/attention/{id}", false, func(r *http.Request, a domain.Agent, q domain.AttentionRequest) (domain.Attention, error) {
		return s.Store.AttentionReceipt(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/events", func(r *http.Request, a domain.Agent) (domain.Page[domain.Event], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Event]{}, err
		}
		q := r.URL.Query()
		return s.Store.Events(r.Context(), a.ID, q.Get("project"), q.Get("task"), q.Get("since"), limit)
	})
	body(s, "POST /v1/tasks/{id}/claim", false, func(r *http.Request, a domain.Agent, q domain.ClaimRequest) (domain.Run, error) {
		return s.Store.Claim(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/tasks/{id}/report", false, func(r *http.Request, a domain.Agent, q domain.ReportRequest) (domain.ReportOutcome, error) {
		return s.Store.Report(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/tasks/{id}/runs", func(r *http.Request, a domain.Agent) ([]domain.Run, error) {
		return s.Store.Runs(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/messages", false, func(r *http.Request, a domain.Agent, q domain.MessageRequest) (domain.Message, error) {
		return s.Store.SendMessage(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/messages", func(r *http.Request, a domain.Agent) (domain.Page[domain.Message], error) {
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Message]{}, err
		}
		return s.Store.Inbox(r.Context(), a.ID, r.URL.Query().Get("cursor"), limit, r.URL.Query().Get("urgent") == "true")
	})
	read(s, "GET /v1/messages/{id}", func(r *http.Request, a domain.Agent) (domain.Message, error) {
		return s.Store.Message(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/messages/{id}/receipt", false, func(r *http.Request, a domain.Agent, q domain.MessageReceipt) (domain.Message, error) {
		return s.Store.ReceiveMessage(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/sessions", false, func(r *http.Request, a domain.Agent, q domain.SessionRequest) (domain.Session, error) {
		return s.Store.RequestSession(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/sessions", func(r *http.Request, a domain.Agent) ([]domain.Session, error) {
		return s.Store.Sessions(r.Context(), a.ID)
	})
	read(s, "GET /v1/sessions/{id}", func(r *http.Request, a domain.Agent) (domain.Session, error) {
		return s.Store.Session(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/sessions/{id}", false, func(r *http.Request, a domain.Agent, q domain.SessionChange) (domain.Session, error) {
		return s.Store.ChangeSession(r.Context(), a.ID, r.PathValue("id"), q)
	})
	s.mux.HandleFunc("GET /v1/sessions/{id}/relay/{side}", func(w http.ResponseWriter, r *http.Request) {
		a, err := s.authenticate(r)
		if err != nil {
			s.respond(w, r, nil, "", err)
			return
		}
		if _, err := s.Store.SessionAccess(r.Context(), a.ID, r.PathValue("id"), r.PathValue("side")); err != nil {
			s.respond(w, r, nil, "", err)
			return
		}
		s.Relay.Serve(w, r, a.ID, r.PathValue("id"), r.PathValue("side"))
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.respond(w, r, nil, "", domain.Fail(domain.NotFound, "unknown API route or method"))
	})
}

func GracefulError(err error) bool { return errors.Is(err, http.ErrServerClosed) }
