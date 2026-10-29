package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

func TestFounderDeviceCLIExportsSeparateProfileAndReplaysExactRevocation(t *testing.T) {
	public, _, _, err := device.Generate("Founder Phone")
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.AgentIdentity{DeviceID: domain.NewID("device"), DeviceName: public.Name, Generation: 1,
		State: "provisioned", SSHKey: public.SSHKey, AuthKey: public.AuthKey, SSHFingerprint: "public-phone-fingerprint",
		OwnerKey: "must-not-export-owner-key", OwnerGeneration: 3}
	var registrations, revocations [][]byte
	worker, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/local/profile":
			diagnosticsEnvelope(t, w, config.ConnectionProfile{URL: "https://foundation.example", CA: "public-ca", SSHHost: "foundation.example", SSHPort: 22, KnownHosts: "public-host-pin"}, nil)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/identity/devices":
			var req domain.RegisterIdentityRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Identity != public || req.OperationID != "enroll-founder-phone" {
				t.Error("device CLI changed the signed public request", err)
			}
			data, _ := json.Marshal(req)
			registrations = append(registrations, data)
			diagnosticsEnvelope(t, w, identity, nil)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/identity/devices":
			diagnosticsEnvelope(t, w, []domain.AgentIdentity{identity}, nil)
		case r.Method == http.MethodPost && r.URL.Path == api.Path("identity", "devices", identity.DeviceID, "revoke"):
			var req domain.RevokeDeviceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Reason != "Lost phone" || req.OperationID != "revoke-founder-phone" {
				t.Error("device CLI changed the exact revocation", err)
			}
			data, _ := json.Marshal(req)
			revocations = append(revocations, data)
			revoked := identity
			revoked.State, revoked.Generation = "revoked", 2
			diagnosticsEnvelope(t, w, revoked, nil)
		default:
			t.Error("unexpected Founder device CLI route", r.Method, r.URL.String())
		}
	})
	var credential config.Credential
	if err := config.Read(worker.CredentialFile, &credential); err != nil {
		t.Fatal(err)
	}
	identity.AgentID = credential.AgentID
	private := filepath.Join(t.TempDir(), "private")
	profilePath := filepath.Join(private, "phone-profile.json")
	input := filepath.Join(private, "public.json")
	if err := config.Write(input, public); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", file, "--operation-id", "enroll-founder-phone", "device", "register", "--input", input, "--output", profilePath}
	for range 2 {
		if envelope, code := invoke(t, "agent", args, nil); code != 0 || !envelope.OK {
			t.Fatal("device registration CLI failed", envelope, code)
		}
	}
	if len(registrations) != 2 || !reflect.DeepEqual(registrations[0], registrations[1]) {
		t.Fatal("device registration did not replay the exact signed proposal")
	}
	var profile config.ConnectionProfile
	if err := config.Read(profilePath, &profile); err != nil || profile.Identity.DeviceID != identity.DeviceID ||
		profile.Identity.AgentID != credential.AgentID || profile.Identity.OwnerKey != "" || profile.Identity.OwnerGeneration != 0 ||
		profile.CA != "public-ca" || profile.SSHHost != "foundation.example" {
		t.Fatal("profile export lost device binding/trust or exposed owner material", err)
	}
	if envelope, code := invoke(t, "agent", []string{"--config", file, "device", "list"}, nil); code != 0 || !envelope.OK {
		t.Fatal("device list failed", envelope, code)
	}
	revokeInput := filepath.Join(t.TempDir(), "revoke.json")
	if err := os.WriteFile(revokeInput, []byte(`{"reason":"Lost phone"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args = []string{"--config", file, "--operation-id", "revoke-founder-phone", "device", "revoke", identity.DeviceID, "--input", revokeInput}
	for range 2 {
		if envelope, code := invoke(t, "agent", args, nil); code != 0 || !envelope.OK {
			t.Fatal("device revocation CLI failed", envelope, code)
		}
	}
	if len(revocations) != 2 || !reflect.DeepEqual(revocations[0], revocations[1]) {
		t.Fatal("revocation replay changed its proposal")
	}
	if err := os.WriteFile(revokeInput, []byte(`{"reason":"Changed proposal"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if envelope, code := invoke(t, "agent", args, nil); code != 5 || envelope.OK || len(revocations) != 2 {
		t.Fatal("changed proposal reused a retained operation", envelope, code)
	}
	for _, req := range requests() {
		if req.method == http.MethodGet && len(req.body) != 0 {
			t.Fatal("device query carried a mutation body", req)
		}
	}
	for _, command := range [][]string{
		{"device"}, {"device", "list", "extra"}, {"device", "register"}, {"device", "register", "--founder"},
		{"device", "revoke"}, {"device", "revoke", identity.DeviceID, "--force"},
	} {
		if envelope, code := invoke(t, "agent", append([]string{"--config", file}, command...), nil); code != 2 || envelope.OK {
			t.Fatal("invalid device command accepted", command, envelope, code)
		}
	}
}
