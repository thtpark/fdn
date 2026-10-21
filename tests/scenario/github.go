package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

const Repository = "thtpark/openai-python"
const RepositoryURL = "https://github.com/" + Repository

// These tiny, original files model the repository shape, not upstream source.
const InitialRetry = "def retry_delay(attempt):\n    return 2 ** attempt\n"
const FirstRetry = "def retry_delay(attempt, base_delay=0.5, max_delay=8.0):\n    \"\"\"Return deterministic bounded backoff in seconds.\"\"\"\n    return min(max_delay, base_delay * (2 ** attempt))\n"
const FixedRetry = "def retry_delay(attempt, base_delay=0.5, max_delay=8.0):\n    \"\"\"Return deterministic bounded backoff in seconds.\"\"\"\n    return min(max_delay, base_delay * (2 ** max(0, attempt)))\n"
const FirstTests = "from openai._retry import retry_delay\n\n\ndef test_retry_delay():\n    assert retry_delay(0) == 0.5\n    assert retry_delay(3) == 4.0\n    assert retry_delay(10) == 8.0\n"
const FixedTests = FirstTests + "    assert retry_delay(-1) == 0.5\n"

type GitHubIdentity struct {
	ActorID  string `json:"actor_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Reviewer bool   `json:"reviewer"`
}

type GitHubEvent struct {
	Sequence int    `json:"sequence"`
	Kind     string `json:"kind"`
	GitHubIdentity
	Head string `json:"head"`
}

type GitHubRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type PullRequest struct {
	Number         int            `json:"number"`
	HTMLURL        string         `json:"html_url"`
	Title          string         `json:"title"`
	Body           string         `json:"body"`
	State          string         `json:"state"`
	Head           GitHubRef      `json:"head"`
	Base           GitHubRef      `json:"base"`
	Merged         bool           `json:"merged"`
	MergeCommitSHA string         `json:"merge_commit_sha,omitempty"`
	Author         GitHubIdentity `json:"author"`
}

// GitHub owns a bare local Git origin and a bounded loopback-only GitHub API.
// Approval is head-bound and independent; there is no force/admin merge route.
type GitHub struct {
	Origin       string
	Seed         string
	Server       *httptest.Server
	mu           sync.Mutex
	identities   map[string]GitHubIdentity
	pr           PullRequest
	events       []GitHubEvent
	reworkHead   string
	approvedHead string
	requests     int
}

func NewGitHub(root string) (*GitHub, error) {
	if err := secure.Dir(root); err != nil {
		return nil, err
	}
	g := &GitHub{Origin: filepath.Join(root, "origin.git"), Seed: filepath.Join(root, "seed"), identities: map[string]GitHubIdentity{}, events: []GitHubEvent{}}
	ctx := context.Background()
	if _, err := Git(ctx, root, "init", "--bare", "--initial-branch=main", g.Origin); err != nil {
		return nil, err
	}
	if err := secure.Dir(g.Seed); err != nil {
		return nil, err
	}
	if _, err := Git(ctx, g.Seed, "init", "--initial-branch=main"); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(g.Seed, "src", "openai"), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(g.Seed, "src", "openai", "_retry.py"), []byte(InitialRetry), 0600); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "src/openai/_retry.py"}, {"commit", "-m", "Seed original local retry fixture"}, {"remote", "add", "origin", g.Origin}, {"push", "origin", "main"}} {
		if _, err := Git(ctx, g.Seed, args...); err != nil {
			return nil, err
		}
	}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serveHTTP))
	return g, nil
}

func (g *GitHub) Close() { g.Server.Close() }

func (g *GitHub) Authorize(identity GitHubIdentity) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	token := domain.NewToken()
	g.identities[token] = identity
	return token
}

func (g *GitHub) Snapshot() (PullRequest, []GitHubEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pr, append([]GitHubEvent{}, g.events...)
}

func (g *GitHub) event(kind string, identity GitHubIdentity, head string) {
	g.events = append(g.events, GitHubEvent{Sequence: len(g.events) + 1, Kind: kind, GitHubIdentity: identity, Head: head})
}

func (g *GitHub) refreshHead() error {
	if g.pr.Number == 0 {
		return nil
	}
	head, err := Git(context.Background(), filepath.Dir(g.Origin), "--git-dir", g.Origin, "rev-parse", "refs/heads/"+g.pr.Head.Ref)
	if err != nil {
		return err
	}
	if head != g.pr.Head.SHA {
		if g.pr.Merged {
			return errors.New("merged fixture head changed")
		}
		g.pr.Head.SHA = head
		g.approvedHead = ""
		g.event("head-updated", g.pr.Author, head)
	}
	return nil
}

func (g *GitHub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	identity, authorized := g.identities[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !authorized {
		http.Error(w, `{"message":"fixture authentication required"}`, http.StatusUnauthorized)
		return
	}
	g.requests++
	if g.requests > 100 {
		http.Error(w, `{"message":"fixture request budget exceeded"}`, http.StatusTooManyRequests)
		return
	}
	if err := g.refreshHead(); err != nil {
		http.Error(w, `{"message":"fixture git state is invalid"}`, http.StatusConflict)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/"+Repository)
	var response any
	var err error
	status := http.StatusOK
	switch {
	case r.URL.Path == "/repos/"+Repository+"/pulls" && r.Method == http.MethodPost:
		var request struct{ Title, Body, Head, Base string }
		if err = decodeRequest(w, r, &request); err == nil {
			response, err = g.create(identity, request.Title, request.Body, request.Head, request.Base)
			status = http.StatusCreated
		}
	case path == "/pulls/1" && r.Method == http.MethodGet:
		if g.pr.Number != 1 {
			err = errors.New("no fixture PR")
		} else {
			response = g.pr
		}
	case path == "/pulls/1/reviews" && r.Method == http.MethodPost:
		var request struct {
			Event    string `json:"event"`
			Body     string `json:"body"`
			CommitID string `json:"commit_id"`
		}
		if err = decodeRequest(w, r, &request); err == nil {
			response, err = g.review(identity, request.Event, request.Body, request.CommitID)
			status = http.StatusCreated
		}
	case path == "/pulls/1/merge" && r.Method == http.MethodPut:
		var request struct {
			SHA         string `json:"sha"`
			MergeMethod string `json:"merge_method"`
		}
		if err = decodeRequest(w, r, &request); err == nil {
			response, err = g.merge(identity, request.SHA, request.MergeMethod)
		}
	default:
		http.Error(w, `{"message":"unimplemented fixture endpoint"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"message":"fixture rejected stale, duplicate, or unauthorized PR operation"}`, http.StatusConflict)
		return
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		return err
	}
	return config.Decode(data, value)
}

func (g *GitHub) create(identity GitHubIdentity, title, body, head, base string) (any, error) {
	if g.pr.Number != 0 || identity.Reviewer || title == "" || body == "" || base != "main" || !strings.HasPrefix(head, "fdn/") {
		return nil, errors.New("fixture requires one worker-created PR against main")
	}
	ctx := context.Background()
	if _, err := Git(ctx, filepath.Dir(g.Origin), "check-ref-format", "--branch", head); err != nil {
		return nil, err
	}
	headSHA, err := Git(ctx, filepath.Dir(g.Origin), "--git-dir", g.Origin, "rev-parse", "refs/heads/"+head)
	if err != nil {
		return nil, err
	}
	baseSHA, err := Git(ctx, filepath.Dir(g.Origin), "--git-dir", g.Origin, "rev-parse", "refs/heads/main")
	if err != nil || headSHA == baseSHA {
		return nil, errors.New("fixture PR must change real Git code")
	}
	g.pr = PullRequest{Number: 1, HTMLURL: RepositoryURL + "/pull/1", Title: title, Body: body, State: "open", Head: GitHubRef{Ref: head, SHA: headSHA}, Base: GitHubRef{Ref: base, SHA: baseSHA}, Author: identity}
	g.event("created", identity, headSHA)
	return g.pr, nil
}

func (g *GitHub) review(identity GitHubIdentity, event, body, head string) (any, error) {
	if g.pr.Number == 0 || g.pr.Merged || !identity.Reviewer || identity.ActorID == g.pr.Author.ActorID || head != g.pr.Head.SHA || body == "" {
		return nil, errors.New("fixture review requires independent current-head evidence")
	}
	switch event {
	case "REQUEST_CHANGES":
		if g.reworkHead != "" {
			return nil, errors.New("duplicate fixture rework")
		}
		g.reworkHead = head
		g.event("review-rework", identity, head)
	case "APPROVE":
		if g.reworkHead == "" || head == g.reworkHead || g.approvedHead != "" {
			return nil, errors.New("fixture approval requires corrected head")
		}
		g.approvedHead = head
		g.event("review-accept", identity, head)
	default:
		return nil, errors.New("unsupported fixture review")
	}
	return map[string]any{"id": len(g.events), "state": event, "commit_id": head, "body": body}, nil
}

func (g *GitHub) merge(identity GitHubIdentity, head, method string) (any, error) {
	if !identity.Reviewer || identity.ActorID == g.pr.Author.ActorID || g.pr.Merged || method != "merge" || head == "" || head != g.pr.Head.SHA || head != g.approvedHead {
		return nil, errors.New("fixture merge requires independent corrected-head approval")
	}
	ctx, dir := context.Background(), filepath.Dir(g.Origin)
	tree, err := Git(ctx, dir, "--git-dir", g.Origin, "rev-parse", head+"^{tree}")
	if err != nil {
		return nil, err
	}
	merge, err := Git(ctx, dir, "--git-dir", g.Origin, "commit-tree", tree, "-p", g.pr.Base.SHA, "-p", head, "-m", "Merge reviewed fixture PR #1")
	if err != nil {
		return nil, err
	}
	if _, err := Git(ctx, dir, "--git-dir", g.Origin, "update-ref", "refs/heads/main", merge, g.pr.Base.SHA); err != nil {
		return nil, err
	}
	g.pr.Merged, g.pr.State, g.pr.MergeCommitSHA = true, "closed", merge
	g.event("merged", identity, head)
	return map[string]any{"merged": true, "sha": merge, "message": fmt.Sprintf("Merged PR #%d after current-head review", g.pr.Number)}, nil
}
