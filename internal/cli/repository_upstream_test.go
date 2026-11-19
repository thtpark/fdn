package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/testidentity"
)

func TestRepositoryUpstreamURLValidation(t *testing.T) {
	for _, value := range []string{"https://github.com/thtpark/fdn.git", "git@github.com:thtpark/fdn.git",
		"ssh://git@github.com/thtpark/fdn.git", "ssh://github.com:2222/thtpark/fdn.git", "/srv/git/tool.git", "file:///srv/git/tool.git"} {
		if err := validateUpstreamURL(value); err != nil {
			t.Fatal("supported upstream was rejected", value, err)
		}
	}
	for _, value := range []string{"", "https://user:token@github.com/thtpark/fdn.git", "https://token@github.com/thtpark/fdn.git",
		"ssh://git:secret@github.com/thtpark/fdn.git", "ext::sh -c touch% /tmp/pwned", "-uupload-pack=touch", "http://github.com/thtpark/fdn.git",
		"git://github.com/thtpark/fdn.git", "https://github.com/thtpark/fdn.git?token=x", "https://github.com/thtpark/fdn.git#main",
		"relative/path", "file://host/srv/tool.git", "https://github.com/thtpark/fdn .git", "fd::17",
		"ssh://-oProxyCommand=touch/x", "ssh://-oProxyCommand@github.com/x", "-host:path"} {
		if err := validateUpstreamURL(value); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("unsafe or credential-bearing upstream was accepted", value, err)
		}
	}
}

type upstreamHarness struct {
	t        *testing.T
	ctx      context.Context
	root     string
	store    *foundation.Store
	founder  domain.Agent
	worker   domain.Agent
	project  domain.Project
	state    *local.State
	res      *resources
	upstream string
	work     string
}

