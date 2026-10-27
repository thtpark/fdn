//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

func TestLoginShellFindsManagedProvidersAndSelectsUpdatedVersion(t *testing.T) {
	l := newLab(t, "codex")
	home := filepath.Join(filepath.Dir(l.worker.configFile), "home")
	profile := filepath.Join(home, ".bash_profile")
	prior := []byte("# Existing human shell settings\nexport FDN_SHELL_TEST=preserved\n")
	if err := os.WriteFile(profile, prior, 0600); err != nil {
		t.Fatal(err)
	}
	c := l.worker.config
	c.ProviderPaths = map[string]string{}
	if err := config.Write(l.worker.configFile, c); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "copilot"} {
		active := provider.Installed{Provider: name, Version: "1.2.3", Path: filepath.Join(binaries, "fixtureprovider"), SignaturesVerified: true}
		if err := config.Write(filepath.Join(c.StateDir, "providers", name, "active.json"), active); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(filepath.Join(binaries, "agent-cli"), "--config", l.worker.configFile, "provider", "link")
	command.Env = append(isolatedEnvironment([]string{"--config", l.worker.configFile}), "SHELL=/bin/bash")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	login := func(script string) string {
		t.Helper()
		command := exec.Command("/bin/bash", "--login", "-c", script)
		command.Env = append(os.Environ(), "HOME="+home, "SHELL=/bin/bash", "PATH=/usr/bin:/bin")
		command.Dir = home
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatal("actual login shell provider command failed", string(output), err)
		}
		return string(output)
	}
	output := login(`set -eu; test "$FDN_SHELL_TEST" = preserved; command -v codex; command -v copilot; codex --version; copilot --version`)
	for _, expected := range []string{filepath.Join(home, ".foundation", "bin", "codex"), filepath.Join(home, ".foundation", "bin", "copilot"), "fixture-cli 1.2.3"} {
		if !strings.Contains(output, expected) {
			t.Fatal("bare commands missing from login PATH", expected, output)
		}
	}
	if strings.Contains(output, `"ok"`) {
		t.Fatal("native version output acquired a Foundation JSON envelope")
	}
	updated := filepath.Join(home, "updated-provider")
	if err := secure.Create(updated, []byte("#!/bin/sh\nprintf 'updated-fixture 9.8.7\\n'\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(updated, 0700); err != nil {
		t.Fatal(err)
	}
	active := provider.Installed{Provider: "codex", Version: "9.8.7", Path: updated, SignaturesVerified: true}
	if err := config.Write(filepath.Join(c.StateDir, "providers", "codex", "active.json"), active); err != nil {
		t.Fatal(err)
	}
	if output := login("codex --version"); output != "updated-fixture 9.8.7\n" {
		t.Fatal("launcher remained pinned to the old version", output)
	}
	if data, err := os.ReadFile(profile); err != nil || !bytes.HasPrefix(data, prior) {
		t.Fatal("existing human shell settings changed", err)
	}
}

func TestManagedInteractiveCommandReadsForegroundTTYAndOwnsSlot(t *testing.T) {
	l := newLab(t, "codex")
	command := exec.Command(filepath.Join(binaries, "agent-cli"), "--config", l.worker.configFile, "provider", "exec", "--provider", "codex", "--", "--fixture-interactive")
	command.Env = isolatedEnvironment([]string{"--config", l.worker.configFile})
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		select {
		case <-done:
		default:
			command.Process.Kill()
			<-done
		}
	}()
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(terminal)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ready, echoed := false, false
	for !echoed {
		select {
		case <-ctx.Done():
			t.Fatal("interactive provider stopped reading its foreground terminal")
		case line, ok := <-lines:
			if !ok {
				t.Fatal("interactive provider exited before accepting terminal input")
			}
			if strings.Contains(line, "FIXTURE_TERMINAL_READY") {
				ready = true
				if lock, err := secure.Acquire(filepath.Join(l.worker.config.StateDir, "work.lock")); err == nil {
					lock.Close()
					t.Fatal("human native provider did not reserve the shared work slot")
				} else if domain.AsFault(err).Code != domain.Conflict {
					t.Fatal(err)
				}
				if _, err := io.WriteString(terminal, "human-fixture-input\n"); err != nil {
					t.Fatal(err)
				}
			}
			echoed = strings.Contains(line, "FIXTURE_TERMINAL_INPUT=human-fixture-input")
		}
	}
	if !ready {
		t.Fatal("fixture did not enter its terminal")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("interactive provider/terminal restoration failed", err)
		}
		done <- nil
	case <-ctx.Done():
		t.Fatal("interactive provider did not release terminal/process state")
	}
	lock, err := secure.Acquire(filepath.Join(l.worker.config.StateDir, "work.lock"))
	if err != nil {
		t.Fatal("finished native session retained the local slot", err)
	}
	lock.Close()
}
