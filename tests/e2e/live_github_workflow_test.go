//go:build e2e && e2e_live

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/tests/scenario"
)

// This separately opted-in boundary check uses real Git/GitHub, never a paid provider.
// The default native scenario above is the Foundation/controller qualification.
func TestOptInLiveGitHubReviewMerge(t *testing.T) {
	if os.Getenv("FDN_E2E_LIVE") != "1" {
		t.Skip("live writes disabled; see tests/e2e/SCENARIOS.md for the explicit disposable-repository opt-in")
	}
	repository := os.Getenv("FDN_E2E_LIVE_REPOSITORY")
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
		t.Fatal("FDN_E2E_LIVE_REPOSITORY must explicitly name the authorized owner/repository; there is no default")
	}
	root := filepath.Join(t.TempDir(), "private-live-github")
	if err := secure.Dir(root); err != nil {
		t.Fatal(err)
	}
	worker := newLiveGitHubActor(t, filepath.Join(root, "worker"), "FDN_E2E_LIVE_WORKER_GH_CONFIG")
	delegate := newLiveGitHubActor(t, filepath.Join(root, "delegate"), "FDN_E2E_LIVE_DELEGATE_GH_CONFIG")
	workerID := strings.TrimSpace(string(worker.must(t, "gh", []string{"api", "--hostname", "github.com", "user", "--jq", ".id"}, nil)))
	delegateID := strings.TrimSpace(string(delegate.must(t, "gh", []string{"api", "--hostname", "github.com", "user", "--jq", ".id"}, nil)))
	if workerID == "" || delegateID == "" || workerID == delegateID {
		t.Fatal("live review requires two different GitHub actors in their own existing credential directories")
	}
	var repo struct {
		DefaultBranch    string `json:"default_branch"`
		Archived         bool   `json:"archived"`
		AllowMergeCommit bool   `json:"allow_merge_commit"`
		Permissions      struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(worker.api(t, "GET", "repos/"+repository, nil), &repo); err != nil {
		t.Fatal(err)
	}
	if repo.Archived || repo.DefaultBranch == "" || !repo.AllowMergeCommit || !repo.Permissions.Push {
		t.Fatal("explicit live target must allow the existing policy-preserving PR/merge workflow; policy is never changed")
	}
	// setup-git writes only the new isolated HOME, not the supplied credential directory.
	worker.must(t, "gh", []string{"auth", "setup-git", "--hostname", "github.com"}, nil)
	delegate.must(t, "gh", []string{"auth", "setup-git", "--hostname", "github.com"}, nil)
	worker.checkout = filepath.Join(root, "worker-checkout")
	delegate.checkout = filepath.Join(root, "delegate-checkout")
	remote := "https://github.com/" + repository + ".git"
	worker.git(t, root, "clone", "--depth=1", "--branch", repo.DefaultBranch, remote, worker.checkout)
	branch := "fdn/" + domain.NewID("live-fixture")
	worker.git(t, worker.checkout, "checkout", "-b", branch)
	file := filepath.ToSlash(filepath.Join("tests", "_foundation_e2e", strings.TrimPrefix(branch, "fdn/"), "retry_fixture.py"))
	write := func(content string) {
		t.Helper()
		path := filepath.Join(worker.checkout, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		worker.git(t, worker.checkout, "add", "--", file)
		worker.git(t, worker.checkout, "diff", "--cached", "--check")
		worker.git(t, worker.checkout, "commit", "-m", "Foundation explicitly authorized live fixture check")
		worker.git(t, worker.checkout, "push", "origin", "HEAD:refs/heads/"+branch)
	}
	prNumber, merged := 0, false
	t.Cleanup(func() {
		if prNumber != 0 && !merged {
			if _, err := worker.request("PATCH", fmt.Sprintf("repos/%s/pulls/%d", repository, prNumber), map[string]any{"state": "closed"}); err != nil {
				t.Error("failed to close this harness's unfinished live PR")
			}
		}
		data, err := worker.request("GET", "repos/"+repository+"/git/matching-refs/heads/"+branch, nil)
		var refs []struct {
			Ref string `json:"ref"`
		}
		if err != nil || json.Unmarshal(data, &refs) != nil {
			t.Error("could not reconcile this harness's exact owned live branch")
			return
		}
		for _, ref := range refs {
			if ref.Ref == "refs/heads/"+branch {
				if _, err := worker.request("DELETE", "repos/"+repository+"/git/refs/heads/"+branch, nil); err != nil {
					t.Error("failed to remove this harness's exact owned live branch")
				}
			}
		}
	})
	write(scenario.FirstRetry)
	firstHead := worker.git(t, worker.checkout, "rev-parse", "HEAD")
	var pr struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Merged bool `json:"merged"`
	}
	path := "repos/" + repository + "/pulls"
	if err := json.Unmarshal(worker.api(t, "POST", path, map[string]any{"title": "Foundation opt-in live fixture review/merge", "body": "Explicitly authorized deterministic fixture. No paid inference. One same-PR correction and independent current-head review; existing branch policies remain enforced.", "head": branch, "base": repo.DefaultBranch}), &pr); err != nil {
		t.Fatal(err)
	}
	prNumber = pr.Number
	if prNumber < 1 || pr.Head.SHA != firstHead {
		t.Fatal("live PR was not created at the actual worker Git head")
	}
	prPath := path + "/" + strconv.Itoa(prNumber)
	delegate.git(t, root, "clone", "--depth=1", "--branch", repo.DefaultBranch, remote, delegate.checkout)
	reviewHead := func(expected, content string) {
		t.Helper()
		delegate.git(t, delegate.checkout, "fetch", "origin", "refs/heads/"+branch+":refs/remotes/origin/"+branch)
		delegate.git(t, delegate.checkout, "checkout", "--detach", "origin/"+branch)
		if delegate.git(t, delegate.checkout, "rev-parse", "HEAD") != expected {
			t.Fatal("live delegate checkout does not match the exact PR head")
		}
		actual, err := os.ReadFile(filepath.Join(delegate.checkout, filepath.FromSlash(file)))
		if err != nil || string(actual) != content {
			t.Fatal("live delegate independently read unexpected Git content", err)
		}
		delegate.git(t, delegate.checkout, "diff", "--check", "HEAD^", "HEAD")
	}
	reviewHead(firstHead, scenario.FirstRetry)
	delegate.api(t, "POST", prPath+"/reviews", map[string]any{"event": "REQUEST_CHANGES", "body": retryReview, "commit_id": firstHead})
	write(scenario.FixedRetry)
	secondHead := worker.git(t, worker.checkout, "rev-parse", "HEAD")
	if secondHead == firstHead {
		t.Fatal("live correction did not advance the same PR branch")
	}
	if err := json.Unmarshal(worker.api(t, "GET", prPath, nil), &pr); err != nil || pr.Head.SHA != secondHead {
		t.Fatal("live same-PR head did not advance", err)
	}
	reviewHead(secondHead, scenario.FixedRetry)
	delegate.api(t, "POST", prPath+"/reviews", map[string]any{"event": "APPROVE", "body": "Independently checked the actual corrected head and exact negative-attempt clamp.", "commit_id": secondHead})
	var merge struct {
		Merged bool `json:"merged"`
	}
	if err := json.Unmarshal(delegate.api(t, "PUT", prPath+"/merge", map[string]any{"sha": secondHead, "merge_method": "merge"}), &merge); err != nil || !merge.Merged {
		t.Fatal("live target policy did not allow the reviewed exact-head merge; no bypass attempted", err)
	}
	merged = true
	if err := json.Unmarshal(delegate.api(t, "GET", prPath, nil), &pr); err != nil || !pr.Merged {
		t.Fatal("live merge was not confirmed by GitHub", err)
	}
	delegate.git(t, delegate.checkout, "fetch", "origin", "refs/heads/"+repo.DefaultBranch+":refs/remotes/origin/"+repo.DefaultBranch)
	if got := delegate.git(t, delegate.checkout, "show", "origin/"+repo.DefaultBranch+":"+file); got+"\n" != scenario.FixedRetry {
		t.Fatal("live merged default branch lacks the actually reviewed correction")
	}
}

