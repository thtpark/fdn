package provider

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type loginHelpRunner struct {
	help  string
	err   error
	specs []process.Spec
}

func (r *loginHelpRunner) Run(ctx context.Context, spec process.Spec) error {
	r.specs = append(r.specs, spec)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 15*time.Second {
		return errors.New("login capability probe is not bounded")
	}
	if !slices.Contains(spec.Args, "--help") || spec.Stdin != nil || spec.Terminal != nil {
		return errors.New("capability probe attempted login or terminal interaction")
	}
	if r.err != nil {
		return r.err
	}
	_, err := io.WriteString(spec.Stdout, r.help)
	return err
}

func TestLoginCommandPrefersOnlyAdvertisedDeviceFlows(t *testing.T) {
	for _, item := range []struct {
		name, help string
		args       []string
	}{
		{"codex", "Options:\n  --device-auth  Use device authentication\n", []string{"login", "--device-auth"}},
		{"copilot", "Options:\n  --device-code  Force OAuth device code flow\n", []string{"--no-auto-update", "login", "--device-code"}},
		{"codex", "Usage: login [--device-auth]", []string{"login", "--device-auth"}},
		{"codex", "Options: --device-authentication", []string{"login"}},
		{"copilot", "Options: --device-code-internal", []string{"--no-auto-update", "login"}},
		{"codex", "Usage: login [options]\n--help", []string{"login"}},
		{"copilot", "Usage: login [options]\n--help", []string{"--no-auto-update", "login"}},
	} {
		t.Run(item.name+item.help, func(t *testing.T) {
			c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
			c.ProviderPaths = map[string]string{item.name: "/fixture/" + item.name}
			runner := &loginHelpRunner{help: item.help}
			manager := &Manager{Config: c, Runner: runner}
			path, args, err := manager.LoginCommand(context.Background(), item.name)
			if err != nil || path != c.ProviderPaths[item.name] || !slices.Equal(args, item.args) {
				t.Fatal("wrong advertised login selection/fallback", path, args, err)
			}
			probe, _ := NativeArguments(item.name, []string{"login", "--help"})
			if len(runner.specs) != 1 || !slices.Equal(runner.specs[0].Args, probe) {
				t.Fatal("selection started sign-in instead of probing help", runner.specs)
			}
		})
	}
}

func TestLoginCommandProbeFailureNeverStartsLogin(t *testing.T) {
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	c.ProviderPaths = map[string]string{"codex": "/fixture/codex"}
	runner := &loginHelpRunner{err: errors.New("PRIVATE_PROVIDER_DIAGNOSTIC")}
	manager := &Manager{Config: c, Runner: runner}
	path, args, err := manager.LoginCommand(context.Background(), "codex")
	if err == nil || domain.AsFault(err).Code != domain.Prerequisite || path != "" || args != nil || len(runner.specs) != 1 ||
		domain.AsFault(err).Message == runner.err.Error() {
		t.Fatal("probe failure exposed details or launched a fallback", path, args, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := manager.LoginCommand(ctx, "codex"); !errors.Is(err, context.Canceled) || len(runner.specs) != 1 {
		t.Fatal("cancelled authorization probed or started a process", err)
	}
	if _, _, err := manager.LoginCommand(context.Background(), "unknown"); err == nil || len(runner.specs) != 1 {
		t.Fatal("unsupported provider started a process", err)
	}
}
