package foundation

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

// upstreamBundle writes a self-contained bundle naming each commit under its
// exact hosted ref, using private fixture refs so the checkout stays untouched.
func upstreamBundle(t *testing.T, directory string, refs ...[2]string) []byte {
	t.Helper()
	var names []string
	for i, ref := range refs {
		name := "refs/fixture/" + strconv.Itoa(i)
		deliveryGit(t, directory, "update-ref", name, ref[1])
		names = append(names, name)
	}
	path := filepath.Join(t.TempDir(), "upstream.bundle")
	deliveryGit(t, directory, append([]string{"bundle", "create", "--version=2", path}, names...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range refs {
		data = bytes.Replace(data, []byte(ref[1]+" refs/fixture/"+strconv.Itoa(i)+"\n"), []byte(ref[1]+" "+ref[0]+"\n"), 1)
	}
	for _, name := range names {
		deliveryGit(t, directory, "update-ref", "-d", name)
	}
	return data
}

func deliveryGitResult(directory string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(data)), err
}

func upstreamCommit(t *testing.T, directory, branch, from, file, content, message string) string {
	t.Helper()
	deliveryGit(t, directory, "checkout", "--quiet", "-B", branch, from)
	if err := os.WriteFile(filepath.Join(directory, file), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "add", file)
	deliveryGit(t, directory, "commit", "--quiet", "-m", message)
	return deliveryGit(t, directory, "rev-parse", "HEAD")
}

func TestUpstreamImportFounderMergeAndAssignedConflictResolution(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Upstream tools"})
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.GrantRequest{Command: op(), AgentID: f.other.ID}
	grant.ExpectedRevision = project.Revision
	if project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, grant); err != nil {
		t.Fatal(err)
	}
	directory, baseline, _ := deliverySource(t)
	internal := upstreamCommit(t, directory, "internal", baseline, "tool.sh", "#!/bin/sh\nprintf 'internal\\n'\n", "Internal change")
	upstreamOne := upstreamCommit(t, directory, "upstream", baseline, "README.md", "upstream docs\n", "Upstream docs")

	empty, err := f.s.CreateRepository(ctx, f.founder.ID, domain.RepositoryRequest{Command: op(), ProjectID: project.ID, Name: "empty tool"})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(actor string, repo domain.HostedRepository, mutate func(*domain.RepositoryMerge), refs ...[2]string) (domain.HostedRepository, error) {
		t.Helper()
		file := readyFile(t, f, actor, upstreamBundle(t, directory, refs...), "", false)
		req := domain.RepositoryMerge{Command: op(), SourceRef: "refs/heads/upstream/main", BundleFileID: file.ID, Reason: "Founder imported reviewed upstream main"}
		req.ExpectedRevision = repo.Revision
		if base, ok := repositoryRef(repo, "refs/heads/"+repo.DefaultBranch); ok {
			req.BaseCommit = base.Commit
		}
		mutate(&req)
		return f.s.UpstreamRepository(ctx, actor, repo.ID, req)
	}
	_, err = submit(f.other.ID, empty, func(q *domain.RepositoryMerge) { q.SourceCommit, q.Commit = upstreamOne, upstreamOne },
		[2]string{"refs/heads/upstream/main", upstreamOne}, [2]string{"refs/heads/main", upstreamOne})
	assertCode(t, err, domain.Denied)
	empty, err = submit(f.founder.ID, empty, func(q *domain.RepositoryMerge) { q.SourceCommit, q.Commit = upstreamOne, upstreamOne },
		[2]string{"refs/heads/upstream/main", upstreamOne}, [2]string{"refs/heads/main", upstreamOne})
	if err != nil {
		t.Fatal("new tool could not adopt its upstream as initial hosted source", err)
	}
	if head, _ := repositoryRef(empty, "refs/heads/main"); head.Commit != upstreamOne {
		t.Fatal("initial upstream import did not create the default branch", empty.Refs)
	}

	seed := readyFile(t, f, f.founder.ID, upstreamBundle(t, directory, [2]string{"refs/heads/main", internal}), "", false)
	repo, err := f.s.CreateRepository(ctx, f.founder.ID, domain.RepositoryRequest{Command: op(), ProjectID: project.ID,
		Name: "managed tool", DefaultBranch: "main", BundleFileID: seed.ID})
	if err != nil {
		t.Fatal(err)
	}
	refs := func(commit string) [2]string { return [2]string{"refs/heads/upstream/main", commit} }
	main := func(commit string) [2]string { return [2]string{"refs/heads/main", commit} }
	for _, test := range []struct {
		name   string
		code   domain.Code
		mutate func(*domain.RepositoryMerge)
	}{
		{"task fields", domain.Invalid, func(q *domain.RepositoryMerge) { q.SourceCommit, q.TaskID = upstreamOne, "task_fixture" }},
		{"protected source", domain.Invalid, func(q *domain.RepositoryMerge) { q.SourceCommit, q.SourceRef = upstreamOne, "refs/heads/main" }},
		{"stale revision", domain.Conflict, func(q *domain.RepositoryMerge) {
			q.SourceCommit, q.ExpectedRevision = upstreamOne, q.ExpectedRevision+1
		}},
		{"stale base", domain.Conflict, func(q *domain.RepositoryMerge) { q.SourceCommit, q.BaseCommit = upstreamOne, baseline }},
		{"diverged as fast-forward", domain.Conflict, func(q *domain.RepositoryMerge) { q.SourceCommit, q.Commit = upstreamOne, upstreamOne }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := submit(f.founder.ID, repo, test.mutate, refs(upstreamOne), main(upstreamOne))
			assertCode(t, err, test.code)
		})
	}
	deliveryGit(t, directory, "checkout", "--quiet", "-B", "reversed", upstreamOne)
	deliveryGit(t, directory, "merge", "--quiet", "--no-ff", "-m", "Reversed parents", internal)
	reversed := deliveryGit(t, directory, "rev-parse", "HEAD")
	_, err = submit(f.founder.ID, repo, func(q *domain.RepositoryMerge) { q.SourceCommit, q.Commit = upstreamOne, reversed }, refs(upstreamOne), main(reversed))
	assertCode(t, err, domain.Invalid)

	deliveryGit(t, directory, "checkout", "--quiet", "-B", "merged", internal)
	deliveryGit(t, directory, "merge", "--quiet", "--no-ff", "-m", "Merge upstream/main into main", upstreamOne)
	merged := deliveryGit(t, directory, "rev-parse", "HEAD")
	clean := func(q *domain.RepositoryMerge) { q.SourceCommit, q.Commit = upstreamOne, merged }
	file := readyFile(t, f, f.founder.ID, upstreamBundle(t, directory, refs(upstreamOne), main(merged)), "", false)
	request := domain.RepositoryMerge{Command: op(), SourceRef: "refs/heads/upstream/main", BaseCommit: internal, BundleFileID: file.ID,
		Reason: "Founder merged reviewed upstream main"}
	request.ExpectedRevision = repo.Revision
	clean(&request)
	repo, err = f.s.UpstreamRepository(ctx, f.founder.ID, repo.ID, request)
	if err != nil {
		t.Fatal("clean two-parent upstream merge was rejected", err)
	}
	if replay, err := f.s.UpstreamRepository(ctx, f.founder.ID, repo.ID, request); err != nil || replay.Revision != repo.Revision {
		t.Fatal("lost upstream import response was not exactly replayable", replay.Revision, err)
	}
	if head, _ := repositoryRef(repo, "refs/heads/main"); head.Commit != merged {
		t.Fatal("default did not advance to the exact merge", repo.Refs)
	}
	if tracking, _ := repositoryRef(repo, "refs/heads/upstream/main"); tracking.Commit != upstreamOne {
		t.Fatal("upstream tracking ref was not recorded", repo.Refs)
	}

	upstreamTwo := upstreamCommit(t, directory, "upstream", upstreamOne, "tool.sh", "#!/bin/sh\nprintf 'upstream\\n'\n", "Upstream tool change")
	_, err = submit(f.founder.ID, repo, func(q *domain.RepositoryMerge) { q.SourceCommit = upstreamOne }, refs(upstreamOne))
	assertCode(t, err, domain.Prerequisite)
	repo, err = submit(f.founder.ID, repo, func(q *domain.RepositoryMerge) { q.SourceCommit = upstreamTwo }, refs(upstreamTwo))
	if err != nil {
		t.Fatal("conflicting upstream tip could not be imported for assigned resolution", err)
	}
	if head, _ := repositoryRef(repo, "refs/heads/main"); head.Commit != merged {
		t.Fatal("import-only conflict changed the protected default", repo.Refs)
	}
	rewritten := upstreamCommit(t, directory, "rewritten", upstreamOne, "README.md", "rewritten\n", "Rewritten upstream")
	_, err = submit(f.founder.ID, repo, func(q *domain.RepositoryMerge) { q.SourceCommit = rewritten }, refs(rewritten))
	assertCode(t, err, domain.Conflict)
	_, err = f.s.PushRepository(ctx, f.founder.ID, repo.ID, domain.RepositoryPush{Command: op(), BundleFileID: file.ID,
		Updates: []domain.GitRefUpdate{{Name: "refs/heads/upstream/main", Old: upstreamTwo, New: rewritten}}})
	if err == nil {
		t.Fatal("ordinary push replaced the protected upstream tracking ref")
	}

	ref := "refs/heads/work/" + f.worker.ID + "/upstream-main"
	task, err := f.s.CreateTask(ctx, f.founder.ID, domain.TaskRequest{Command: op(), ProjectID: project.ID,
		Title: "Resolve upstream merge conflicts", Description: "Merge the imported upstream tip into main",
		AcceptanceCriteria: "Two-parent merge of main and upstream with conflicts resolved",
		Source:             &domain.TaskSource{RepositoryID: repo.ID, Commit: merged, Ref: ref}})
	if err != nil {
		t.Fatal(err)
	}
	task = f.change(t, f.founder.ID, task.ID, "assign", func(q *domain.TaskChange) { q.AgentID = f.worker.ID })
	task = f.change(t, f.worker.ID, task.ID, "workspace", func(q *domain.TaskChange) { q.WorkspaceRef = "workspace_fixture" })
	task = f.change(t, f.worker.ID, task.ID, "status", func(q *domain.TaskChange) { q.Status = domain.Todo })
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.founder.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "checkout", "--quiet", "-B", strings.TrimPrefix(ref, "refs/heads/"), merged)
	if out, err := deliveryGitResult(directory, "merge", "--no-ff", "-m", "Merge upstream/main into main", upstreamTwo); err == nil {
		t.Fatal("fixture upstream change did not conflict", out)
	}
	if err := os.WriteFile(filepath.Join(directory, "tool.sh"), []byte("#!/bin/sh\nprintf 'internal and upstream\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "add", "tool.sh")
	deliveryGit(t, directory, "commit", "--quiet", "--no-edit")
	resolved := deliveryGit(t, directory, "rev-parse", "HEAD")
	work := readyFile(t, f, f.worker.ID, upstreamBundle(t, directory, [2]string{ref, resolved}), "", false)
	if _, err := f.s.PushRepository(ctx, f.worker.ID, repo.ID, domain.RepositoryPush{Command: op(), TaskID: task.ID, RunID: run.ID,
		BundleFileID: work.ID, Updates: []domain.GitRefUpdate{{Name: ref, New: resolved}}}); err != nil {
		t.Fatal("assigned agent could not push its conflict resolution", err)
	}
	report, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "Resolved upstream conflicts", Evidence: []string{resolved}})
	if err != nil {
		t.Fatal(err)
	}
	task = report.Task
	reviewToken := domain.NewToken()
	review := domain.ClaimRequest{Command: op(), Kind: domain.VerificationRun, SpecGeneration: task.SpecGeneration,
		ResultGeneration: task.ResultGeneration, ProjectEpoch: project.Epoch, TokenHash: domain.Hash(reviewToken), Provider: "codex"}
	review.ExpectedRevision = task.Revision
	reviewRun, err := f.s.Claim(ctx, f.other.ID, task.ID, review)
	if err != nil {
		t.Fatal(err)
	}
	mergeFile := readyFile(t, f, f.other.ID, upstreamBundle(t, directory, [2]string{ref, resolved}), "", false)
	repo, err = f.s.MergeRepository(ctx, f.other.ID, repo.ID, domain.RepositoryMerge{Command: op(), TaskID: task.ID, RunID: reviewRun.ID,
		SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration, SourceRef: ref, SourceCommit: resolved,
		BaseCommit: merged, Commit: resolved, BundleFileID: mergeFile.ID, Reason: "Independent reviewer checked the conflict resolution"})
	if err != nil {
		t.Fatal("independent verifier could not integrate the assigned resolution", err)
	}
	source, err := f.s.RepositoryFile(ctx, f.other.ID, repo.ID, resolved, "tool.sh")
	if err != nil || !bytes.Contains(source.Data, []byte("internal and upstream")) {
		t.Fatal("integrated default lacks the resolved content", err)
	}
	var imports int
	if err := f.s.db.QueryRowContext(ctx, "SELECT count(*) FROM histories WHERE source_id=? AND action='repository.upstream'", repo.ID).Scan(&imports); err != nil {
		t.Fatal(err)
	}
	if imports != 2 {
		t.Fatal("upstream imports were not durably audited", imports)
	}
}
