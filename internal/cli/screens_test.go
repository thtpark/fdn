package cli

import (
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestHeadlessScreenCLIRequiresNoEnrollmentAndDoesNotInventDisplay(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	c := config.DefaultWorker(filepath.Join(home, "state"))
	file := filepath.Join(home, "agent.json")
	if err := config.Write(file, c); err != nil {
		t.Fatal(err)
	}
	envelope, code := invoke(t, "agent", []string{"--config", file, "screen", "list"}, nil)
	if code != 0 || !envelope.OK || string(envelope.Data) != "[]" {
		t.Fatal("headless list advertised a display or required remote authority", code, string(envelope.Data), envelope.Error)
	}
	envelope, code = invoke(t, "agent", []string{"--config", file, "screen", "launch", "screen_missing"}, nil)
	if code != domain.ExitCode(domain.Fail(domain.Invalid, "")) || envelope.OK {
		t.Fatal("screen launch accepted missing application ID", code, envelope.Error)
	}
	envelope, code = invoke(t, "agent", []string{"--config", file, "screen", "launch", "screen_missing", "--application", "browser"}, nil)
	if code == 0 || envelope.OK {
		t.Fatal("headless screen launch silently created a desktop", code, envelope.Error)
	}
}