type liveGitHubActor struct{ home, credentialDirectory, checkout string }

func newLiveGitHubActor(t *testing.T, home, variable string) *liveGitHubActor {
	t.Helper()
	directory := os.Getenv(variable)
	info, err := os.Lstat(directory)
	if !filepath.IsAbs(directory) || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("%s must name an existing absolute owner-only credential directory; it is never copied or read by the test", variable)
	}
	if err := secure.CheckOwner(info); err != nil {
		t.Fatal("live credential directory must belong to this account")
	}
	if err := secure.Dir(home); err != nil {
		t.Fatal(err)
	}
	return &liveGitHubActor{home: home, credentialDirectory: directory}
}

func (a *liveGitHubActor) command(tool string, args []string, input any, dir string) ([]byte, error) {
	path, err := exec.LookPath(tool)
	if err != nil {
		return nil, errors.New("explicit live tool is unavailable")
	}
	var data []byte
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	output, diagnostic := process.NewBuffer(256<<10), process.NewBuffer(64<<10)
	env := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.Getenv("TMPDIR"), "HOME=" + a.home, "GH_CONFIG_DIR=" + a.credentialDirectory, "GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(a.home, ".gitconfig")}
	err = (process.Native{}).Run(ctx, process.Spec{Path: path, Args: args, Dir: dir, Env: env, Stdin: bytes.NewReader(data), Stdout: output, Stderr: diagnostic})
	result, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	return result, errors.Join(err, outputErr, diagnosticErr)
}

func (a *liveGitHubActor) must(t *testing.T, tool string, args []string, input any) []byte {
	t.Helper()
	data, err := a.command(tool, args, input, a.home)
	if err != nil {
		t.Fatalf("opt-in live %s operation failed; credential/terminal diagnostics remain private: %v", tool, err)
	}
	return data
}

func (a *liveGitHubActor) request(method, path string, input any) ([]byte, error) {
	args := []string{"api", "--hostname", "github.com", "--method", method, path}
	if input != nil {
		args = append(args, "--input", "-")
	}
	return a.command("gh", args, input, a.home)
}

func (a *liveGitHubActor) api(t *testing.T, method, path string, input any) []byte {
	t.Helper()
	data, err := a.request(method, path, input)
	if err != nil {
		t.Fatalf("opt-in live GitHub %s failed; existing branch policies were not bypassed: %v", method, err)
	}
	return data
}

func (a *liveGitHubActor) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	options := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "user.name=Foundation live fixture", "-c", "user.email=fixture@example.invalid", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	data, err := a.command("git", append(options, args...), nil, dir)
	if err != nil {
		t.Fatalf("opt-in live Git operation failed; private diagnostics not published: %v", err)
	}
	return strings.TrimSpace(string(data))
}
