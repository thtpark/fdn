package api

import (
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (s *Server) upgradeRoutes() {
	body(s, "POST /v1/upgrades/preflight", false, func(r *operationRequest, a domain.Agent, req domain.UpgradePreflightRequest) (domain.UpgradePreflight, error) {
		if r.URL.RawQuery != "" {
			return domain.UpgradePreflight{}, domain.Fail(domain.Invalid, "native preflight does not accept query parameters")
		}
		return s.Store.RequestUpgradePreflight(r.Context(), a.ID, req)
	})
	read(s, "GET /v1/upgrades/preflight/{id}", func(r *operationRequest, a domain.Agent) (domain.UpgradePreflight, error) {
		if err := agentOperationRead(r, true); err != nil {
			return domain.UpgradePreflight{}, err
		}
		return s.Store.UpgradePreflight(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/upgrades/preflight-requests", func(r *operationRequest, a domain.Agent) ([]domain.UpgradePreflight, error) {
		if err := agentOperationRead(r, false); err != nil {
			return nil, err
		}
		return s.Store.NativeUpgradePreflights(r.Context(), a.ID)
	})
	body(s, "POST /v1/upgrades/preflight/{id}/result", false, func(r *operationRequest, a domain.Agent, req domain.UpgradePreflightReport) (domain.UpgradePreflight, error) {
		if r.URL.RawQuery != "" || req.ID != r.PathValue("id") {
			return domain.UpgradePreflight{}, domain.Fail(domain.Invalid, "native preflight result requires its exact request ID without query overrides")
		}
		return s.Store.ReportUpgradePreflight(r.Context(), a.ID, req)
	})
	body(s, "POST /v1/upgrades/maintenance", false, func(r *operationRequest, a domain.Agent, req domain.UpgradeMaintenanceRequest) (domain.UpgradeMaintenance, error) {
		if r.URL.RawQuery != "" {
			return domain.UpgradeMaintenance{}, domain.Fail(domain.Invalid, "temporary upgrade maintenance does not accept query parameters")
		}
		return s.Store.PauseUpgradeMaintenance(r.Context(), a.ID, req)
	})
	read(s, "GET /v1/message-requests", func(r *operationRequest, a domain.Agent) ([]domain.Message, error) {
		return s.Store.NativeMessageRequests(r.Context(), a.ID)
	})
	read(s, "GET /v1/maintenance/requests", func(r *operationRequest, a domain.Agent) ([]domain.Message, error) {
		return nil, domain.Fail(domain.Denied, "Foundation maintenance inbox is available only through the root-private native handler")
	})
	body(s, "POST /v1/maintenance/request-result", false, func(r *operationRequest, a domain.Agent, req domain.MessageWorkerCommand) (domain.Message, error) {
		return domain.Message{}, domain.Fail(domain.Denied, "Foundation maintenance results require the root-private native handler")
	})
	read(s, "GET /v1/upgrades/context", func(r *operationRequest, a domain.Agent) (domain.UpgradeContext, error) {
		if err := agentOperationRead(r, false); err != nil {
			return domain.UpgradeContext{}, err
		}
		return s.Store.UpgradeContext(r.Context(), a.ID)
	})
	body(s, "POST /v1/upgrades/authorize", false, func(r *operationRequest, a domain.Agent, req domain.UpgradeAuthorizationRequest) (domain.UpgradePermit, error) {
		if r.URL.RawQuery != "" {
			return domain.UpgradePermit{}, domain.Fail(domain.Invalid, "native upgrade authorization does not accept query parameters")
		}
		return s.Store.AuthorizeUpgrade(r.Context(), a.ID, req, func(p domain.UpgradePermit) (string, error) {
			return upgrade.SignPermit(p, s.identity.key)
		})
	})
	body(s, "POST /v1/upgrades/lease", false, func(r *operationRequest, a domain.Agent, req domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		if r.URL.RawQuery != "" {
			return domain.UpgradeLease{}, domain.Fail(domain.Invalid, "native upgrade leases do not accept queries")
		}
		return s.Store.LeaseUpgrade(r.Context(), a.ID, req, false, func(lease domain.UpgradeLease) (string, error) {
			return upgrade.SignLease(lease, s.identity.key)
		})
	})
}
