package api

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

func apiPreflightRequest(t *testing.T, f *authenticationAPIFixture, role string) domain.UpgradePreflightRequest {
	t.Helper()
	authority, err := f.store.UpgradeContext(context.Background(), f.founder.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := f.target.ID
	if role == "foundation" {
		target = authority.InstanceID
	}
	return domain.UpgradePreflightRequest{Command: domain.Command{OperationID: domain.NewID("op")},
		InstanceID: authority.InstanceID, TargetID: target, Role: role, Platform: "linux-amd64",
		Native: domain.UpgradeNativeRequest{Action: "inspect", ID: domain.NewID("native")}}
}

func apiPreflightReply(record domain.UpgradePreflight) domain.UpgradePreflightReport {
	return domain.UpgradePreflightReport{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: record.Revision}, ID: record.ID,
		Result: domain.UpgradeNativeReply{State: "observed", Platform: record.Request.Platform,
			Version: "0.3.2", Commit: strings.Repeat("a", 40), Challenge: &domain.UpgradeChallenge{
				InstanceID: record.Request.InstanceID, TargetID: record.Request.TargetID,
				Role: record.Request.Role, Generation: record.TargetGeneration, CurrentVersion: "0.3.2",
				CurrentCommit: strings.Repeat("a", 40), PolicySHA256: strings.Repeat("b", 64),
			}},
	}
}

func TestUpgradePreflightNativeAPIIsFounderOnlyTargetOwnedAndNotInstallAuthority(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	target := messageFixtureClient(t, f.server, f.target.ID)
	other := messageFixtureClient(t, f.server, f.delegate.ID)
	request := apiPreflightRequest(t, f, "agent")
	if _, err := Call[domain.UpgradePreflight](ctx, target, "POST", "/v1/upgrades/preflight", request); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("worker requested Founder native preflight", err)
	}
	record, err := Call[domain.UpgradePreflight](ctx, founder, "POST", "/v1/upgrades/preflight", request)
	if err != nil || record.State != "queued" || record.Result != nil {
		t.Fatal("native preflight was not queued explicitly", record, err)
	}
	inbox, err := Call[[]domain.UpgradePreflight](ctx, target, "GET", "/v1/upgrades/preflight-requests", nil)
	if err != nil || len(inbox) != 1 || inbox[0].ID != record.ID {
		t.Fatal("target's ordinary peer-checked adapter could not fetch its narrow inbox", inbox, err)
	}
	unrelated, err := Call[[]domain.UpgradePreflight](ctx, other, "GET", "/v1/upgrades/preflight-requests", nil)
	if err != nil || len(unrelated) != 0 {
		t.Fatal("target inbox leaked another identity's proof", unrelated, err)
	}
	if _, err := Call[domain.UpgradePreflight](ctx, other, "GET", Path("upgrades", "preflight", record.ID), nil); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("private preflight became a shared report", err)
	}
	report := apiPreflightReply(record)
	if _, err := Call[domain.UpgradePreflight](ctx, other, "POST", Path("upgrades", "preflight", record.ID, "result"), report); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("another target forged the native observation", err)
	}
	done, err := Call[domain.UpgradePreflight](ctx, target, "POST", Path("upgrades", "preflight", record.ID, "result"), report)
	if err != nil || done.Result == nil || done.State != "completed" || done.Result.Lease != nil {
		t.Fatal("native observation was lost or granted start authority", done, err)
	}
	observed, err := Call[domain.UpgradePreflight](ctx, founder, "GET", Path("upgrades", "preflight", record.ID), nil)
	if err != nil || observed.Revision != done.Revision || observed.Result.Version != "0.3.2" {
		t.Fatal("Founder could not read the actual target result", observed, err)
	}
	for _, path := range []string{
		"/v1/upgrades/preflight-requests?target=" + f.target.ID,
		Path("upgrades", "preflight", record.ID) + "?role=foundation",
	} {
		if err := founder.Request(ctx, "GET", path, nil, nil); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("preflight accepted scope metadata overrides", path, err)
		}
	}
}

func TestUpgradePreflightFoundationInboxAndResultRemainRootPrivate(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	request := apiPreflightRequest(t, f, "foundation")
	record, err := Call[domain.UpgradePreflight](ctx, founder, "POST", "/v1/upgrades/preflight", request)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"query.maintenance.upgrade-preflights", "command.maintenance.upgrade-preflight-result"} {
		if _, err := founder.Message(ctx, message.Request{Operation: operation}); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("ordinary Founder ingress acquired a root maintenance capability", operation, err)
		}
	}
	report := apiPreflightReply(record)
	if _, err := Call[domain.UpgradePreflight](ctx, founder, "POST", Path("upgrades", "preflight", record.ID, "result"), report); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary agent reported on behalf of the Foundation OS receiver", err)
	}
	var workers sync.WaitGroup
	root := NewMessageClient("fixture://private-maintenance", func(context.Context) (net.Conn, error) {
		left, right := net.Pipe()
		workers.Go(func() {
			defer left.Close()
			_ = f.server.serveMaintenanceMessages(ctx, left)
		})
		return right, nil
	})
	defer func() {
		root.Close()
		cancel()
		workers.Wait()
	}()
	raw, err := root.Message(ctx, message.Request{Operation: "query.maintenance.upgrade-preflights"})
	if err != nil {
		t.Fatal(err)
	}
	var inbox []domain.UpgradePreflight
	if err := message.UnmarshalPayload(raw, &inbox); err != nil || len(inbox) != 1 || inbox[0].ID != record.ID {
		t.Fatal("private native maintenance handler did not return this instance's exact request", inbox, err)
	}
	payload, err := message.MarshalPayload(report)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = root.Message(ctx, message.Request{Operation: "command.maintenance.upgrade-preflight-result", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var done domain.UpgradePreflight
	if err := message.UnmarshalPayload(raw, &done); err != nil || done.State != "completed" || done.Result == nil {
		t.Fatal("private Foundation receiver failed to report the observation", done, err)
	}
	if _, err := root.Message(ctx, message.Request{Operation: "query.maintenance.upgrade-preflights", Target: f.target.ID}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("maintenance inbox accepted a worker target override", err)
	}
}
