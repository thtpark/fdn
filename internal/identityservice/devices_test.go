package identityservice

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestFounderDeviceProfilePinsDeviceAcrossActivationAndOwnerAdmission(t *testing.T) {
	f := newFixture(t)
	f.profile.Identity.DeviceID = domain.NewID("device")
	f.profile.Identity.DeviceName = "Founder Phone"
	f.remote.profile = f.profile
	status, err := Connect(context.Background(), f.config, f.profile, f.options)
	if err != nil || status.Identity.DeviceID != f.profile.Identity.DeviceID || !status.Agent.Founder {
		t.Fatal("identity service lost Founder device activation", status, err)
	}
	service, stop := startService(t, f, rootlessBoundary{})
	defer stop()
	client, err := api.NewLocalClient(config.Worker{IdentitySocket: f.config.Socket})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var local api.LocalStatus
	if err := client.Request(context.Background(), "GET", "/local/status", nil, &local); err != nil ||
		local.Identity.DeviceID != f.profile.Identity.DeviceID || !local.Ready {
		t.Fatal("local proxy lost the independent Founder device", local, err)
	}
	if !service.Status().Ready {
		t.Fatal("Founder device owner did not become ready")
	}
	d, _, err := loadDevice(f.config.StateDir, f.options.Cryptography)
	if err != nil {
		t.Fatal(err)
	}
	var issued domain.IdentityCertificate
	if err := config.Read(filepath.Join(f.config.StateDir, certificateFile), &issued); err != nil {
		t.Fatal(err)
	}
	changed := f.profile
	changed.Identity.DeviceID = domain.NewID("device")
	if _, err := validateCertificate(issued.Certificate, d.AuthPrivate, changed, "device", 0, f.now); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("certificate was accepted under a different device binding", err)
	}
	changed.Identity.DeviceID = ""
	if _, err := validateCertificate(issued.Certificate, d.AuthPrivate, changed, "device", 0, f.now); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("device certificate was accepted as the original computer", err)
	}
}
