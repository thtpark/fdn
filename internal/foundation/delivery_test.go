package foundation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/upgrade"
)

func deliveryGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func deliverySource(t *testing.T, kinds ...string) (string, string, []byte) {
	t.Helper()
	directory := t.TempDir()
	deliveryGit(t, directory, "init", "--quiet", "-b", "main")
	recipe := domain.BuildRecipe{FormatVersion: 1, Version: "1.0.0", Platform: runtime.GOOS + "-" + runtime.GOARCH,
		TimeoutSeconds: 30, Profile: "archive", Steps: []domain.BuildStep{{Args: []string{"sh", "-c", "mkdir -p bin; printf 'checked tool\\n' > bin/tool"}}},
		Outputs: []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}}, Dependencies: []domain.SoftwareDependency{}}
	if len(kinds) > 0 && kinds[0] == "foundation" {
		recipe.Outputs = nil
		for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
			recipe.Outputs = append(recipe.Outputs, domain.BuildOutput{Path: "bin/" + name, Mode: 0755})
		}
		recipe.Steps[0].Args[2] = "mkdir -p bin; for name in agent-cli agent-service agent-identity fdn-service; do printf 'checked tool\\n' > bin/$name; done"
	}
	raw, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "build.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tool.sh"), []byte("#!/bin/sh\nprintf 'baseline\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "add", ".")
	deliveryGit(t, directory, "commit", "--quiet", "-m", "Initial internal source")
	commit := deliveryGit(t, directory, "rev-parse", "HEAD")
	bundlePath := filepath.Join(t.TempDir(), "initial.bundle")
	deliveryGit(t, directory, "bundle", "create", "--version=2", bundlePath, "refs/heads/main")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	return directory, commit, data
}

func deliveryKey(t *testing.T) ([]byte, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
}

