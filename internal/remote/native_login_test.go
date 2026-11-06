package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeAuthenticationPrefersAdvertisedDeviceFlow(t *testing.T) {
	for _, item := range []struct{ name, flag string }{{"codex", "--device-auth"}, {"copilot", "--device-code"}} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			c := config.DefaultWorker(filepath.Join(root, "state"))
			if err := os.MkdirAll(c.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(bin, item.name)
			provider := "#!/bin/sh\ncase \"$*\" in *--help*) printf '%s\\n' 'Options: " + item.flag + "'; exit 0;; esac\nexit 91\n"
			if err := os.WriteFile(path, []byte(provider), 0700); err != nil {
				t.Fatal(err)
			}
			c.ProviderPaths = map[string]string{item.name: path}
			log := filepath.Join(c.StateDir, "tmux-arguments")
			tmux := "#!/bin/sh\ncase \"$*\" in *kill-server*) exit 0;; esac\nprevious=\nfor argument in \"$@\"; do\n  if [ \"$previous\" = -c ]; then cd \"$argument\" || exit 95; break; fi\n  previous=$argument\ndone\nprintf '%s\\n' \"$@\" > tmux-arguments.pending\n/bin/mv tmux-arguments.pending tmux-arguments\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmux), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			terminal, err := (Native{}).Open(ctx, domain.Session{ID: "session_fixture", Mode: "tmux", Purpose: "authentication", Provider: item.name}, c)
			if terminal == nil || err == nil || domain.AsFault(err).Code != domain.Prerequisite ||
				!strings.Contains(err.Error(), "native session client exited before session readiness was proven") {
				t.Fatal("synthetic argument recorder was mistaken for a ready native login session", err)
			}
			defer func() {
				if err := terminal.Close(); err != nil {
					t.Error("synthetic native client cleanup was not proved", err)
				}
			}()
			var data []byte
			for {
				data, err = os.ReadFile(log)
				if err == nil && len(data) != 0 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("synthetic tmux did not receive the planned login", err)
				case <-time.After(10 * time.Millisecond):
				}
			}
			if !strings.Contains(string(data), path+"\n") || !strings.Contains(string(data), "login\n"+item.flag+"\n") ||
				(item.name == "copilot" && !strings.Contains(string(data), "--no-auto-update\n")) {
				t.Fatal("remote authentication did not use the shared safe device-flow selector", string(data))
			}
		})
	}
}
