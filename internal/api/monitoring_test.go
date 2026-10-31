package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMonitoringEnvironmentAndAnnouncementsKeepRawAuthorityAndPrivacy(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	observation := domain.EnvironmentObservation{Version: domain.EnvironmentVersion, ObservedAt: time.Now().Unix(),
		System: domain.SystemEnvironment{OS: "darwin", Architecture: "arm64", GoVersion: "go1.27"},
		CPU:    domain.CPUEnvironment{LogicalCores: 8}, Executables: []domain.FoundationExecutable{}, Problems: []domain.EnvironmentProblem{}}
	path := "/v1/agents/" + f.target.ID + "/environment"
	envelope, status := f.request(t, f.target, http.MethodPost, path, domain.EnvironmentRequest{
		Command: domain.Command{OperationID: "environment-fixture"}, Observation: observation})
	if status != http.StatusOK || !envelope.OK {
		t.Fatal("environment report failed", status, envelope)
	}
	envelope, status = f.request(t, f.delegate, http.MethodGet, path, nil)
	var environment domain.AgentEnvironment
	if err := json.Unmarshal(envelope.Data, &environment); err != nil || status != http.StatusOK ||
		len(environment.Computers) != 1 || environment.Computers[0].BindingID != f.target.ID {
		t.Fatal("public environment projection failed", environment, status, err)
	}
	if _, status := f.request(t, f.delegate, http.MethodPost, path, domain.EnvironmentRequest{
		Command: domain.Command{OperationID: "forged-computer"}, Observation: observation}); status != http.StatusForbidden {
		t.Fatal("delegate impersonated environment reporter", status)
	}
	for _, malformed := range []string{path + "?raw=1", path + "?scope=global", "/v1/announcements/audience?scope=global&scope=project"} {
		if _, status := f.request(t, f.founder, http.MethodGet, malformed, nil); status != http.StatusBadRequest {
			t.Fatal("raw endpoint accepted unsupported selectors", malformed, status)
		}
	}
	if _, status := f.request(t, f.target, http.MethodGet, "/v1/announcements/audience?scope=global", nil); status != http.StatusForbidden {
		t.Fatal("non-delegate acquired global broadcast authority", status)
	}
	envelope, status = f.request(t, f.delegate, http.MethodGet, "/v1/announcements/audience?scope=global", nil)
	var audience domain.AnnouncementAudience
	if err := json.Unmarshal(envelope.Data, &audience); err != nil || status != http.StatusOK || audience.ActorID != f.delegate.ID {
		t.Fatal("current delegate could not preview audience", status, err)
	}
	request := domain.AnnouncementRequest{Command: domain.Command{OperationID: "announcement-fixture"},
		Scope: "global", Kind: "upgrade-request", Title: "Prepare software upgrade", Body: "Review versions and request approval for native replacement."}
	for _, recipient := range audience.Recipients {
		request.RecipientIDs = append(request.RecipientIDs, recipient.AgentID)
	}
	envelope, status = f.request(t, f.delegate, http.MethodPost, "/v1/announcements", request)
	var receipt domain.AnnouncementReceipt
	if err := json.Unmarshal(envelope.Data, &receipt); err != nil || status != http.StatusOK || len(receipt.Deliveries) != len(request.RecipientIDs) {
		t.Fatal("delegate announcement did not fan out exact durable messages", receipt, status, err)
	}
}