func TestImprovementHostedGitIndependentIntegrationAndCustody(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	project, err := f.s.Project(ctx, f.founder.ID, task.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.GrantRequest{Command: op(), AgentID: f.other.ID}
	grant.ExpectedRevision = project.Revision
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, grant)
	if err != nil {
		t.Fatal(err)
	}
	directory, baseline, data := deliverySource(t)
	file := readyFile(t, f, f.founder.ID, data, "", false)
	repo, err := f.s.CreateRepository(ctx, f.founder.ID, domain.RepositoryRequest{Command: op(), ProjectID: project.ID,
		Name: "internal", DefaultBranch: "main", BundleFileID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.s.File(ctx, f.founder.ID, file.ID)
	if err != nil || stored.RegistryID == "" {
		t.Fatal("source submission did not transfer immutable custody", stored, err)
	}
	_, err = f.s.DeleteFile(ctx, f.founder.ID, file.ID, domain.FileChangeRequest{Command: op(), Reason: "Cannot remove retained release source"})
	assertCode(t, err, domain.Denied)
	unrelated := f.enroll(t, "unrelated")
	_, err = f.s.Repository(ctx, unrelated.ID, repo.ID)
	assertCode(t, err, domain.Denied)
	ref := "refs/heads/work/" + f.worker.ID + "/feature"
	task = f.change(t, f.founder.ID, task.ID, "source", func(q *domain.TaskChange) {
		q.Source = &domain.TaskSource{RepositoryID: repo.ID, Commit: baseline, Ref: ref}
	})
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.founder.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "checkout", "--quiet", "-b", strings.TrimPrefix(ref, "refs/heads/"))
	if err := os.WriteFile(filepath.Join(directory, "tool.sh"), []byte("#!/bin/sh\nprintf 'feature\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, directory, "add", "tool.sh")
	deliveryGit(t, directory, "commit", "--quiet", "-m", "Implement internal feature")
	head := deliveryGit(t, directory, "rev-parse", "HEAD")
	bundlePath := filepath.Join(t.TempDir(), "work.bundle")
	deliveryGit(t, directory, "bundle", "create", "--version=2", bundlePath, ref)
	workBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	workFile := readyFile(t, f, f.worker.ID, workBytes, "", false)
	push := domain.RepositoryPush{Command: op(), TaskID: task.ID, RunID: run.ID, BundleFileID: workFile.ID,
		Updates: []domain.GitRefUpdate{{Name: ref, New: head}}}
	bad := push
	bad.Command = op()
	bad.Updates = []domain.GitRefUpdate{{Name: "refs/heads/main", Old: baseline, New: head}}
	_, err = f.s.PushRepository(ctx, f.worker.ID, repo.ID, bad)
	assertCode(t, err, domain.Denied)
	if _, err := f.s.PushRepository(ctx, f.worker.ID, repo.ID, push); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PushRepository(ctx, f.worker.ID, repo.ID, push); err != nil {
		t.Fatal("lost push response was not exactly replayable", err)
	}
	report, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "Actual checked source pushed", Evidence: []string{head}})
	if err != nil || report.Task.ResultSource == nil || report.Task.ResultSource.Commit != head {
		t.Fatal("completion did not pin actual source", report, err)
	}
	task = report.Task
	verify := domain.TaskChange{Command: op(), Kind: "verify", SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration,
		Decision: "accept", Reason: "Cannot accept unmerged source"}
	verify.ExpectedRevision = task.Revision
	_, err = f.s.ChangeTask(ctx, f.other.ID, task.ID, verify)
	assertCode(t, err, domain.Prerequisite)
	reviewToken := domain.NewToken()
	review := domain.ClaimRequest{Command: op(), Kind: domain.VerificationRun, SpecGeneration: task.SpecGeneration,
		ResultGeneration: task.ResultGeneration, ProjectEpoch: project.Epoch, TokenHash: domain.Hash(reviewToken), Provider: "codex"}
	review.ExpectedRevision = task.Revision
	reviewRun, err := f.s.Claim(ctx, f.other.ID, task.ID, review)
	if err != nil {
		t.Fatal(err)
	}
	mergeFile := readyFile(t, f, f.other.ID, workBytes, "", false)
	merge := domain.RepositoryMerge{Command: op(), TaskID: task.ID, RunID: reviewRun.ID, SpecGeneration: task.SpecGeneration,
		ResultGeneration: task.ResultGeneration, SourceRef: ref, SourceCommit: head, BaseCommit: baseline, Commit: head,
		BundleFileID: mergeFile.ID, Reason: "Independent reviewer checked exact fast-forward"}
	repo, err = f.s.MergeRepository(ctx, f.other.ID, repo.ID, merge)
	if err != nil {
		t.Fatal(err)
	}
	report, err = f.s.Report(ctx, f.other.ID, task.ID, domain.ReportRequest{Command: op(), RunID: reviewRun.ID, Token: reviewToken,
		SpecGeneration: task.SpecGeneration, ResultGeneration: task.ResultGeneration, Outcome: "completed",
		Decision: "accept", Summary: "Independently verified and integrated", Evidence: []string{head}})
	if err != nil || report.Task.Status != domain.Done {
		t.Fatal("independent actual integration did not accept source", report, err)
	}
	source, err := f.s.RepositoryFile(ctx, f.other.ID, repo.ID, head, "tool.sh")
	if err != nil || !bytes.Contains(source.Data, []byte("feature")) {
		t.Fatal("hosted source differs from integrated content", err)
	}
}

func TestImprovementInternalBuildReleaseAndVendorBoundary(t *testing.T) {
	for _, kind := range []string{"cli", "foundation"} {
		t.Run(kind, func(t *testing.T) {
			testImprovementInternalBuildReleaseAndVendorBoundary(t, kind)
		})
	}
}

func testImprovementInternalBuildReleaseAndVendorBoundary(t *testing.T, kind string) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	_, commit, source := deliverySource(t, kind)
	file := readyFile(t, f, f.founder.ID, source, "", false)
	repo, err := f.s.CreateRepository(ctx, f.founder.ID, domain.RepositoryRequest{Command: op(), ProjectID: task.ProjectID,
		Name: "existing internal tool", BundleFileID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	product, err := f.s.CreateSoftwareProduct(ctx, f.founder.ID, domain.SoftwareProductRequest{Command: op(), ProjectID: task.ProjectID,
		RepositoryID: repo.ID, Name: "internal CLI", Kind: kind, Provenance: "internal", RecipePath: "build.json",
		License: "Original fixture source; authorized internal distribution", Reason: "Register approved source and owner"})
	if err != nil {
		t.Fatal(err)
	}
	task = f.change(t, f.founder.ID, task.ID, "source", func(q *domain.TaskChange) {
		q.Source = &domain.TaskSource{RepositoryID: repo.ID, Commit: commit, ProductID: product.ID}
	})
	build, err := f.s.CreateSoftwareBuild(ctx, f.founder.ID, domain.SoftwareBuildRequest{Command: op(), ProductID: product.ID,
		TaskID: task.ID, Platform: runtime.GOOS + "-" + runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	task, err = f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil || task.Readiness != nil || build.SpecGeneration != task.SpecGeneration {
		t.Fatal("build changed inputs without invalidating readiness", err)
	}
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	task = f.change(t, f.founder.ID, task.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: op(), SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	claim.ExpectedRevision = task.Revision
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "A sentence is not a build"})
	assertCode(t, err, domain.Prerequisite)
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range build.Recipe.Outputs {
		if err := os.WriteFile(filepath.Join(root, entry.Path), []byte("checked tool\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	var payload bytes.Buffer
	if _, err := software.CreateBundle(&payload, root, build.Recipe.Outputs, f.now.Unix()); err != nil {
		t.Fatal(err)
	}
	output := readyFile(t, f, f.worker.ID, payload.Bytes(), "", false)
	build, err = f.s.SubmitSoftwareBuild(ctx, f.worker.ID, build.ID, domain.SoftwareBuildResult{Command: op(), RunID: run.ID,
		BundleFileID: output.ID, Toolchain: "Synthetic native fixture", Summary: "Exact output bytes verified"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*domain.SoftwareBuild)
	}{
		{"foreign-task", func(b *domain.SoftwareBuild) { b.TaskID = "foreign" }},
		{"foreign-source", func(b *domain.SoftwareBuild) { b.Source.Commit = strings.Repeat("0", 40) }},
		{"foreign-repository", func(b *domain.SoftwareBuild) { b.Source.RepositoryID = "foreign" }},
		{"foreign-product", func(b *domain.SoftwareBuild) { b.ProductID = "foreign" }},
		{"foreign-spec", func(b *domain.SoftwareBuild) { b.SpecGeneration++ }},
		{"foreign-builder", func(b *domain.SoftwareBuild) { b.BuilderID = f.other.ID }},
		{"prepared", func(b *domain.SoftwareBuild) { b.State = "prepared" }},
		{"missing-artifact", func(b *domain.SoftwareBuild) { b.ArtifactID = "" }},
		{"missing-original-run", func(b *domain.SoftwareBuild) { b.RunID = "missing" }},
		{"missing-artifact-record", func(b *domain.SoftwareBuild) { b.ArtifactID = "missing" }},
		{"unverified-payload", func(b *domain.SoftwareBuild) { b.Payloads = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			altered := build
			test.edit(&altered)
			raw, err := encode(altered)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE software_builds SET data=? WHERE id=?", raw, build.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.s.validateSubmittedBuild(ctx, tx, task, run, f.worker); err == nil {
				t.Fatal("invalid submitted provenance accepted")
			}
		})
	}
	for _, test := range []struct {
		name string
		edit func(*domain.Run)
	}{
		{"foreign-original-task", func(r *domain.Run) { r.TaskID = "foreign" }},
		{"foreign-original-builder", func(r *domain.Run) { r.AgentID = f.other.ID }},
		{"foreign-original-spec", func(r *domain.Run) { r.SpecGeneration++ }},
		{"original-verifier", func(r *domain.Run) { r.Kind = domain.VerificationRun }},
		{"unaccepted-original", func(r *domain.Run) { r.Attempt = 0 }},
		{"stale-original", func(r *domain.Run) { r.State = "stale" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			original := run
			test.edit(&original)
			if err := f.s.saveRun(ctx, tx, original); err != nil {
				t.Fatal(err)
			}
			if err := f.s.validateSubmittedBuild(ctx, tx, task, run, f.worker); err == nil {
				t.Fatal("foreign original provenance accepted")
			}
		})
	}
	for _, test := range []struct {
		name string
		edit func(*domain.Run, *domain.Agent)
	}{
		{"expired-admission", func(r *domain.Run, _ *domain.Agent) { r.DeadlineAt = f.now.Unix() }},
		{"foreign-current-task", func(r *domain.Run, _ *domain.Agent) { r.TaskID = "foreign" }},
		{"foreign-current-builder", func(r *domain.Run, _ *domain.Agent) { r.AgentID = f.other.ID }},
		{"old-current-spec", func(r *domain.Run, _ *domain.Agent) { r.SpecGeneration-- }},
		{"old-current-admission", func(r *domain.Run, _ *domain.Agent) { r.State = "stopped" }},
		{"verifier-takeover", func(r *domain.Run, _ *domain.Agent) { r.Kind = domain.VerificationRun }},
		{"paused-mode", func(_ *domain.Run, a *domain.Agent) { a.Mode = "manual" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			current, actor := run, f.worker
			test.edit(&current, &actor)
			if err := f.s.validateSubmittedBuild(ctx, f.s.db, task, current, actor); err == nil {
				t.Fatal("invalid current admission reused submitted custody")
			}
		})
	}
	stopped, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "stopped", Summary: "Joined provider; submitted custody inspected"})
	if err != nil {
		t.Fatal(err)
	}
	task = stopped.Task
	if !task.UnsafeRetry || task.ActiveRun != nil || task.Attempts != 1 || task.Failures != 0 || task.NextAttemptAt != 0 {
		t.Fatal("joined stopped run lost its unsafe fence or counters", task)
	}
	claim.Command = op()
	claim.ExpectedRevision = task.Revision
	if _, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("submitted custody bypassed exact unsafe retry approval", err)
	}
	if _, err := f.s.ChangeTask(ctx, f.worker.ID, task.ID, domain.TaskChange{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
		Kind:    "clarity", SpecGeneration: task.SpecGeneration, Reason: "Inspect submitted custody",
	}); err == nil || !strings.Contains(err.Error(), "unsafe-retry approval") {
		t.Fatal("clarity cleared unsafe retry before exact approval", err)
	}
	task = f.change(t, f.founder.ID, task.ID, "retry-approve", func(q *domain.TaskChange) {
		q.SpecGeneration, q.RunID, q.Attempt = task.SpecGeneration, run.ID, 2
	})
	task = f.change(t, f.worker.ID, task.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	claim.Command = op()
	claim.ExpectedRevision = task.Revision
	current, err := f.s.Claim(ctx, f.worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	buildCount, artifactCount := 0, 0
	if err := f.s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM software_builds").Scan(&buildCount); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM delivery_artifacts").Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	report := domain.ReportRequest{Command: op(), RunID: current.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "Actual archive submitted", Evidence: []string{build.ArtifactID}}
	result, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Result.RunID != current.ID || result.Task.Attempts != 2 || result.Task.Failures != 0 ||
		!strings.Contains(strings.Join(result.Task.Result.Evidence, " "), "@"+run.ID) {
		t.Fatal("recovery lost original provenance or current admission counters", result)
	}
	replayed, err := f.s.Report(ctx, f.worker.ID, task.ID, report)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatal("report replay changed the result", err)
	}
	retained, err := f.s.SoftwareBuild(ctx, f.worker.ID, build.ID)
	if err != nil || !reflect.DeepEqual(retained, build) {
		t.Fatal("recovery mutated immutable build provenance", retained, err)
	}
	runs, err := f.s.Runs(ctx, f.worker.ID, task.ID)
	if err != nil || len(runs) != 2 {
		t.Fatal("recovery created extra executions", runs, err)
	}
	for _, retained := range runs {
		if retained.ID == run.ID && (retained.State != "stopped" || retained.Attempt != 1) ||
			retained.ID == current.ID && (retained.State != "completed" || retained.Attempt != 2) {
			t.Fatal("recovery changed retained execution provenance", retained)
		}
	}
	for _, count := range []struct {
		query string
		want  int
	}{{"SELECT COUNT(*) FROM software_builds", buildCount}, {"SELECT COUNT(*) FROM delivery_artifacts", artifactCount}} {
		var got int
		if err := f.s.db.QueryRowContext(ctx, count.query).Scan(&got); err != nil || got != count.want {
			t.Fatal("recovery duplicated immutable custody", got, count.want, err)
		}
	}
	task = result.Task
	key, public := deliveryKey(t)
	sign := func(manifest domain.SoftwareManifest) (string, string, error) {
		return software.SignRelease(manifest, key)
	}
	releaseReq := domain.SoftwareReleaseRequest{Command: op(), ProductID: product.ID, BuildIDs: []string{build.ID}, Reason: "Publish only after independent verification"}
	_, err = f.s.CreateSoftwareRelease(ctx, f.founder.ID, releaseReq, sign)
	assertCode(t, err, domain.Denied)
	task = f.change(t, f.founder.ID, task.ID, "verify", func(q *domain.TaskChange) {
		q.SpecGeneration, q.ResultGeneration, q.Decision = task.SpecGeneration, task.ResultGeneration, "accept"
	})
	releaseReq.Command = op()
	release, err := f.s.CreateSoftwareRelease(ctx, f.founder.ID, releaseReq, sign)
	if err != nil {
		t.Fatal(err)
	}
	if err := software.VerifyRelease(release, public); err != nil {
		t.Fatal("separate artifact signature did not verify", err)
	}
	replay, err := f.s.CreateSoftwareRelease(ctx, f.founder.ID, releaseReq, sign)
	if err != nil || replay.ID != release.ID || replay.Manifest.Revision != 1 {
		t.Fatal("publication replay replaced an immutable release", err)
	}
	vendor, err := f.s.CreateSoftwareProduct(ctx, f.founder.ID, domain.SoftwareProductRequest{Command: op(), ProjectID: task.ProjectID,
		Name: "vendor managed", Kind: "cli", Provenance: "vendor", License: "Vendor terms retained", Reason: "Observation only"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CreateSoftwareBuild(ctx, f.founder.ID, domain.SoftwareBuildRequest{Command: op(), ProductID: vendor.ID,
		TaskID: task.ID, Platform: runtime.GOOS + "-" + runtime.GOARCH})
	assertCode(t, err, domain.Prerequisite)
	ca, err := pki.Generate(f.now)
	if err != nil {
		t.Fatal(err)
	}
	control, err := f.s.RequestSoftwarePreflight(ctx, f.founder.ID, domain.SoftwarePreflightRequest{Command: op(),
		TargetID: f.worker.ID, BindingID: f.worker.ID, ProductID: product.ID,
		Native: domain.SoftwareNativeRequest{Action: "challenge", ID: "native_tool_install", ProductID: product.ID}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := f.s.UpgradeContext(ctx, f.founder.ID)
	if err != nil {
		t.Fatal(err)
	}
	challenge := domain.SoftwareChallenge{ID: control.Request.Native.ID, Nonce: domain.NewToken(),
		InstanceID: instance.InstanceID, TargetID: f.worker.ID, BindingID: f.worker.ID, Generation: control.TargetGeneration,
		ProductID: product.ID, Platform: build.Recipe.Platform, State: "absent", PolicySHA256: domain.Hash("exact native policy"),
		ExpiresAt: f.now.Unix() + 900}
	var bridge *upgrade.SoftwareBridge
	var native *deliveryNativeRunner
	if kind == "foundation" {
		bridge, native = pairedDeliveryBridge(t, f, ca, public, release, payload.Bytes(), control.TargetGeneration)
		reply, err := bridge.Handle(ctx, control.Request.Native)
		if err != nil || reply.Challenge == nil {
			t.Fatal("paired Agent native challenge failed", reply, err)
		}
		challenge = *reply.Challenge
		if challenge.CurrentVersion != "0.3.2" {
			t.Fatal("paired Agent challenge did not observe the original managed source", challenge)
		}
	}
	if _, err := f.s.ReportSoftwarePreflight(ctx, f.other.ID, false, domain.SoftwarePreflightReport{Command: op(), ID: control.ID,
		Result: domain.SoftwareNativeReply{State: "planned", Challenge: &challenge}}); err == nil {
		t.Fatal("unrelated actor forged another computer's native observation")
	}
	if _, err := f.s.ReportSoftwarePreflight(ctx, f.worker.ID, false, domain.SoftwarePreflightReport{Command: op(), ID: control.ID,
		Result: domain.SoftwareNativeReply{State: "planned", Challenge: &challenge}}); err != nil {
		t.Fatal(err)
	}
	batch, err := f.s.PauseSoftwareMaintenance(ctx, f.founder.ID, domain.SoftwareMaintenanceRequest{Command: op(),
		ID: "software_batch_fixture", PreflightIDs: []string{control.ID}, Reason: "Exact approved rollout"})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	authorize := domain.SoftwareAuthorizationRequest{Command: op(), PreflightID: control.ID, MaintenanceID: batch.ID,
		Proposal: domain.SoftwareProposal{Challenge: challenge, Release: release, BackupRecipient: recovery.Recipient().String(),
			Reason: "Authorize exact source and target, not discovery"}}
	for name, change := range map[string]func(*domain.SoftwareAuthorizationRequest){
		"missing-binding":  func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.BindingID = "" },
		"foreign-binding":  func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.BindingID = f.other.ID },
		"foreign-instance": func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.InstanceID = "instance_foreign" },
		"foreign-target":   func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.TargetID = f.other.ID },
		"generation":       func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.Generation++ },
		"product":          func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.ProductID = vendor.ID },
		"policy": func(r *domain.SoftwareAuthorizationRequest) {
			r.Proposal.Challenge.PolicySHA256 = domain.Hash("foreign")
		},
		"expiry": func(r *domain.SoftwareAuthorizationRequest) { r.Proposal.Challenge.ExpiresAt = f.now.Unix() },
		"source": func(r *domain.SoftwareAuthorizationRequest) {
			r.Proposal.Release.Manifest.Source.Commit = strings.Repeat("f", 40)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := authorize
			changed.Command = op()
			change(&changed)
			signed := false
			if _, err := f.s.AuthorizeSoftware(ctx, f.founder.ID, changed, func(permit domain.SoftwarePermit) (string, error) {
				signed = true
				return software.SignPermit(permit, ca.CAKey)
			}); err == nil || signed {
				t.Fatal("changed proposal reached signing or authorization", err)
			}
		})
	}
	deployment, err := f.s.AuthorizeSoftware(ctx, f.founder.ID, authorize, func(permit domain.SoftwarePermit) (string, error) {
		return software.SignPermit(permit, ca.CAKey)
	})
	if err != nil || deployment.State != "queued" {
		t.Fatal("native submission did not remain queued", deployment, err)
	}
	replayedDeployment, err := f.s.AuthorizeSoftware(ctx, f.founder.ID, authorize, func(permit domain.SoftwarePermit) (string, error) {
		return software.SignPermit(permit, ca.CAKey)
	})
	if err != nil || replayedDeployment.ID != deployment.ID || replayedDeployment.Permit.ExpiresAt != deployment.Permit.ExpiresAt {
		t.Fatal("lost install submission renewed or replaced authority", err)
	}
	if bridge != nil {
		for name, change := range map[string]func(*domain.SoftwarePermit){
			"binding":    func(p *domain.SoftwarePermit) { p.Proposal.Challenge.BindingID = f.other.ID },
			"generation": func(p *domain.SoftwarePermit) { p.Proposal.Challenge.Generation++ },
			"instance":   func(p *domain.SoftwarePermit) { p.Proposal.Challenge.InstanceID = "instance_foreign" },
			"target":     func(p *domain.SoftwarePermit) { p.Proposal.Challenge.TargetID = f.other.ID },
			"product":    func(p *domain.SoftwarePermit) { p.Proposal.Challenge.ProductID = vendor.ID },
			"policy":     func(p *domain.SoftwarePermit) { p.Proposal.Challenge.PolicySHA256 = domain.Hash("foreign") },
			"nonce":      func(p *domain.SoftwarePermit) { p.Proposal.Challenge.Nonce = domain.NewToken() },
			"source":     func(p *domain.SoftwarePermit) { p.Proposal.Release.Manifest.Source.Commit = strings.Repeat("f", 40) },
			"expiry": func(p *domain.SoftwarePermit) {
				p.IssuedAt = f.now.Unix() - 1
				p.ExpiresAt = f.now.Unix()
			},
			"signature": func(p *domain.SoftwarePermit) { p.Signature = "invalid" },
			"role":      func(p *domain.SoftwarePermit) { p.Proposal.Release.Manifest.Kind = "cli" },
		} {
			t.Run("native-"+name, func(t *testing.T) {
				changed := deployment.Permit
				change(&changed)
				switch name {
				case "binding", "generation", "policy", "nonce", "source", "expiry", "target", "role":
					changed.Signature, err = software.SignPermit(changed, ca.CAKey)
					if err != nil {
						t.Fatal(err)
					}
				}
				before := native.effects
				_, err := bridge.Handle(ctx, domain.SoftwareNativeRequest{Action: "prepare", ID: challenge.ID, ProductID: product.ID, Permit: &changed})
				if err == nil || native.effects != before {
					t.Fatal("invalid authority reached native effects", err)
				}
			})
		}
		request := domain.SoftwareNativeRequest{Action: "prepare", ID: challenge.ID, ProductID: product.ID, Permit: &deployment.Permit}
		native.generation++
		before := native.effects
		if _, err := bridge.Handle(ctx, request); err == nil || native.effects != before {
			t.Fatal("replaced current identity reached native effects", err)
		}
		native.generation--
		bridge.Native.Policy.Role = "foundation"
		if _, err := bridge.Handle(ctx, request); err == nil || native.effects != before {
			t.Fatal("Agent permit acquired Foundation native role", err)
		}
		bridge.Native.Policy.Role = "agent"
		if reply, err := bridge.Handle(ctx, request); err != nil || reply.State != "prepared" {
			t.Fatal("signed core-Agent permit did not prepare", reply, err)
		}
		request.Action = "apply"
		reply, err := bridge.Handle(ctx, request)
		if err != nil || reply.State != "verified" || reply.Result == nil {
			t.Fatal("paired Agent install did not verify", reply, err)
		}
		before = native.effects
		replay, err := bridge.Handle(ctx, request)
		if err != nil || !reflect.DeepEqual(replay, reply) || native.effects != before {
			t.Fatal("lost native response repeated effects", replay, err)
		}
		if _, err := f.s.ReportSoftware(ctx, f.worker.ID, false, domain.SoftwareResultRequest{Command: op(), Result: *reply.Result}); err != nil {
			t.Fatal("actual paired receipt rejected", err)
		}
		if err := bridge.AcknowledgeResult(deployment.ID, domain.Hash(deployment.Permit.Signature)); err != nil {
			t.Fatal(err)
		}
		return
	}
	outputPlatform := release.Manifest.Platforms[0]
	nativeResult := domain.SoftwareResultRequest{Command: op(), Result: domain.SoftwareResult{PermitID: deployment.ID,
		PermitSHA256: domain.Hash(deployment.Permit.Signature), ProductID: product.ID, ReleaseID: release.ID,
		ArtifactID: outputPlatform.ArtifactID, SHA256: outputPlatform.SHA256, BackupSHA256: domain.Hash("encrypted cold snapshot"),
		State: "verified", ResumeSafe: true}}
	verified, err := f.s.ReportSoftware(ctx, f.worker.ID, false, nativeResult)
	if err != nil || verified.State != "verified" {
		t.Fatal("native receipt did not verify exact bytes", verified, err)
	}
	agent, err := f.s.Me(ctx, f.worker.ID)
	if err != nil || agent.Mode != "ai" {
		t.Fatal("unchanged prior AI intent did not restore after exact native verification", agent, err)
	}
	if _, err := f.s.ReportSoftware(ctx, f.worker.ID, false, nativeResult); err != nil {
		t.Fatal("result replay repeated or changed native mode effects", err)
	}
}