func newUpstreamHarness(t *testing.T) *upstreamHarness {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Founder Fixture\n\temail = founder@example.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := foundation.OpenWithFiles(ctx, filepath.Join(root, "server", "foundation.sqlite"), foundation.FileStoreOptions{
		Permissions: filestore.PermissionFunc(func(context.Context, string, []uint32) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	h := &upstreamHarness{t: t, ctx: ctx, root: root, store: store}
	if h.founder, err = testidentity.Register(ctx, store, "", "Founder", 4200); err != nil {
		t.Fatal(err)
	}
	if h.worker, err = testidentity.Register(ctx, store, h.founder.ID, "Resolver", 4201); err != nil {
		t.Fatal(err)
	}
	if h.project, err = store.CreateProject(ctx, h.founder.ID, domain.ProjectRequest{Command: domain.Command{OperationID: "upstream-project"}, Name: "Managed tools"}); err != nil {
		t.Fatal(err)
	}
	server, err := api.NewAuthenticated(store, nil, func(*http.Request) (domain.Agent, error) { return h.founder, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	handler := server.Handler()
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	if h.state, err = local.Open(ctx, filepath.Join(root, "founder", "state")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.state.Close() })
	client := &api.Client{BaseURL: httpServer.URL, HTTP: httpServer.Client(), Credential: config.Credential{AgentID: h.founder.ID}}
	h.res = &resources{config: config.DefaultWorker(h.state.Root), state: h.state, client: client}
	h.upstream = filepath.Join(root, "upstream.git")
	h.work = filepath.Join(root, "upstream-work")
	h.git(root, "init", "--quiet", "--bare", "-b", "main", h.upstream)
	h.git(root, "init", "--quiet", "-b", "main", h.work)
	return h
}

func (h *upstreamHarness) git(directory string, args ...string) string {
	h.t.Helper()
	output, err := h.gitResult(directory, args...)
	if err != nil {
		h.t.Fatalf("isolated Git %v: %v: %s", args, err, output)
	}
	return output
}

func (h *upstreamHarness) gitResult(directory string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(data)), err
}

func (h *upstreamHarness) commit(directory, branch, from, file, content string) string {
	h.t.Helper()
	args := []string{"checkout", "--quiet", "-B", branch}
	if from != "" {
		args = append(args, from)
	}
	h.git(directory, args...)
	if err := os.WriteFile(filepath.Join(directory, file), []byte(content), 0644); err != nil {
		h.t.Fatal(err)
	}
	h.git(directory, "add", file)
	h.git(directory, "commit", "--quiet", "-m", "Change "+file)
	return h.git(directory, "rev-parse", "HEAD")
}

func (h *upstreamHarness) publish(commit string, force bool) {
	h.t.Helper()
	spec := commit + ":refs/heads/main"
	if force {
		spec = "+" + spec
	}
	h.git(h.work, "push", "--quiet", h.upstream, spec)
}

func (h *upstreamHarness) repository(name string, commit string) domain.HostedRepository {
	h.t.Helper()
	req := domain.RepositoryRequest{Command: domain.Command{OperationID: domain.NewID("op")}, ProjectID: h.project.ID, Name: name}
	if commit != "" {
		h.git(h.work, "update-ref", "refs/fixture/seed", commit)
		path := filepath.Join(h.root, name+".bundle")
		h.git(h.work, "bundle", "create", "--version=2", path, "refs/fixture/seed")
		data, err := os.ReadFile(path)
		if err != nil {
			h.t.Fatal(err)
		}
		data = bytes.Replace(data, []byte(commit+" refs/fixture/seed\n"), []byte(commit+" refs/heads/main\n"), 1)
		file, err := h.res.client.UploadFile(h.ctx, domain.FileUploadRequest{Command: domain.Command{OperationID: domain.NewID("op")},
			Name: name + ".bundle", MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: domain.Hash(string(data))}, bytes.NewReader(data))
		if err != nil {
			h.t.Fatal(err)
		}
		req.BundleFileID = file.ID
	}
	repo, err := h.store.CreateRepository(h.ctx, h.founder.ID, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return repo
}

func (h *upstreamHarness) input(value any) string {
	h.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		h.t.Fatal(err)
	}
	path := filepath.Join(h.root, domain.NewID("input")+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *upstreamHarness) run(operation string, leaf string, args ...string) (any, error) {
	h.t.Helper()
	a := &app{operation: operation, explicitOperation: true}
	return a.repositoryUpstreamCommand(h.ctx, h.res, leaf, args)
}

func (h *upstreamHarness) sync(operation, repo string, input repositorySyncInput) (repositorySyncResult, error) {
	h.t.Helper()
	value, err := h.run(operation, "sync", repo, "--input", h.input(input))
	if err != nil {
		return repositorySyncResult{}, err
	}
	return value.(repositorySyncResult), nil
}

func (h *upstreamHarness) refs(id string) (domain.HostedRepository, map[string]string) {
	h.t.Helper()
	repo, err := h.store.Repository(h.ctx, h.founder.ID, id)
	if err != nil {
		h.t.Fatal(err)
	}
	refs := map[string]string{}
	for _, ref := range repo.Refs {
		refs[ref.Name] = ref.Commit
	}
	return repo, refs
}

func TestRepositoryUpstreamSyncFounderWorkflow(t *testing.T) {
	h := newUpstreamHarness(t)
	baseline := h.commit(h.work, "main", "", "tool.sh", "#!/bin/sh\nprintf 'baseline\\n'\n")
	h.publish(baseline, false)
	internal := h.commit(h.work, "internal", baseline, "tool.sh", "#!/bin/sh\nprintf 'internal\\n'\n")
	repo := h.repository("managed", internal)

	_, err := h.sync("sync-unconfigured", repo.ID, repositorySyncInput{Reason: "Pull reviewed upstream"})
	if domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("sync ran without an explicit upstream", err)
	}
	for _, bad := range []repositoryUpstreamInput{{URL: "https://user:token@github.com/thtpark/fdn.git"}, {URL: h.upstream, Branch: "bad..name"}} {
		if _, err := h.run("upstream-bad", "upstream", repo.ID, "--input", h.input(bad)); domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("unsafe upstream configuration was stored", bad, err)
		}
	}
	configured, err := h.run("upstream-set", "upstream", repo.ID, "--input", h.input(repositoryUpstreamInput{URL: h.upstream}))
	if err != nil {
		t.Fatal(err)
	}
	if value := configured.(repositoryUpstream); value.Branch != "main" || value.TrackingRef != "refs/heads/upstream/main" || value.URL != h.upstream {
		t.Fatal("upstream defaults were not recorded", value)
	}
	if shown, err := h.run("upstream-show", "upstream", repo.ID); err != nil || shown.(repositoryUpstream).URL != h.upstream {
		t.Fatal("local upstream selection was not readable", shown, err)
	}

	result, err := h.sync("sync-contained", repo.ID, repositorySyncInput{Reason: "Track reviewed upstream"})
	if err != nil || result.Outcome != "up-to-date" || result.Applied || result.UpstreamCommit != baseline {
		t.Fatal("contained upstream was not recognized", result, err)
	}
	_, refs := h.refs(repo.ID)
	if refs["refs/heads/main"] != internal || refs["refs/heads/upstream/main"] != baseline {
		t.Fatal("contained upstream changed the default or skipped tracking", refs)
	}
	before, _ := h.refs(repo.ID)
	if result, err := h.sync("sync-noop", repo.ID, repositorySyncInput{Reason: "Nothing new"}); err != nil || result.Outcome != "up-to-date" {
		t.Fatal(result, err)
	}
	if after, _ := h.refs(repo.ID); after.Revision != before.Revision {
		t.Fatal("no-op sync submitted a mutation")
	}

	docs := h.commit(h.work, "upstream", baseline, "README.md", "upstream docs\n")
	h.publish(docs, false)
	preview, err := h.sync("sync-preview", repo.ID, repositorySyncInput{Reason: "Preview", DryRun: true})
	if err != nil || preview.Outcome != "merged" || preview.Applied || !preview.DryRun {
		t.Fatal("dry run did not classify a clean merge", preview, err)
	}
	if after, _ := h.refs(repo.ID); after.Revision != before.Revision {
		t.Fatal("dry run changed hosted source")
	}
	merged, err := h.sync("sync-merge", repo.ID, repositorySyncInput{Reason: "Apply reviewed upstream docs"})
	if err != nil || merged.Outcome != "merged" || !merged.Applied || merged.BaseCommit != internal || merged.UpstreamCommit != docs {
		t.Fatal("clean upstream merge was not applied", merged, err)
	}
	if replay, err := h.sync("sync-merge", repo.ID, repositorySyncInput{Reason: "Apply reviewed upstream docs"}); err != nil || replay.Commit != merged.Commit || replay.Revision != merged.Revision {
		t.Fatal("upstream sync replay was not exact", replay, err)
	}
	if _, err := h.sync("sync-merge", repo.ID, repositorySyncInput{Reason: "Different intent"}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("operation ID was reused for a different sync", err)
	}
	_, refs = h.refs(repo.ID)
	if refs["refs/heads/main"] != merged.Commit || refs["refs/heads/upstream/main"] != docs {
		t.Fatal("hosted refs do not match the applied merge", refs)
	}
	for path, want := range map[string]string{"tool.sh": "internal", "README.md": "upstream docs"} {
		file, err := h.store.RepositoryFile(h.ctx, h.founder.ID, repo.ID, merged.Commit, path)
		if err != nil || !bytes.Contains(file.Data, []byte(want)) {
			t.Fatal("merge lost one side's content", path, err)
		}
	}
	hosted := h.fetchHosted(repo.ID, merged.Commit)
	if parents := strings.Fields(h.git(hosted, "rev-list", "--parents", "-n", "1", merged.Commit)); len(parents) != 3 || parents[1] != internal || parents[2] != docs {
		t.Fatal("merge does not keep the hosted default as first parent", parents)
	}
	if author := h.git(hosted, "log", "-1", "--format=%an <%ae>", merged.Commit); author != "Founder Fixture <founder@example.invalid>" {
		t.Fatal("merge did not use the Founder's own Git identity", author)
	}

	conflicting := h.commit(h.work, "upstream", docs, "tool.sh", "#!/bin/sh\nprintf 'upstream\\n'\n")
	h.publish(conflicting, false)
	conflict, err := h.sync("sync-conflict", repo.ID, repositorySyncInput{Reason: "Upstream changed the tool", AssignTo: h.worker.ID})
	if err != nil || conflict.Outcome != "conflict" || conflict.Applied || !slices.Contains(conflict.Conflicts, "tool.sh") || conflict.TaskID == "" {
		t.Fatal("conflicting upstream was not imported and assigned", conflict, err)
	}
	if replay, err := h.sync("sync-conflict", repo.ID, repositorySyncInput{Reason: "Upstream changed the tool", AssignTo: h.worker.ID}); err != nil || replay.TaskID != conflict.TaskID {
		t.Fatal("conflict assignment replay created another task", replay, err)
	}
	again, err := h.sync("sync-conflict-again", repo.ID, repositorySyncInput{Reason: "Still conflicting"})
	if err != nil || again.Outcome != "conflict" || again.TaskID != "" || !slices.Contains(again.Conflicts, "tool.sh") {
		t.Fatal("an already-imported conflict could not be re-examined without a no-op import", again, err)
	}
	_, refs = h.refs(repo.ID)
	if refs["refs/heads/main"] != merged.Commit || refs["refs/heads/upstream/main"] != conflicting {
		t.Fatal("conflict import changed the protected default", refs)
	}
	task, err := h.store.Task(h.ctx, h.founder.ID, conflict.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.AssigneeID != h.worker.ID || task.Source == nil || task.Source.Commit != merged.Commit ||
		!strings.HasPrefix(task.Source.Ref, "refs/heads/work/"+h.worker.ID+"/upstream-") || !strings.Contains(task.Description, "tool.sh") {
		t.Fatal("conflict task does not bind the exact base, work ref, assignee and conflicts", task)
	}

	h.git(hosted, "fetch", "--quiet", h.upstream, "refs/heads/main")
	h.git(hosted, "checkout", "--quiet", "-B", "resolution", merged.Commit)
	if _, err := h.gitResult(hosted, "merge", "--no-ff", "-m", "Merge upstream/main into main", conflicting); err == nil {
		t.Fatal("fixture did not conflict")
	}
	if err := os.WriteFile(filepath.Join(hosted, "tool.sh"), []byte("#!/bin/sh\nprintf 'internal and upstream\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	h.git(hosted, "add", "tool.sh")
	h.git(hosted, "commit", "--quiet", "--no-edit")
	resolved := h.git(hosted, "rev-parse", "HEAD")
	h.git(hosted, "checkout", "--quiet", "-B", "reversed", conflicting)
	h.git(hosted, "merge", "--quiet", "-X", "ours", "--no-ff", "-m", "Reversed", merged.Commit)
	binding, err := h.state.BindWorkspace(h.ctx, hosted, true, 60, []string{h.state.Root})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.sync("sync-reversed", repo.ID, repositorySyncInput{Reason: "Wrong parent order",
		Resolution: &repositorySyncResolution{Workspace: binding.Ref, Ref: "refs/heads/reversed"}})
	if domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("resolution with the wrong parent order was submitted", err)
	}
	applied, err := h.sync("sync-resolved", repo.ID, repositorySyncInput{Reason: "Founder resolved upstream conflicts",
		Resolution: &repositorySyncResolution{Workspace: binding.Ref, Ref: "refs/heads/resolution"}})
	if err != nil || applied.Outcome != "resolved" || !applied.Applied || applied.Commit != resolved {
		t.Fatal("Founder resolution was not applied", applied, err)
	}
	_, refs = h.refs(repo.ID)
	if refs["refs/heads/main"] != resolved || refs["refs/heads/upstream/main"] != conflicting {
		t.Fatal("resolution did not advance only the default", refs)
	}

	rewritten := h.commit(h.work, "rewritten", docs, "README.md", "rewritten upstream\n")
	h.publish(rewritten, true)
	if _, err := h.sync("sync-rewritten", repo.ID, repositorySyncInput{Reason: "Upstream force-pushed"}); domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("rewritten upstream history replaced the retained tracking ref", err)
	}
	if entries, err := os.ReadDir(filepath.Join(h.state.Root, "source-sync")); err != nil || len(entries) != 0 {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatal("private sync scratch or bundles were left behind", names, err)
	}
}

func TestRepositoryUpstreamSyncAdoptsNewToolSource(t *testing.T) {
	h := newUpstreamHarness(t)
	initial := h.commit(h.work, "main", "", "tool.sh", "#!/bin/sh\nprintf 'new tool\\n'\n")
	h.publish(initial, false)
	repo := h.repository("new-tool", "")
	if _, err := h.run("upstream-new", "upstream", repo.ID, "--input", h.input(repositoryUpstreamInput{URL: "file://" + h.upstream, Branch: "main"})); err != nil {
		t.Fatal(err)
	}
	result, err := h.sync("sync-initial", repo.ID, repositorySyncInput{Reason: "Adopt the reviewed upstream tool"})
	if err != nil || result.Outcome != "initial" || !result.Applied || result.Commit != initial {
		t.Fatal("empty hosted repository did not adopt upstream", result, err)
	}
	next := h.commit(h.work, "main", initial, "tool.sh", "#!/bin/sh\nprintf 'next\\n'\n")
	h.publish(next, false)
	result, err = h.sync("sync-forward", repo.ID, repositorySyncInput{Reason: "Fast-forward reviewed upstream"})
	if err != nil || result.Outcome != "fast-forward" || result.Commit != next {
		t.Fatal("upstream fast-forward was not applied", result, err)
	}
	_, refs := h.refs(repo.ID)
	if refs["refs/heads/main"] != next || refs["refs/heads/upstream/main"] != next {
		t.Fatal("fast-forward refs differ", refs)
	}
}

// fetchHosted clones hosted retained bytes for commit into a fresh checkout.
func (h *upstreamHarness) fetchHosted(id, commit string) string {
	h.t.Helper()
	repo, err := h.store.Repository(h.ctx, h.founder.ID, id)
	if err != nil {
		h.t.Fatal(err)
	}
	directory := filepath.Join(h.root, "hosted-"+commit[:8])
	h.git(h.root, "init", "--quiet", "-b", "main", directory)
	for _, ref := range repo.Refs {
		if ref.Commit != commit {
			continue
		}
		var data bytes.Buffer
		if _, err := h.res.client.DownloadArtifact(h.ctx, ref.ArtifactID, &data, false); err != nil {
			h.t.Fatal(err)
		}
		path := filepath.Join(h.root, "hosted-"+commit[:8]+".bundle")
		if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
			h.t.Fatal(err)
		}
		h.git(directory, "fetch", "--quiet", path, commit)
		h.git(directory, "checkout", "--quiet", "-B", "main", commit)
		return directory
	}
	h.t.Fatal("hosted commit is not retained", commit)
	return ""
}
