package foundation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/software"
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

func deliverySource(t *testing.T) (string, string, []byte) {
	t.Helper()
	directory := t.TempDir()
	deliveryGit(t, directory, "init", "--quiet", "-b", "main")
	recipe := domain.BuildRecipe{FormatVersion: 1, Version: "1.0.0", Platform: runtime.GOOS + "-" + runtime.GOARCH,
		TimeoutSeconds: 30, Profile: "archive", Steps: []domain.BuildStep{{Args: []string{"sh", "-c", "mkdir -p bin; printf 'checked tool\\n' > bin/tool"}}},
		Outputs: []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}}, Dependencies: []domain.SoftwareDependency{}}
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
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	_, commit, source := deliverySource(t)
	file := readyFile(t, f, f.founder.ID, source, "", false)
	repo, err := f.s.CreateRepository(ctx, f.founder.ID, domain.RepositoryRequest{Command: op(), ProjectID: task.ProjectID,
		Name: "existing internal tool", BundleFileID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	product, err := f.s.CreateSoftwareProduct(ctx, f.founder.ID, domain.SoftwareProductRequest{Command: op(), ProjectID: task.ProjectID,
		RepositoryID: repo.ID, Name: "internal CLI", Kind: "cli", Provenance: "internal", RecipePath: "build.json",
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
	if err := os.WriteFile(filepath.Join(root, "bin", "tool"), []byte("checked tool\n"), 0755); err != nil {
		t.Fatal(err)
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
	result, err := f.s.Report(ctx, f.worker.ID, task.ID, domain.ReportRequest{Command: op(), RunID: run.ID, Token: token,
		SpecGeneration: task.SpecGeneration, Outcome: "completed", Summary: "Actual archive submitted", Evidence: []string{build.ArtifactID}})
	if err != nil {
		t.Fatal(err)
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
	ca, err := pki.Generate(f.now)
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
