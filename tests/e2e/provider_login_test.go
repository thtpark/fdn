//go:build e2e

package e2e_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/thtpark/fdn/internal/config"
)

func TestNativeProviderLoginShowsSupportedDeviceFlowOnHumanTTY(t *testing.T) {
	for _, item := range []struct{ name, flag string }{{"codex", "--device-auth"}, {"copilot", "--device-code"}} {
		t.Run(item.name, func(t *testing.T) {
			l := newLab(t, item.name)
			path := filepath.Join(filepath.Dir(l.worker.configFile), "device-login-fixture")
			script := "#!/bin/sh\ncase \"$*\" in *--help*) printf '%s\\n' 'Options: " + item.flag + "'; exit 0;; esac\ncase \"$*\" in *'login " + item.flag + "'*) ;; *) exit 92;; esac\nprintf 'PUBLIC_LOGIN_SCREEN https://example.invalid/device\\n'\n"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			c := l.worker.config
			c.ProviderPaths = map[string]string{item.name: path}
			if err := config.Write(l.worker.configFile, c); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(filepath.Join(binaries, "agent-cli"), "--config", l.worker.configFile, "provider", "login", "--provider", item.name)
			command.Env = isolatedEnvironment([]string{"--config", l.worker.configFile})
			terminal, err := pty.Start(command)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			var captured bytes.Buffer
			readDone := make(chan struct{})
			go func() { io.Copy(&captured, terminal); close(readDone) }()
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				select {
				case <-readDone:
				case <-time.After(time.Second):
					terminal.Close()
					<-readDone
				}
				if err != nil || !strings.Contains(captured.String(), "PUBLIC_LOGIN_SCREEN https://example.invalid/device") {
					t.Fatal("human-visible device sign-in was suppressed or wrong flags were used", captured.String(), err)
				}
			case <-time.After(8 * time.Second):
				command.Process.Kill()
				<-done
				terminal.Close()
				<-readDone
				t.Fatal("synthetic device sign-in did not finish")
			}
		})
	}
}