type deliveryNativeRunner struct {
	policy     upgrade.Policy
	release    domain.SoftwareRelease
	generation int64
	effects    int
}

func (r *deliveryNativeRunner) Run(_ context.Context, spec process.Spec) error {
	reply := func(value any) error {
		return json.NewEncoder(spec.Stdout).Encode(map[string]any{"ok": true, "data": value})
	}
	args := strings.Join(spec.Args, " ")
	switch {
	case args == "--version":
		root := filepath.Join(r.policy.Prefix, "lib", "fdn")
		target, err := os.Readlink(filepath.Join(root, "current"))
		if err != nil {
			return err
		}
		version, commit := "0.3.2", strings.Repeat("b", 40)
		switch target {
		case filepath.Join(root, "fdn-original"):
		case filepath.Join(root, "internal-"+r.release.ID):
			version, commit = r.release.Manifest.Version, r.release.Manifest.Source.Commit
		default:
			return fmt.Errorf("unexpected paired fixture activation target: %s", target)
		}
		return reply(map[string]string{"version": version, "commit": commit})
	case strings.Contains(args, "agent identity"):
		return reply(domain.AgentIdentity{AgentID: r.policy.TargetID, InstanceID: r.policy.InstanceID, Generation: r.generation, State: "active"})
	case strings.Contains(args, "work status"):
		return reply(map[string]any{"pending_invocations": []string{}, "session_fence": map[string]bool{"active": false},
			"work": map[string]any{"mode": "manual", "pending_reports": 0}})
	case spec.Path == "/usr/bin/tar":
		r.effects++
		_, err := io.WriteString(spec.Stdout, "isolated cold fixture retaining identity and journals")
		return err
	case spec.Path == "/usr/bin/systemctl" || spec.Path == "/bin/launchctl":
		r.effects++
		return nil
	default:
		return fmt.Errorf("unexpected paired fixture command: %s", spec.Path)
	}
}

