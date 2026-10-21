//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/tests/scenario"
)

var retryQuestions = []string{
	"Which code and test files are in scope, and must the existing call remain compatible?",
	"Which units, default delay, maximum delay, and jitter behavior should be used?",
	"What should zero, negative, and large attempt values return?",
	"Which base branch and PR/review/merge workflow is authorized, including in-scope review rework?",
}

var retryAnswers = []string{
	"Change only src/openai/_retry.py and tests/test_retry.py; preserve the existing one-argument call.",
	"Use seconds, base delay 0.5, maximum delay 8.0, and deterministic exponential backoff without jitter.",
	"Clamp negative attempts to zero; zero and negative inputs return the base delay, and large attempts return the maximum.",
	"Use one PR against main in thtpark/openai-python. The independent delegate may authorize in-scope rework, then review the corrected head and merge before done.",
}

const retryReview = "Clamp negative attempts to zero before exponentiation and add a regression assertion for retry_delay(-1) == 0.5. Keep this in-scope correction on the same PR."
const retryAcceptance = "Verified corrected retry delay, negative-attempt regression assertion, exact current PR head, and merge into main."

func workerHome(w *worker) string { return filepath.Join(filepath.Dir(w.configFile), "home") }

func scriptReply(outcome, summary string) string {
	data, err := json.Marshal(provider.Reply{Outcome: outcome, Summary: summary, Evidence: []string{}, Questions: []provider.Question{}, AnswerIDs: []string{}})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func assertion(text, equal string) scenario.Action {
	return scenario.Action{Kind: "assert", Text: text, Equal: &equal}
}

func gitAction(args ...string) scenario.Action { return scenario.Action{Kind: "git", Args: args} }

func workerPlan(l *lab, w *worker, github *scenario.GitHub, token, model string) scenario.Plan {
	plan := scenario.Plan{ActorID: w.agent.ID, Provider: w.config.Provider, Model: model, Models: []string{model}, APIURL: github.Server.URL, APIToken: token,
		Variables: map[string]string{"origin": github.Origin, "first_code": scenario.FirstRetry, "fixed_code": scenario.FixedRetry, "first_tests": scenario.FirstTests, "fixed_tests": scenario.FixedTests}}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "onboarding", Phases: []string{"onboarding"}, MaxCalls: 1, Reply: scriptReply("reviewed", "Reviewed this identity's private fixture capabilities")})
	waiting := true
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "incorporate-founder-reply", Phases: []string{"preflight"}, Waiting: &waiting, MaxCalls: len(retryAnswers),
		Actions: []scenario.Action{assertion("{{len .Answers}}", "1"), assertion("{{(index .Answers 0).AuthorID}}", l.founder.agent.ID)},
		Reply:   `{"outcome":"clear","summary":"Incorporated the actual requested Founder's reply into the exact specification and criteria","evidence":[],"questions":[],"answer_ids":{{json .AnswerIDs}},"description":{{json (printf "%s\nFounder decision: %s" .Task.Description .AnswerText)}},"acceptance_criteria":{{json (printf "%s\nFounder acceptance: %s" .Task.AcceptanceCriteria .AnswerText)}}}`})
	for index, question := range retryQuestions {
		reply := provider.Reply{Outcome: "blocked", Summary: "Resolve a productive scope or acceptance decision before coding", Evidence: []string{}, Questions: []provider.Question{{Body: question, RespondentIDs: []string{l.founder.agent.ID}}}, AnswerIDs: []string{}}
		data, _ := json.Marshal(reply)
		plan.Rules = append(plan.Rules, scenario.Rule{Name: fmt.Sprintf("clarification-%d", index+1), Phases: []string{"scope"}, DescriptionIncludes: slices.Clone(retryAnswers[:index]), DescriptionExcludes: []string{retryAnswers[index]}, MaxCalls: 1, Reply: string(data)})
	}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "review-refined-spec", Phases: []string{"scope", "acceptance", "preflight"}, DescriptionIncludes: slices.Clone(retryAnswers), MaxCalls: 6, Reply: scriptReply("clear", "Reviewed the exact refined scope, criteria, and full discussion")})
	path := "/repos/" + scenario.Repository + "/pulls"
	firstActions := []scenario.Action{
		assertion("{{.Task.AssigneeID}}", w.agent.ID), assertion("{{.Task.Readiness.ActorID}}", l.founder.agent.ID),
		gitAction("clone", "--no-hardlinks", "{{.Plan.Variables.origin}}", "."), gitAction("checkout", "-b", "fdn/retry"),
		{Kind: "write", Path: "src/openai/_retry.py", Text: "{{.Plan.Variables.first_code}}"},
		{Kind: "write", Path: "tests/test_retry.py", Text: "{{.Plan.Variables.first_tests}}"},
		gitAction("add", "src/openai/_retry.py", "tests/test_retry.py"), gitAction("diff", "--cached", "--check"),
		gitAction("commit", "-m", "Bound deterministic retry delay"), gitAction("push", "origin", "HEAD:refs/heads/fdn/retry"),
		{Kind: "git", Args: []string{"rev-parse", "HEAD"}, Store: "head"},
		{Kind: "http", Method: "POST", Path: path, Body: `{"title":"Bound deterministic retry delay","body":"Implement the exact Founder-approved retry scope; independent review and merge required.","head":"fdn/retry","base":"main"}`, Store: "pr"},
	}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "open-one-pr", Phases: []string{"execution"}, Call: 1, MaxCalls: 1, Actions: firstActions,
		Reply: `{"outcome":"completed","summary":"Implemented bounded retry delay and opened one PR for independent review","evidence":[{{json (printf "PR %s head %s; git diff --cached --check succeeded" .Vars.pr.html_url .Vars.head)}}],"questions":[],"answer_ids":[]}`})
	reworkActions := []scenario.Action{
		{Kind: "assert", Text: "{{json .Discussion}}", Contains: []string{retryReview}},
		{Kind: "file", Path: "src/openai/_retry.py", Text: "{{.Plan.Variables.first_code}}"},
		{Kind: "write", Path: "src/openai/_retry.py", Text: "{{.Plan.Variables.fixed_code}}"},
		{Kind: "write", Path: "tests/test_retry.py", Text: "{{.Plan.Variables.fixed_tests}}"},
		gitAction("add", "src/openai/_retry.py", "tests/test_retry.py"), gitAction("diff", "--cached", "--check"),
		gitAction("commit", "-m", "Address independent review: clamp negative retry attempts"), gitAction("push", "origin", "HEAD:refs/heads/fdn/retry"),
		{Kind: "git", Args: []string{"rev-parse", "HEAD"}, Store: "head"},
		{Kind: "http", Method: "GET", Path: path + "/1", Store: "pr"},
		assertion("{{.Vars.pr.state}}", "open"), assertion("{{.Vars.pr.head.sha}}", "{{.Vars.head}}"),
	}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "correct-same-pr", Phases: []string{"execution"}, Call: 2, MaxCalls: 1, Actions: reworkActions,
		Reply: `{"outcome":"completed","summary":"Addressed the delegate's negative-attempt comment on the same PR","evidence":[{{json (printf "PR %s corrected head %s; git diff --cached --check succeeded" .Vars.pr.html_url .Vars.head)}}],"questions":[],"answer_ids":[]}`})
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "scoped-attention", Phases: []string{"attention"}, MaxCalls: 4, Reply: scriptReply("responded", "The actionable review is incorporated in the current same-PR correction")})
	return plan
}

