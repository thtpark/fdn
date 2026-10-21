package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGitHubRequiresIndependentCurrentHeadReviewBeforeMerge(t *testing.T) {
	root := privateTestRoot(t)
	github, err := NewGitHub(filepath.Join(root, "github"))
	if err != nil {
		t.Fatal(err)
	}
	defer github.Close()
	worker := github.Authorize(GitHubIdentity{ActorID: "worker", Provider: "codex", Model: "worker-model"})
	delegate := github.Authorize(GitHubIdentity{ActorID: "delegate", Provider: "copilot", Model: "delegate-model", Reviewer: true})
	git := func(args ...string) string {
		t.Helper()
		value, err := Git(context.Background(), github.Seed, args...)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	git("checkout", "-b", "fdn/unit")
	push := func(content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(github.Seed, "src", "openai", "_retry.py"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "src/openai/_retry.py")
		git("commit", "-m", "Update test fixture retry code")
		git("push", "origin", "HEAD:refs/heads/fdn/unit")
		return git("rev-parse", "HEAD")
	}
	first := push(FirstRetry)
	request := func(method, path, token string, value any, expected int) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, github.Server.URL+"/repos/"+Repository+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := github.Server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		if response.StatusCode != expected {
			t.Fatalf("%s %s: status=%d expected=%d", method, path, response.StatusCode, expected)
		}
	}
	create := map[string]any{"title": "Bound retry delay", "body": "Private fixture scenario", "head": "fdn/unit", "base": "main"}
	request("POST", "/pulls", worker, create, http.StatusCreated)
	request("GET", "/pulls/1", "not-authorized", nil, http.StatusUnauthorized)
	request("POST", "/pulls", worker, create, http.StatusConflict)
	review := func(token, event, head string, expected int) {
		request("POST", "/pulls/1/reviews", token, map[string]any{"event": event, "commit_id": head, "body": "Check actual current Git code"}, expected)
	}
	merge := func(token, head string, expected int) {
		request("PUT", "/pulls/1/merge", token, map[string]any{"sha": head, "merge_method": "merge"}, expected)
	}
	merge(delegate, first, http.StatusConflict)
	review(worker, "REQUEST_CHANGES", first, http.StatusConflict)
	review(delegate, "REQUEST_CHANGES", "stale", http.StatusConflict)
	review(delegate, "REQUEST_CHANGES", first, http.StatusCreated)
	review(delegate, "APPROVE", first, http.StatusConflict)
	second := push(FixedRetry)
	review(delegate, "APPROVE", first, http.StatusConflict)
	merge(delegate, second, http.StatusConflict)
	review(delegate, "APPROVE", second, http.StatusCreated)
	merge(worker, second, http.StatusConflict)
	merge(delegate, first, http.StatusConflict)
	request("PUT", "/pulls/1/merge", delegate, map[string]any{"sha": second, "merge_method": "merge", "admin": true}, http.StatusConflict)
	merge(delegate, second, http.StatusOK)
	merge(delegate, second, http.StatusConflict)
	pr, events := github.Snapshot()
	kinds := []string{}
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	if !slices.Equal(kinds, []string{"created", "review-rework", "head-updated", "review-accept", "merged"}) || first == second || pr.Head.SHA != second || !pr.Merged || pr.Number != 1 {
		t.Fatal("fixture lost same-PR review/head/merge ordering", kinds)
	}
	if got := git("--git-dir", github.Origin, "show", "main:src/openai/_retry.py"); got+"\n" != FixedRetry {
		t.Fatal("merge did not publish the actual corrected Git content")
	}
}