func pairedDeliveryBridge(t *testing.T, f *fixture, ca pki.Bundle, public []byte, release domain.SoftwareRelease, payload []byte, generation int64) (*upgrade.SoftwareBridge, *deliveryNativeRunner) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(account.HomeDir, ".fdn-delivery-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for _, name := range []string{"prefix", "prefix/bin", "worker", "identity"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	policy := upgrade.Policy{Role: "agent", Name: "fixture", InstanceID: release.Manifest.InstanceID, TargetID: f.worker.ID,
		User: account.Username, Home: account.HomeDir, Method: "archive", Prefix: filepath.Join(root, "prefix"),
		BinaryDir: filepath.Join(root, "prefix/bin"), CAFile: filepath.Join(root, "ca.pem"), StateDir: filepath.Join(root, "updater"),
		ConfigFile: filepath.Join(root, "worker.json"), IdentityConfig: filepath.Join(root, "identity.json"),
		ServiceOptions: filepath.Join(root, "services.json"), BackupPaths: []string{filepath.Join(root, "worker"), filepath.Join(root, "identity")},
		EnableInternalSoftware: true, InternalProductID: release.Manifest.ProductID, DistributionPublicKeyFile: filepath.Join(root, "distribution.pem")}
	for path, content := range map[string][]byte{policy.CAFile: ca.CA, policy.DistributionPublicKeyFile: public,
		policy.ServiceOptions: []byte("{}"), filepath.Join(root, "worker/work.lock"): []byte("{}")} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Write(policy.ConfigFile, config.DefaultWorker(filepath.Join(root, "worker"))); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(policy.IdentityConfig, config.IdentityService{StateDir: filepath.Join(root, "identity"), Socket: filepath.Join(root, "identity.sock")}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(policy.Prefix, "lib", "fdn")
	original := filepath.Join(archive, "fdn-original")
	if err := os.MkdirAll(filepath.Join(original, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, filepath.Join(archive, "current")); err != nil {
		t.Fatal(err)
	}
	for _, entry := range release.Manifest.Platforms[0].Payloads {
		if err := os.WriteFile(filepath.Join(original, entry.Path), []byte("original"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(archive, "current", entry.Path), filepath.Join(policy.Prefix, entry.Path)); err != nil {
			t.Fatal(err)
		}
	}
	runner := &deliveryNativeRunner{policy: policy, release: release, generation: generation}
	n := &upgrade.Native{Policy: policy, Runner: runner, Now: func() time.Time { return f.now },
		CheckServices: func(context.Context) error { return nil }}
	bridge := &upgrade.SoftwareBridge{Native: n, Trust: upgrade.SoftwareBridgeTrust{
		VerifyPermit: software.VerifyPermit, VerifyAdmittedPermit: software.VerifyAdmittedPermit, VerifyRelease: software.VerifyRelease, Extract: software.ExtractBundle,
		Fetch: func(_ context.Context, id, path string) error {
			if id != release.Manifest.Platforms[0].ArtifactID {
				return fmt.Errorf("foreign artifact")
			}
			return os.WriteFile(path, payload, 0600)
		},
		Lease: func(ctx context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
			return f.s.LeaseSoftware(ctx, f.worker.ID, false, request, func(lease domain.UpgradeLease) (string, error) { return upgrade.SignLease(lease, ca.CAKey) })
		},
	}}
	return bridge, runner
}
