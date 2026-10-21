package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/local"
)

func invoke(t *testing.T, role string, args []string, input any) (api.Envelope, int) {
	t.Helper()
	var in, out, diagnostic bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&in).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	code := run(context.Background(), role, args, &in, &out, &diagnostic)
	var envelope api.Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(out.String(), err)
	}
	return envelope, code
}

func TestEveryCLILeafHasOfflineHelp(t *testing.T) {
	for _, role := range []string{"agent", "foundation", "service"} {
		for _, command := range commands(role) {
			t.Run(role+"/"+command, func(t *testing.T) {
				args := append(strings.Fields(command), "--help")
				envelope, code := invoke(t, role, args, nil)
				if code != 0 || !envelope.OK {
					t.Fatal(command, envelope.Error)
				}
			})
		}
		for _, args := range [][]string{{"--version"}, {"--help"}, {"help"}} {
			if envelope, code := invoke(t, role, args, nil); code != 0 || !envelope.OK {
				t.Fatal(args, envelope.Error)
			}
		}
	}
	for _, args := range [][]string{{"--timeout", "0", "status"}, {"--operation-id", "../bad", "status"}, {"unknown", "--help"}, {"--bad-flag"}} {
		if envelope, code := invoke(t, "agent", args, nil); code != 2 || envelope.OK {
			t.Fatal(args, code)
		}
	}
}

func TestOfflineCLIUsesPrivatePersistentState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	t.Setenv("HOME", filepath.Join(root, "home"))
	configFile := filepath.Join(root, "config", "agent.json")
	base := []string{"--config", configFile}
	call := func(args []string, input any) api.Envelope {
		t.Helper()
		envelope, code := invoke(t, "agent", append(append([]string{}, base...), args...), input)
		if code != 0 || !envelope.OK {
			t.Fatalf("%v: %v (%d)", args, envelope.Error, code)
		}

		return envelope
	}
	call([]string{"config", "init", "--state", filepath.Join(root, "state")}, nil)
	call([]string{"config", "show"}, nil)
	call([]string{"config", "set", "--input", "-"}, map[string]any{"poll_seconds": 2, "automatic_provider_updates": false})
	call([]string{"mode", "set", "manual"}, nil)
	call([]string{"mode", "show"}, nil)
	call([]string{"preferences", "set", "--input", "-"}, map[string]string{"provider": "copilot", "model": "fixture-model"})
	call([]string{"preferences", "show"}, nil)
	call([]string{"inventory", "refresh"}, nil)
	call([]string{"inventory", "show"}, nil)
	for revision := int64(0); revision < 4; revision++ {
		call([]string{"knowledge", "put", "--input", "-"}, local.KnowledgeChange{Kind: "skill", Name: "test", Content: "private offline content", ExpectedRevision: revision, Selected: true})
	}
	call([]string{"knowledge", "list"}, nil)
	call([]string{"knowledge", "show", "skill:test"}, nil)
	history := call([]string{"knowledge", "history", "skill:test"}, nil)
	var records []local.Knowledge
	if err := json.Unmarshal(history.Data, &records); err != nil || len(records) != 3 {
		t.Fatal(len(records), err)
	}
	call([]string{"work", "prompt"}, nil)
	call([]string{"work", "status"}, nil)
	call([]string{"service", "status"}, nil)
	envelope, code := invoke(t, "agent", append(base, "provider", "login"), nil)
	if code != 6 || envelope.OK {
		t.Fatal("protected login opened without human terminal", code)
	}
	envelope, code = invoke(t, "agent", append(base, "config", "init", "--state", filepath.Join(root, "state")), nil)
	if code != 5 || envelope.OK {
		t.Fatal("configuration reset existing state", code)
	}
}

func TestGuideCommandsNeedNoConfigurationNetworkOrEnrollment(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	configFile := filepath.Join(home, "missing", "agent.json")
	for _, role := range []string{"agent", "foundation", "service"} {
		for _, args := range [][]string{{"guide", "list"}, {"guide", "show", "COMMANDS.md"}, {"guide", "show", ".agents/skills/foundation-work/SKILL.md"}} {
			envelope, code := invoke(t, role, append([]string{"--config", configFile}, args...), nil)
			if code != 0 || !envelope.OK {
				t.Fatal("offline guide failed", role, args, envelope.Error)
			}
		}
		envelope, code := invoke(t, role, []string{"guide", "install", "--home", home}, nil)
		if code != 0 || !envelope.OK {
			t.Fatal("own-account guide installation failed", role, envelope.Error)
		}
		for _, invalid := range [][]string{{"guide", "show", "../credential.json"}, {"guide", "show", "/etc/passwd"}, {"guide", "show", "."}} {
			if envelope, code := invoke(t, role, invalid, nil); code != 3 || envelope.OK {
				t.Fatal("guide read accepted a non-catalog file", role, invalid, code)
			}
		}
	}
	if _, err := os.Stat(configFile); !os.IsNotExist(err) {
		t.Fatal("offline guide created an application configuration")
	}
	envelope, code := invoke(t, "agent", []string{"--config", configFile, "work", "prompt"}, nil)
	if code != 0 || !envelope.OK {
		t.Fatal("shared prompt requires initialization", envelope.Error)
	}
	var prompt struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(envelope.Data, &prompt); err != nil || !strings.Contains(prompt.Prompt, "FDN_OPERATING_GUIDANCE") || !strings.Contains(prompt.Prompt, "name: foundation-work") {
		t.Fatal("manual prompt omitted operating guidance", err)
	}
	catalog, err := guidance.Catalog()
	if err != nil || len(catalog.Skills) != 6 {
		t.Fatal("wrong skill catalog", err)
	}
}
