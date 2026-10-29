//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeFounderDeviceEnrollmentListAndRevocation(t *testing.T) {
	l := newLab(t, "codex")
	public, _, _, err := device.Generate("Founder Phone")
	if err != nil {
		t.Fatal(err)
	}
	var enrolled domain.AgentIdentity
	args := []string{"--config", l.founder.configFile, "--operation-id", "native-founder-phone", "device", "register", "--input", "-"}
	for range 2 {
		current := must[domain.AgentIdentity](t, "agent-cli", args, public)
		if current.AgentID != l.founder.agent.ID || current.DeviceID == "" || current.State != "pending" {
			t.Fatal("compiled CLI created another actor instead of a device", current)
		}
		if enrolled.DeviceID != "" && enrolled != current {
			t.Fatal("compiled enrollment replay created another device")
		}
		enrolled = current
	}
	devices := must[[]domain.AgentIdentity](t, "agent-cli", []string{"--config", l.founder.configFile, "device", "list"}, nil)
	if len(devices) != 2 {
		t.Fatal("compiled list omitted original or extra Founder computer", len(devices))
	}
	for _, command := range [][]string{
		{"device", "list"},
		{"device", "register", "--input", "-"},
		{"device", "revoke", enrolled.DeviceID, "--input", "-"},
	} {
		var input any = public
		if command[1] == "revoke" {
			input = domain.RevokeDeviceRequest{Reason: "Workers cannot revoke Founder devices"}
		}
		reply, code, _ := run(t, "agent-cli", append([]string{"--config", l.worker.configFile}, command...), input)
		if code == 0 || reply.OK || reply.Error == nil || reply.Error.Code != domain.Denied {
			t.Fatal("compiled worker command acquired Founder device authority", command, reply.Error)
		}
	}
	args = []string{"--config", l.founder.configFile, "--operation-id", "native-revoke-founder-phone",
		"device", "revoke", enrolled.DeviceID, "--input", "-"}
	for range 2 {
		revoked := must[domain.AgentIdentity](t, "agent-cli", args, domain.RevokeDeviceRequest{Reason: "Cancel this device approval"})
		if revoked.DeviceID != enrolled.DeviceID || revoked.State != "revoked" || revoked.Generation != enrolled.Generation+1 {
			t.Fatal("compiled scoped revocation changed another device or epoch", revoked)
		}
	}
	agents, err := l.fixture.store.Agents(context.Background(), l.founder.agent.ID)
	if err != nil || len(agents) != 2 {
		t.Fatal("device CLI created an extra enrolled actor", agents, err)
	}
	raw := l.cli(t, l.founder, []string{"agent", "identity"}, nil)
	var retained domain.AgentIdentity
	if err := json.Unmarshal(raw, &retained); err != nil || retained.AgentID != l.founder.agent.ID || retained.DeviceID != "" || retained.State != "active" {
		t.Fatal("revocation changed the original Founder enrollment", err)
	}
}
