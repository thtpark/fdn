package foundation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func environmentObservation() domain.EnvironmentObservation {
	total, free := uint64(16<<30), uint64(8<<30)
	return domain.EnvironmentObservation{Version: domain.EnvironmentVersion, ObservedAt: 1800000000,
		System: domain.SystemEnvironment{OS: "linux", Architecture: "amd64", GoVersion: "go1.27", OSVersion: "Fixture Linux"},
		CPU:    domain.CPUEnvironment{LogicalCores: 8}, Memory: domain.MemoryEnvironment{TotalBytes: &total, AvailableBytes: &free},
		Executables: []domain.FoundationExecutable{{Name: "agent-cli", State: "observed", Version: "0.3.16", Commit: "fixture"}},
		Problems:    []domain.EnvironmentProblem{}}
}

func TestEnvironmentReportsAreComputerBoundFreshAndExactReplayDoesNotRenew(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	req := domain.EnvironmentRequest{Command: op(), Observation: environmentObservation()}
	first, err := f.s.PublishEnvironment(ctx, f.worker.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.BindingID != f.worker.ID || first.Generation <= 0 {
		t.Fatal("report is not bound to the enrolled computer")
	}
	f.now = f.now.Add(30 * time.Second)
	replayed, err := f.s.PublishEnvironment(ctx, f.worker.ID, req)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatal("replay renewed or changed a retained observation", replayed, err)
	}
	environment, err := f.s.AgentEnvironment(ctx, f.other.ID, f.worker.ID)
	if err != nil || len(environment.Computers) != 1 || environment.Computers[0].Stale {
		t.Fatal("public computer environment was not available", environment, err)
	}
	f.now = f.now.Add(61 * time.Second)
	environment, err = f.s.AgentEnvironment(ctx, f.other.ID, f.worker.ID)
	if err != nil || !environment.Computers[0].Stale {
		t.Fatal("stale server receipt was reported current", environment, err)
	}
	empty, err := f.s.AgentEnvironment(ctx, f.founder.ID, f.other.ID)
	if err != nil || len(empty.Computers) != 0 {
		t.Fatal("missing reports should remain explicit", empty, err)
	}
	if _, err := f.s.AgentEnvironment(ctx, f.worker.ID, f.founder.ID); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("worker discovered private Founder computers", err)
	}
}

func TestEnvironmentFounderDevicesKeepIndependentReports(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	mac, _ := founderDevice(t, f, "Environment Mac", 4101)
	phone, _ := founderDevice(t, f, "Environment Phone", 4102)
	for _, identity := range []domain.AgentIdentity{mac, phone} {
		certificate := securityCertificate(identity, "device")
		observation := environmentObservation()
		if identity.DeviceID == phone.DeviceID {
			observation.System.OS = "ios"
		}
		if _, err := f.s.PublishEnvironment(WithIdentityFence(ctx, certificate), f.founder.AgentID,
			domain.EnvironmentRequest{Command: op(), Observation: observation}); err != nil {
			t.Fatal(err)
		}
	}
	reports, err := f.s.AgentEnvironment(ctx, f.founder.AgentID, f.founder.AgentID)
	if err != nil || len(reports.Computers) != 2 || reports.Computers[0].BindingID == reports.Computers[1].BindingID {
		t.Fatal("separate Founder computers overwrote one environment", reports, err)
	}
}

func TestEnvironmentRejectsPrivatePathsAndRetiredIdentityProofs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	identity, err := f.s.Identity(ctx, f.worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	ci := securityCertificate(identity, "device")
	ci.Generation++
	if _, err := f.s.PublishEnvironment(WithIdentityFence(ctx, ci), f.worker.ID,
		domain.EnvironmentRequest{Command: op(), Observation: environmentObservation()}); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("stale certificate published computer facts", err)
	}
	bad := environmentObservation()
	bad.Executables[0].Commit = "/private/identity/key"
	if _, err := f.s.PublishEnvironment(ctx, f.worker.ID, domain.EnvironmentRequest{Command: op(), Observation: bad}); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("public observation accepted a private path", err)
	}
	bad = environmentObservation()
	bad.Problems = []domain.EnvironmentProblem{{Component: "provider", Code: "raw\ntranscript"}}
	if _, err := f.s.PublishEnvironment(ctx, f.worker.ID, domain.EnvironmentRequest{Command: op(), Observation: bad}); domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("public observation accepted raw output", err)
	}
}
