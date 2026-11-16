package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestImprovementNativeBuildCLIUsesExactSourceAndDoesNotRepeatCompiler(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := foundation.OpenWithFiles(ctx, filepath.Join(root, "server", "foundation.sqlite"), foundation.FileStoreOptions{
		Permissions: filestore.PermissionFunc(func(context.Context, string, []uint32) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	founder, err := testidentity.Register(ctx, store, "", "Founder", 4100)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := testidentity.Register(ctx, store, founder.ID, "Builder", 4101)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, founder.ID, domain.ProjectRequest{Command: domain.Command{OperationID: "build-project"}, Name: "Internal tools"})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "original")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated Git: %v %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "--quiet", "-b", "main")
	recipe := domain.BuildRecipe{FormatVersion: 1, Version: "1.0.0", Platform: runtime.GOOS + "-" + runtime.GOARCH, TimeoutSeconds: 30, Profile: "archive",
		Steps:   []domain.BuildStep{{Args: []string{"sh", "-c", "mkdir -p bin; printf 'checked native tool\\n' >bin/tool; chmod 755 bin/tool"}}},
		Outputs: []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}}, Dependencies: []domain.SoftwareDependency{}}
	data, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "build.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "--quiet", "-m", "Initial new internal tool")
	commit := git("rev-parse", "HEAD")
	bundlePath := filepath.Join(root, "source.bundle")
	git("bundle", "create", "--version=2", bundlePath, "refs/heads/main")
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.BeginFile(ctx, founder.ID, domain.FileUploadRequest{Command: domain.Command{OperationID: "build-source-upload"},
		Name: "source.bundle", MediaType: "application/octet-stream", Size: int64(len(bundle)), SHA256: domain.Hash(string(bundle))})
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(bundle); offset += domain.FileChunkSize {
		chunk := bundle[offset:min(len(bundle), offset+domain.FileChunkSize)]
		if _, err := store.PutFileChunk(ctx, founder.ID, file.ID, domain.FileChunkRequest{Command: domain.Command{OperationID: domain.NewID("op")},
			Offset: int64(offset), Data: chunk, SHA256: domain.Hash(string(chunk))}); err != nil {
			t.Fatal(err)
		}
	}
	file, err = store.FinalizeFile(ctx, founder.ID, file.ID, domain.FileChangeRequest{Command: domain.Command{OperationID: "build-source-finalize"}, Reason: "Explicit source"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.CreateRepository(ctx, founder.ID, domain.RepositoryRequest{Command: domain.Command{OperationID: "build-repository"}, ProjectID: project.ID, Name: "new tool", BundleFileID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	product, err := store.CreateSoftwareProduct(ctx, founder.ID, domain.SoftwareProductRequest{Command: domain.Command{OperationID: "build-product"},
		ProjectID: project.ID, RepositoryID: repo.ID, Name: "Native tool", Kind: "cli", Provenance: "internal", RecipePath: "build.json", License: "Original test fixture", Reason: "Register new tool"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, founder.ID, domain.TaskRequest{Command: domain.Command{OperationID: "build-task"}, ProjectID: project.ID,
		Title: "Build exact native tool", Description: "Run the source recipe", AcceptanceCriteria: "Actual output bytes verified",
		Source: &domain.TaskSource{RepositoryID: repo.ID, Commit: commit, ProductID: product.ID}})
	if err != nil {
		t.Fatal(err)
	}
	change := func(actor, kind string, set func(*domain.TaskChange)) {
		t.Helper()
		current, err := store.Task(ctx, actor, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		req := domain.TaskChange{Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: current.Revision}, Kind: kind, Reason: "Exact fixture decision"}
		set(&req)
		task, err = store.ChangeTask(ctx, actor, task.ID, req)
		if err != nil {
			t.Fatal(kind, err)
		}
	}
	change(founder.ID, "assign", func(q *domain.TaskChange) { q.AgentID = worker.ID })
	state, err := local.Open(ctx, filepath.Join(root, "worker", "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binding, err := state.BindWorkspace(ctx, workspace, true, 60, []string{state.Root})
	if err != nil {
		t.Fatal(err)
	}
	change(worker.ID, "workspace", func(q *domain.TaskChange) { q.WorkspaceRef = binding.Ref })
	build, err := store.CreateSoftwareBuild(ctx, founder.ID, domain.SoftwareBuildRequest{Command: domain.Command{OperationID: "build-spec"},
		ProductID: product.ID, TaskID: task.ID, Platform: recipe.Platform})
	if err != nil {
		t.Fatal(err)
	}
	change(worker.ID, "status", func(q *domain.TaskChange) { q.Status = domain.Todo })
	change(worker.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	change(founder.ID, "approve", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	claim := domain.ClaimRequest{Command: domain.Command{OperationID: "native-build-claim", ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}
	run, err := store.Claim(ctx, worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.NewAuthenticated(store, nil, func(*http.Request) (domain.Agent, error) { return worker, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	client := &api.Client{BaseURL: httpServer.URL, HTTP: httpServer.Client(), Credential: config.Credential{AgentID: worker.ID}}
	res := &resources{config: config.DefaultWorker(state.Root), state: state, client: client}
	t.Setenv(workTaskEnvironment, task.ID)
	t.Setenv(workRunEnvironment, run.ID)
	app := &app{operation: "native-build-run", diagnostic: io.Discard}
	result, err := app.runSoftwareBuild(ctx, res, []string{build.ID})
	if err != nil {
		t.Fatal("actual native build CLI failed", err)
	}
	submitted, ok := result.(domain.SoftwareBuild)
	if !ok || submitted.State != "submitted" || submitted.ArtifactID == "" {
		t.Fatal("CLI returned metadata rather than an actual build result", result)
	}
	var archive bytes.Buffer
	if _, err := client.DownloadArtifact(ctx, submitted.ArtifactID, &archive, false); err != nil {
		t.Fatal(err)
	}
	if archive.Len() == 0 {
		t.Fatal("no native release bytes were produced")
	}
	if _, err := app.runSoftwareBuild(ctx, res, []string{build.ID}); err != nil {
		t.Fatal("completed child replay attempted another compiler/output overwrite", err)
	}
}