func delegatePlan(w *worker, github *scenario.GitHub, token, model string) scenario.Plan {
	plan := scenario.Plan{ActorID: w.agent.ID, Provider: w.config.Provider, Model: model, Models: []string{model}, APIURL: github.Server.URL, APIToken: token,
		Variables: map[string]string{"origin": github.Origin, "first_code": scenario.FirstRetry, "fixed_code": scenario.FixedRetry, "fixed_tests": scenario.FixedTests}}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "onboarding", Phases: []string{"onboarding"}, MaxCalls: 1, Reply: scriptReply("reviewed", "Reviewed the delegate's own private provider and capabilities")})
	path := "/repos/" + scenario.Repository + "/pulls/1"
	firstActions := []scenario.Action{
		assertion("{{.Task.Status}}", "completed"), assertion("{{.Task.ResultGeneration}}", "1"),
		gitAction("clone", "--no-hardlinks", "{{.Plan.Variables.origin}}", "."), gitAction("checkout", "--detach", "origin/fdn/retry"),
		{Kind: "file", Path: "src/openai/_retry.py", Text: "{{.Plan.Variables.first_code}}"},
		gitAction("diff", "--check", "origin/main...HEAD"), {Kind: "git", Args: []string{"rev-parse", "HEAD"}, Store: "head"},
		{Kind: "http", Method: "POST", Path: path + "/reviews", Body: `{"event":"REQUEST_CHANGES","body":` + jsonString(retryReview) + `,"commit_id":{{json .Vars.head}}}`},
	}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "independent-rework-review", Phases: []string{"verification"}, Call: 1, MaxCalls: 1, Actions: firstActions,
		Reply: `{"outcome":"reviewed","decision":"rework","summary":` + jsonString(retryReview) + `,"evidence":[{{json (printf "Reviewed %s/pull/1 head %s in an independent Git checkout; negative attempts currently produce 0.25, not 0.5" "` + scenario.RepositoryURL + `" .Vars.head)}}],"questions":[],"answer_ids":[]}`})
	secondActions := []scenario.Action{
		assertion("{{.Task.Status}}", "completed"), assertion("{{.Task.ResultGeneration}}", "2"),
		gitAction("init"), gitAction("fetch", "{{.Plan.Variables.origin}}", "refs/heads/main:refs/remotes/origin/main", "refs/heads/fdn/retry:refs/remotes/origin/fdn/retry"), gitAction("checkout", "--detach", "origin/fdn/retry"),
		{Kind: "file", Path: "src/openai/_retry.py", Text: "{{.Plan.Variables.fixed_code}}"},
		{Kind: "file", Path: "tests/test_retry.py", Text: "{{.Plan.Variables.fixed_tests}}"},
		gitAction("diff", "--check", "origin/main...HEAD"), {Kind: "git", Args: []string{"rev-parse", "HEAD"}, Store: "head"},
		{Kind: "http", Method: "POST", Path: path + "/reviews", Body: `{"event":"APPROVE","body":` + jsonString(retryAcceptance) + `,"commit_id":{{json .Vars.head}}}`},
		{Kind: "http", Method: "PUT", Path: path + "/merge", Body: `{"sha":{{json .Vars.head}},"merge_method":"merge"}`, Store: "merge"},
		assertion("{{.Vars.merge.merged}}", "true"),
	}
	plan.Rules = append(plan.Rules, scenario.Rule{Name: "verify-corrected-head-and-merge", Phases: []string{"verification"}, Call: 2, MaxCalls: 1, Actions: secondActions,
		Reply: `{"outcome":"reviewed","decision":"accept","summary":` + jsonString(retryAcceptance) + `,"evidence":[{{json (printf "Reviewed %s/pull/1 corrected head %s; merged commit %s after independent current-head approval" "` + scenario.RepositoryURL + `" .Vars.head .Vars.merge.sha)}}],"questions":[],"answer_ids":[]}`})
	return plan
}

