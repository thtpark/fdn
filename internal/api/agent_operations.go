package api

import (
	"net/http"
	"net/url"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Server) agentOperationRoutes() {
	body(s, "POST /v1/agents/{id}/diagnostics", false, func(r *http.Request, a domain.Agent, req domain.DiagnosticRefreshRequest) (domain.AgentOperation, error) {
		if r.URL.RawQuery != "" {
			return domain.AgentOperation{}, domain.Fail(domain.Invalid, "agent operation requests do not accept query parameters")
		}
		return s.Store.RequestDiagnosticRefresh(r.Context(), a.ID, r.PathValue("id"), req)
	})
	body(s, "POST /v1/agents/{id}/repairs", false, func(r *http.Request, a domain.Agent, req domain.SessionRecoveryRequest) (domain.AgentOperation, error) {
		if r.URL.RawQuery != "" {
			return domain.AgentOperation{}, domain.Fail(domain.Invalid, "agent operation requests do not accept query parameters")
		}
		return s.Store.RequestSessionRecovery(r.Context(), a.ID, r.PathValue("id"), req)
	})
	read(s, "GET /v1/agent-operations", func(r *http.Request, a domain.Agent) (domain.Page[domain.AgentOperation], error) {
		if err := agentOperationRead(r, true); err != nil {
			return domain.Page[domain.AgentOperation]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.AgentOperation]{}, err
		}
		return s.Store.ListAgentOperations(r.Context(), a.ID, r.URL.Query().Get("cursor"), limit)
	})
	read(s, "GET /v1/agent-operations/{id}", func(r *http.Request, a domain.Agent) (domain.AgentOperation, error) {
		if err := agentOperationRead(r, false); err != nil {
			return domain.AgentOperation{}, err
		}
		return s.Store.GetAgentOperation(r.Context(), a.ID, r.PathValue("id"))
	})
	body(s, "POST /v1/agent-operations/{id}/result", false, func(r *http.Request, a domain.Agent, req domain.AgentOperationReport) (domain.AgentOperation, error) {
		if r.URL.RawQuery != "" {
			return domain.AgentOperation{}, domain.Fail(domain.Invalid, "agent operation results do not accept query parameters")
		}
		return s.Store.ReportAgentOperation(r.Context(), a.ID, r.PathValue("id"), req)
	})
}

func agentOperationRead(r *http.Request, pagination bool) error {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return domain.Fail(domain.Invalid, "agent operation reads do not accept a body")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.Fail(domain.Invalid, "agent operation query is malformed")
	}
	for key, values := range query {
		if !pagination || (key != "cursor" && key != "limit") || len(values) != 1 || values[0] == "" {
			return domain.Fail(domain.Invalid, "agent operation reads accept only one nonempty cursor and limit")
		}
	}
	return nil
}
