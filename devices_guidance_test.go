package guidance_test

import (
	"strings"
	"testing"

	guidance "github.com/thtpark/fdn"
)

func TestGuideDocumentsFounderDevicesAndNonFounderExclusivity(t *testing.T) {
	for file, fragments := range map[string][]string{
		"AGENTS.md":              {"one active work slot per agent", "device register", "Non-Founders have one computer/live owner"},
		"README.md":              {"device register", "device revoke", "not a new phone/browser client"},
		"SCHEMA.md":              {"SQL7", "identity_owners", "Claims still serialize by AgentID", "optional `device_id`"},
		"COMMANDS.md":            {"agent-cli device list", "agent-cli device register", "agent-cli device revoke", "domain.RevokeDeviceRequest"},
		"DEPLOYMENT.md":          {"## Enroll and revoke additional Founder computers", "Every connection still requires", "last active computer"},
		"MIGRATION.md":           {"A SQL6 binary cannot open SQL7", "independent live Founder owner generations"},
		"DEVELOPMENT.md":         {"SQL7 adds Founder device bindings", "multiple live owner sessions"},
		"TEST_PLAN.md":           {"TestNativeFounderDeviceEnrollmentListAndRevocation", "distinct **logical** UIDs"},
		"IMPLEMENTATION_PLAN.md": {"one active work slot per agent", "duplicate-owner auto-ban", "SQL7"},
		".agents/skills/foundation-enroll/SKILL.md":     {"additional Founder devices", "device register", "device revoke", "prepare fresh keys there"},
		".agents/skills/foundation-install/SKILL.md":    {"Current server SQL7", "SQL6 binaries cannot read SQL7"},
		".agents/skills/foundation-operations/SKILL.md": {"live non-Founder owner key", "device revoke", "all old devices"},
		".agents/skills/foundation-develop/SKILL.md":    {"server SQL migration 7", "device-bound proofs/certificates", "one work slot per agent"},
	} {
		data, err := guidance.Read(file)
		if err != nil {
			t.Fatal(file, err)
		}
		text := strings.Join(strings.Fields(string(data)), " ")
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				t.Fatal("guide lost the Founder device/worker exclusivity contract", file, fragment)
			}
		}
	}
	context, err := guidance.WorkContext()
	if err != nil || !strings.Contains(context, "device register") || !strings.Contains(context, "one active work slot per agent") {
		t.Fatal("controller prompt omitted Founder device and shared-slot guidance", err)
	}
}