func jsonString(value string) string { data, _ := json.Marshal(value); return string(data) }

func (l *lab) startScenarioIdentity(t *testing.T, w *worker) *child {
	t.Helper()
	agent, _ := start(t, "agent-service", "--config", w.configFile, "serve")
	waitFor(t, 5*time.Second, func() bool {
		status := must[struct {
			Running bool `json:"daemon_running"`
		}](t, "agent-cli", []string{"--config", w.configFile, "service", "status"}, nil)
		return status.Running
	}, "owned native identity service health")
	return agent
}

func fixtureSignIn(t *testing.T, w *worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(binaries, "fixtureprovider"), "login")
	command.Env = isolatedEnvironment([]string{"--config", w.configFile})
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatal("Founder-arranged fixture sign-in failed without using a live credential UI", err)
	}
}

func scenarioTrace(t *testing.T, w *worker) scenario.Trace {
	t.Helper()
	trace, err := scenario.ReadTrace(workerHome(w))
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func callsFor(trace scenario.Trace, phase string) []scenario.Call {
	calls := []scenario.Call{}
	for _, call := range trace.Calls {
		if call.Phase == phase {
			calls = append(calls, call)
		}
	}
	return calls
}

func assertNoScenarioCoding(t *testing.T, github *scenario.GitHub, identities ...*worker) {
	t.Helper()
	for _, identity := range identities {
		for _, call := range scenarioTrace(t, identity).Calls {
			if call.Coding || call.Phase == "execution" || call.Phase == "verification" {
				t.Fatal("tool-enabled inference occurred before exact Founder signoff")
			}
		}
	}
	pr, events := github.Snapshot()
	if pr.Number != 0 || len(events) != 0 {
		t.Fatal("GitHub side effects occurred before exact Founder signoff")
	}
}

func scenarioDiscussion(t *testing.T, l *lab, taskID string) []domain.Entry {
	t.Helper()
	page, err := api.Call[domain.Page[domain.Entry]](context.Background(), l.founder.client, "GET", api.Path("tasks", taskID, "entries")+"?limit=200", nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" {
		t.Fatal("scenario discussion unexpectedly exceeded its bounded page")
	}
	return page.Items
}

func scenarioJournals(t *testing.T, w *worker) []local.Journal {
	t.Helper()
	state, err := local.Open(context.Background(), w.config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	journals, err := state.Journals(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := state.Outbox(context.Background())
	if err != nil || len(outbox) != 0 {
		t.Fatal("scenario left an undelivered report", err)
	}
	return journals
}

func assertPrivateScenarioJournals(t *testing.T, w *worker, ownNote, otherNote string, tokens []string) {
	t.Helper()
	for _, journal := range scenarioJournals(t, w) {
		if journal.State != "done" {
			t.Fatalf("scenario journal %s remained %s", journal.Kind, journal.State)
		}
		if !strings.Contains(string(journal.Context), ownNote) || strings.Contains(string(journal.Context), otherNote) {
			t.Fatal("identity-private context was lost or crossed into another actor's prompt")
		}
		for _, token := range tokens {
			if strings.Contains(string(journal.Context), token) {
				t.Fatal("a credential entered provider context")
			}
		}
	}
	for _, name := range []string{"plan.json", "trace.json", "script.lock"} {
		info, err := os.Lstat(filepath.Join(workerHome(w), scenario.PlanDirectory, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			t.Fatal("scripted provider material was not kept private", err)
		}
	}
}
