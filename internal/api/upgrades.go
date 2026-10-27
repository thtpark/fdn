package api

import (
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (s *Server) upgradeRoutes() {
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
