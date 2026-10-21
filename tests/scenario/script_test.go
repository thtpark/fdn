package scenario

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

func privateTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(".", ".scenario-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func scriptTest(t *testing.T, plan Plan, launch work.Launch, args []string) ([]byte, string, error) {
	t.Helper()
	home := privateTestRoot(t)
	cwd := filepath.Join(home, "workspace")
	if err := secure.Dir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	if err := WritePlan(home, plan); err != nil {
		t.Fatal(err)
	}
	reply, present, err := RunScript(home, launch, args)
	if !present {
		t.Fatal("an explicitly written private script was not selected")
	}
	return reply, home, err
}

func TestScriptRefinesOnlyActualRequestedRepliesWithoutTools(t *testing.T) {
	waiting := true
	plan := Plan{ActorID: "worker", Provider: "codex", Model: "worker-model", Rules: []Rule{{Name: "actual-answer", Phases: []string{"preflight"}, Waiting: &waiting, MaxCalls: 1,
		Reply: `{"outcome":"clear","summary":"Incorporated the actual requested answer","questions":[],"evidence":[],"answer_ids":{{json .AnswerIDs}},"description":{{json (printf "%s: %s" .Task.Description .AnswerText)}}}`}}}
	launch := work.Launch{AgentID: "worker", Phase: "preflight", Task: &domain.Task{Description: "scope", Waiting: &domain.Waiting{QuestionIDs: []string{"question"}, RequestedIDs: []string{"founder"}}}, Discussion: []domain.Entry{
		{ID: "wrong-actor", Kind: "comment", ParentID: "question", AuthorID: "bystander", Body: "fabricated"},
		{ID: "wrong-question", Kind: "comment", ParentID: "other", AuthorID: "founder", Body: "not this decision"},
		{ID: "actual", Kind: "comment", ParentID: "question", AuthorID: "founder", Body: "Use 0.5 seconds, no jitter"},
	}}
	data, home, err := scriptTest(t, plan, launch, []string{"--ask-for-approval", "never", "exec", "--sandbox", "read-only", "--model", "worker-model"})
	if err != nil {
		t.Fatal(err)
	}
	var reply provider.Reply
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.AnswerIDs) != 1 || reply.AnswerIDs[0] != "actual" || reply.Description == nil || *reply.Description != "scope: Use 0.5 seconds, no jitter" {
		t.Fatal("script invented or misattributed a requested answer")
	}
	trace, err := ReadTrace(home)
	if err != nil || len(trace.Calls) != 1 || trace.Calls[0].Coding || !trace.Calls[0].Completed {
		t.Fatal("text-only trace was not privately recorded", err)
	}
	if _, _, err := RunScript(home, launch, []string{"exec", "--model", "worker-model"}); err == nil {
		t.Fatal("script repeated a single-use inference rule")
	}
}

func TestScriptRejectsToolActionsDuringTextCalls(t *testing.T) {
	plan := Plan{ActorID: "worker", Provider: "copilot", Model: "model", Rules: []Rule{{Name: "no-tools", Phases: []string{"scope"}, MaxCalls: 1, Actions: []Action{{Kind: "write", Path: "must-not-exist", Text: "unapproved code"}}, Reply: `{}`}}}
	_, home, err := scriptTest(t, plan, work.Launch{AgentID: "worker", Phase: "scope"}, []string{"--model", "model"})
	if err == nil {
		t.Fatal("a text-only call performed a tool action")
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("text-only call changed code")
	}
}

func TestScriptRejectsAnotherIdentityProviderModelAndToolPolicy(t *testing.T) {
	for _, test := range []struct {
		name, actor string
		args        []string
	}{
		{"identity", "delegate", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--model", "model"}},
		{"provider", "worker", []string{"--allow-all", "--model", "model"}},
		{"model", "worker", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--model", "another"}},
		{"tool-policy", "worker", []string{"exec", "--model", "model"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := Plan{ActorID: "worker", Provider: "codex", Model: "model", Rules: []Rule{{Name: "guarded", Phases: []string{"execution"}, Reply: `{}`}}}
			if _, _, err := scriptTest(t, plan, work.Launch{AgentID: test.actor, Phase: "execution"}, test.args); err == nil {
				t.Fatal("an attribution/tool-policy mismatch was accepted")
			}
		})
	}
}

func TestScriptHTTPNeverUsesLiveHostsOrRedirects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "https://github.com/", http.StatusFound)
	}))
	defer server.Close()
	for _, endpoint := range []string{"https://api.github.com", "http://github.com", "http://localhost:1234", server.URL} {
		t.Run(strings.NewReplacer(":", "_", "/", "_").Replace(endpoint), func(t *testing.T) {
			plan := Plan{ActorID: "worker", Provider: "copilot", Model: "model", APIURL: endpoint, Rules: []Rule{{Name: "loopback-only", Phases: []string{"execution"}, Actions: []Action{{Kind: "http", Method: "GET", Path: "/repos/thtpark/openai-python/pulls/1"}}, Reply: `{}`}}}
			if _, _, err := scriptTest(t, plan, work.Launch{AgentID: "worker", Phase: "execution"}, []string{"--allow-all", "--model", "model"}); err == nil {
				t.Fatal("a live host or redirect was accepted")
			}
		})
	}
	if requests.Load() != 1 {
		t.Fatal("a non-literal-loopback request escaped the fixture")
	}
}

func TestLocalGitRejectsHTTPTransportAndFileTraversal(t *testing.T) {
	root := privateTestRoot(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := Git(context.Background(), root, "ls-remote", server.URL+"/repo.git"); err == nil || requests.Load() != 0 {
		t.Fatal("isolated Git attempted an HTTP transport")
	}
	for _, override := range []string{"-c", "-cprotocol.allow=always", "--config=credential.helper=unsafe"} {
		if _, err := Git(context.Background(), root, override, "version"); err == nil {
			t.Fatal("isolated Git accepted a configuration override")
		}
	}
	for _, path := range []string{"../escape", "/absolute", ".git/config", "."} {
		if _, err := relativeFile(root, path); err == nil {
			t.Fatal("file action accepted an unowned path", path)
		}
	}
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := relativeFile(root, "link/escape"); err == nil {
		t.Fatal("file action followed a symlink outside its explicit path")
	}
}

func TestMissingPlanPreservesLegacyFixtureSelection(t *testing.T) {
	if _, present, err := RunScript(privateTestRoot(t), work.Launch{}, nil); err != nil || present {
		t.Fatal("an absent opt-in plan changed legacy provider behavior", err)
	}
}
