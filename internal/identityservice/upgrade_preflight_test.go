package identityservice

import (
	"net/url"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func TestUpgradePreflightProxyKeepsExactMethodsQueriesAndMaintenanceBoundary(t *testing.T) {
	for _, item := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/upgrades/preflight", true},
		{"GET", "/v1/upgrades/preflight/preflight_fixture", true},
		{"GET", "/v1/upgrades/preflight-requests", true},
		{"POST", "/v1/upgrades/preflight/preflight_fixture/result", true},
		{"GET", "/v1/upgrades/preflight-requests?target=agent_other", false},
		{"POST", "/v1/upgrades/preflight?role=foundation", false},
		{"GET", "/v1/upgrades/preflight/preflight_fixture?owner=agent_other", false},
		{"POST", "/v1/upgrades/preflight/preflight_fixture/result?generation=2", false},
		{"GET", "/v1/upgrades/preflight/preflight_fixture/result", false},
		{"POST", "/v1/upgrades/preflight-requests", false},
		{"POST", "/v1/upgrades/preflight/preflight_fixture/result/other", false},
		{"GET", "/v1/maintenance/upgrade-preflights", false},
		{"POST", "/v1/maintenance/upgrade-preflight-result", false},
	} {
		parsed, err := url.Parse(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if actual := ordinaryPath(item.method, parsed); actual != item.allowed {
			t.Fatal("preflight proxy changed its narrow boundary", item.method, item.path, actual)
		}
	}
	for _, operation := range []string{"query.maintenance.upgrade-preflights", "command.maintenance.upgrade-preflight-result"} {
		if err := api.ValidateOrdinaryMessage(message.Request{Operation: operation}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("ordinary native proxy exposed Foundation root maintenance", operation, err)
		}
	}
}
