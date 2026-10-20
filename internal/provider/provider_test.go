package provider

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type fixtureRunner struct{ specs []process.Spec }

func (r *fixtureRunner) Run(_ context.Context, s process.Spec) error {
	r.specs = append(r.specs, s)
	if s.Started != nil {
		if err := s.Started(123); err != nil {
			return err
		}
	}
	if slices.Contains(s.Args, "--version") {
		_, err := io.WriteString(s.Stdout, "codex-cli 1.2.3\n")
		return err
	}
	if slices.Contains(s.Args, "--help") {
		_, err := io.WriteString(s.Stdout, "--available-tools --allow-all --disable-builtin-mcps --no-custom-instructions --no-ask-user --no-auto-update --silent --dangerously-bypass-approvals-and-sandbox --ask-for-approval --output-last-message --ephemeral --sandbox --skip-git-repo-check")
		return err
	}
	if slices.Contains(s.Args, "login") {
		return nil
	}
	body, err := io.ReadAll(s.Stdin)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), "private prompt") {
		return io.ErrUnexpectedEOF
	}
	reply := Reply{Outcome: "completed", Summary: "fixture", Evidence: []string{"local"}, Questions: []Question{}, AnswerIDs: []string{}}
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if i := slices.Index(s.Args, "--output-last-message"); i >= 0 {
		return secure.Write(s.Args[i+1], data)
	}
	_, err = s.Stdout.Write(data)
	return err
}

func TestAdaptersPinPermissionsStdinAndPrivateOutput(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		for _, coding := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "-coding", false: "-text"}[coding], func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "state")
				c := config.DefaultWorker(root)
				c.ProviderPaths = map[string]string{name: "/fixture/" + name}
				runner := &fixtureRunner{}
				manager := &Manager{Config: c, Runner: runner}
				workspace := filepath.Join(t.TempDir(), "workspace")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
				reply, err := manager.Invoke(context.Background(), Invocation{ID: "invocation-one", Provider: name, Path: c.ProviderPaths[name], Prompt: "private prompt", Workspace: workspace, Coding: coding, Timeout: time.Second})
				if err != nil || reply.Outcome != "completed" {
					t.Fatal(reply, err)
				}
				spec := runner.specs[len(runner.specs)-1]
				args := strings.Join(spec.Args, " ")
				if strings.Contains(args, "private prompt") {
					t.Fatal("prompt exposed in argv")
				}
				if coding {
					if spec.Dir != workspace || (!strings.Contains(args, "--allow-all") && !strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox")) {
						t.Fatal("coding approvals not explicit", args)
					}
				} else {
					if spec.Dir == workspace || strings.Contains(args, "--allow-all") || strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
						t.Fatal("text call acquired tools", args)
					}
					if name == "codex" && !strings.Contains(args, "features.shell_tool=false") {
						t.Fatal("shell tool enabled")
					}
				}
				if _, err := secure.Read(filepath.Join(root, "invocations", "invocation-one", "reply.json"), 256<<10); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProviderHealthAndReplyValidation(t *testing.T) {
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	c.ProviderPaths = map[string]string{"codex": "/fixture/codex"}
	manager := &Manager{Config: c, Runner: &fixtureRunner{}}
	health, err := manager.Check(context.Background(), "codex")
	if err != nil || health.Authentication != "credentials-present-unverified" || health.Version != "1.2.3" {
		t.Fatal(health, err)
	}
	if _, err := manager.Resolve("unknown"); err == nil {
		t.Fatal("unknown adapter")
	}
	if _, err := Arguments("unknown", "", "", "", false); err == nil {
		t.Fatal("unknown adapter arguments")
	}
	if err := (Reply{Outcome: "completed", Summary: ""}).Validate(); err == nil {
		t.Fatal("empty success accepted")
	}
	if err := (Reply{Outcome: "done", Summary: "unverified"}).Validate(); err == nil {
		t.Fatal("provider can accept its own result")
	}
}
