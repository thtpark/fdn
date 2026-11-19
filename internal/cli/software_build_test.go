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
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/testidentity"
)

func TestImprovementNativeBuildCLIUsesExactSourceAndDoesNotRepeatCompiler(t *testing.T) {
	host := runtime.GOOS + "-" + runtime.GOARCH
	target := "linux-amd64"
	if target == host {
		target = "darwin-arm64"
	}
	for _, test := range []struct {
		name, platform string
		capabilities   []string
		allowed        bool
	}{
		{"native", host, nil, true},
		{"cross-denied-by-default", target, nil, false},
		{"cross-exact-opt-in", target, []string{"cross-build:" + host + ":" + target}, true},
		{"cross-wrong-direction", target, []string{"cross-build:" + target + ":" + host}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			testImprovementBuildCLI(t, test.platform, test.capabilities, test.allowed, "")
		})
	}
}

func TestImprovementNativeBuildFailedJournalDoesNotRepeatCompiler(t *testing.T) {
	testImprovementBuildCLI(t, runtime.GOOS+"-"+runtime.GOARCH, nil, true, "printf attempted >> attempts; printf private-failure >&2; exit 9")
}

func testImprovementBuildCLI(t *testing.T, target string, capabilities []string, allowed bool, failingStep string) {
	t.Helper()
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
	recipe := domain.BuildRecipe{FormatVersion: 1, Version: "1.0.0", Platform: target, TimeoutSeconds: 30, Profile: "archive",
		Steps:   []domain.BuildStep{{Args: []string{"sh", "-c", "mkdir -p bin; printf 'checked native tool\\n' >bin/tool; chmod 755 bin/tool"}}},
		Outputs: []domain.BuildOutput{{Path: "bin/tool", Mode: 0755}}, Dependencies: []domain.SoftwareDependency{}}
	if failingStep != "" {
		recipe.Steps[0].Args = []string{"sh", "-c", failingStep}
	}
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
		ProjectID: project.ID, RepositoryID: repo.ID, Name: "Native tool", Kind: "cli", Provenance: "internal", RecipePath: "build.json",
		Capabilities: capabilities, License: "Original test fixture", Reason: "Register new tool"})
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
	token := domain.NewToken()
	claim := domain.ClaimRequest{Command: domain.Command{OperationID: "native-build-claim", ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(token), Provider: "codex"}
	run, err := store.Claim(ctx, worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.NewAuthenticated(store, nil, func(*http.Request) (domain.Agent, error) { return worker, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var requests atomic.Int64
	var observedRun atomic.Pointer[domain.Run]
	var observedMaintenance atomic.Pointer[domain.MaintenanceStatus]
	var observationDelay atomic.Int64
	handler := server.Handler()
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		if run := observedRun.Load(); run != nil && r.Method == http.MethodGet {
			if r.URL.Path == api.Path("runs", run.ID) {
				diagnosticsEnvelope(t, w, *run, nil)
				return
			}
			if r.URL.Path == api.Path("maintenance-reservations", run.MaintenanceID) {
				time.Sleep(time.Duration(observationDelay.Load()))
				diagnosticsEnvelope(t, w, *observedMaintenance.Load(), nil)
				return
			}
		}
		if r.Method != http.MethodGet {
			requests.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	client := &api.Client{BaseURL: httpServer.URL, HTTP: httpServer.Client(), Credential: config.Credential{AgentID: worker.ID}}
	res := &resources{config: config.DefaultWorker(state.Root), state: state, client: client}
	t.Setenv(workTaskEnvironment, task.ID)
	t.Setenv(workRunEnvironment, run.ID)
	app := &app{operation: "native-build-run", diagnostic: io.Discard}
	result, err := app.runSoftwareBuild(ctx, res, []string{build.ID})
	if failingStep != "" {
		if err == nil || strings.Contains(err.Error(), "private-failure") {
			t.Fatal("failed native execution was not safely reported", err)
		}
		var journal buildRunJournal
		found, err := state.Get(ctx, "build-run:"+build.ID+":"+run.ID, &journal)
		if err != nil || !found || journal.State != "running" {
			t.Fatal("failed native journal lost its uncertainty fence", journal, err)
		}
		if _, err := app.runSoftwareBuild(ctx, res, []string{build.ID}); err == nil {
			t.Fatal("uncertain native build replay was allowed")
		}
		attempts, err := filepath.Glob(filepath.Join(workspace, "source-*", "attempts"))
		if err != nil || len(attempts) != 1 {
			t.Fatal("native attempts evidence missing", err)
		}
		data, err := os.ReadFile(attempts[0])
		if err != nil || string(data) != "attempted" {
			t.Fatal("compiler was repeated after failed execution", string(data), err)
		}
		return
	}
	if !allowed {
		if err == nil || domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal("unapproved cross target was accepted", result, err)
		}
		var journal buildRunJournal
		found, err := state.Get(ctx, "build-run:"+build.ID+":"+run.ID, &journal)
		if err != nil || found {
			t.Fatal("denied cross-build created a native effects journal", found, err)
		}
		return
	}
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
	destination := filepath.Join(root, "independent-review.tar.gz")
	if _, err := app.downloadSoftwareBuild(ctx, res, []string{build.ID, "--output", destination}); err != nil {
		t.Fatal("submitted build was not available through authenticated registry custody", err)
	}
	downloaded, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(downloaded, archive.Bytes()) {
		t.Fatal("review download did not retain the actual immutable output bytes", err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("review bytes were not owner-only", err)
	}
	if _, err := app.downloadSoftwareBuild(ctx, res, []string{build.ID, "--output", destination}); err == nil || domain.AsFault(err).Code != domain.Conflict {
		t.Fatal("review download overwrote retained evidence", err)
	}
	if _, err := app.runSoftwareBuild(ctx, res, []string{build.ID}); err != nil {
		t.Fatal("completed child replay attempted another compiler/output overwrite", err)
	}
	originalKey := "build-run:" + build.ID + ":" + run.ID
	var original buildRunJournal
	if found, err := state.Get(ctx, originalKey, &original); err != nil || !found {
		t.Fatal("missing submitted journal", err)
	}
	stopped, err := store.Report(ctx, worker.ID, task.ID, domain.ReportRequest{
		Command: domain.Command{OperationID: "lost-provider-report"}, RunID: run.ID, Token: token,
		SpecGeneration: run.SpecGeneration, Outcome: "stopped", Summary: "Joined provider; submitted custody inspected"})
	if err != nil {
		t.Fatal(err)
	}
	task = stopped.Task
	if !task.UnsafeRetry || task.ActiveRun != nil || task.Attempts != 1 || task.Failures != 0 || task.NextAttemptAt != 0 {
		t.Fatal("joined stopped run lost its unsafe retry fence or counters", task)
	}
	claim.Command = domain.Command{OperationID: "unapproved-report-continuation", ExpectedRevision: task.Revision}
	if _, err := store.Claim(ctx, worker.ID, task.ID, claim); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("submitted custody bypassed exact unsafe retry approval", err)
	}
	if _, err := store.ChangeTask(ctx, worker.ID, task.ID, domain.TaskChange{
		Command: domain.Command{OperationID: "unapproved-continuation-clarity", ExpectedRevision: task.Revision},
		Kind:    "clarity", SpecGeneration: task.SpecGeneration, Reason: "Inspect submitted custody",
	}); err == nil || !strings.Contains(err.Error(), "unsafe-retry approval") {
		t.Fatal("clarity cleared the unsafe retry fence before approval", err)
	}
	change(founder.ID, "retry-approve", func(q *domain.TaskChange) {
		q.SpecGeneration, q.RunID, q.Attempt = task.SpecGeneration, run.ID, 2
	})
	change(worker.ID, "clarity", func(q *domain.TaskChange) { q.SpecGeneration = task.SpecGeneration })
	claim.Command = domain.Command{OperationID: "report-only-continuation", ExpectedRevision: task.Revision}
	next, err := store.Claim(ctx, worker.ID, task.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workRunEnvironment, next.ID)
	for _, invalid := range []struct{ task, run string }{
		{"", next.ID}, {task.ID, ""}, {task.ID, run.ID}, {"foreign", next.ID},
	} {
		t.Setenv(workTaskEnvironment, invalid.task)
		t.Setenv(workRunEnvironment, invalid.run)
		if _, err := app.runSoftwareBuild(ctx, res, []string{build.ID}); err == nil {
			t.Fatal("submitted custody bypassed exact current admission")
		}
	}
	t.Setenv(workTaskEnvironment, task.ID)
	t.Setenv(workRunEnvironment, next.ID)
	if target == runtime.GOOS+"-"+runtime.GOARCH && len(capabilities) == 0 {
		for _, test := range []struct {
			name    string
			observe int64
			delay   time.Duration
			change  func(*domain.MaintenanceStatus)
			allowed bool
		}{
			{name: "target-clock-ahead", observe: 1000, allowed: true},
			{name: "target-clock-behind", observe: time.Now().Unix() + 86400, allowed: true},
			{name: "server-expired", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.ServerObservedAt = s.Reservation.ExpiresAt }},
			{name: "settled", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.State = "settled" }},
			{name: "changed-reservation", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.ID += "-changed" }},
			{name: "foreign-run", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.RunID = run.ID }},
			{name: "foreign-task", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.SourceID += "-foreign" }},
			{name: "foreign-actor", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.AgentID = founder.ID }},
			{name: "changed-deadline", observe: 1000, change: func(s *domain.MaintenanceStatus) { s.Reservation.ExpiresAt++ }},
			{name: "expired-in-transit", observe: 1000, delay: 1100 * time.Millisecond, change: func(s *domain.MaintenanceStatus) { s.ServerObservedAt = s.Reservation.ExpiresAt - 1 }},
		} {
			t.Run(test.name, func(t *testing.T) {
				current := next
				current.MaintenanceID, current.DeadlineAt = "maintenance-fixture", test.observe+60
				status := domain.MaintenanceStatus{ServerObservedAt: test.observe, Reservation: domain.MaintenanceReservation{
					ID: current.MaintenanceID, RunID: current.ID, AgentID: worker.ID, ProjectID: project.ID,
					SourceID: task.ID, Kind: domain.ExecutionRun, State: "admitted", ExpiresAt: current.DeadlineAt}}
				if test.change != nil {
					test.change(&status)
				}
				observedMaintenance.Store(&status)
				observedRun.Store(&current)
				observationDelay.Store(int64(test.delay))
				before := requests.Load()
				reused, err := app.runSoftwareBuild(ctx, res, []string{build.ID})
				if test.allowed && (err != nil || !reflect.DeepEqual(reused, submitted)) {
					t.Fatal("server-live submitted custody rejected under target clock skew", reused, err)
				}
				if !test.allowed && (err == nil || domain.AsFault(err).Code != domain.Denied) {
					t.Fatal("invalid maintenance admission was not explicitly denied", err)
				}
				if requests.Load() != before {
					t.Fatal("maintenance validation mutated remote custody")
				}
			})
		}
		observedRun.Store(nil)
	}
	if err := state.EnableWorkspace(ctx, binding.Ref, false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.runSoftwareBuild(ctx, res, []string{build.ID}); err == nil {
		t.Fatal("submitted custody bypassed disabled tools")
	}
	if err := state.EnableWorkspace(ctx, binding.Ref, true); err != nil {
		t.Fatal(err)
	}
	before := requests.Load()
	reused, err := app.runSoftwareBuild(ctx, res, []string{build.ID})
	if err != nil || !reflect.DeepEqual(reused, submitted) {
		t.Fatal("fresh admitted continuation did not reuse immutable custody", reused, err)
	}
	if requests.Load() != before {
		t.Fatal("submitted custody replay made a mutating HTTP request")
	}
	var retained buildRunJournal
	if found, err := state.Get(ctx, "build-run:"+build.ID+":"+next.ID, &retained); err != nil || found {
		t.Fatal("submitted continuation created a new native journal", found, err)
	}
	if found, err := state.Get(ctx, originalKey, &retained); err != nil || !found || !reflect.DeepEqual(retained, original) {
		t.Fatal("original submitted journal changed", err)
	}
	report := domain.ReportRequest{Command: domain.Command{OperationID: "complete-report-only-continuation"},
		RunID: next.ID, Token: token, SpecGeneration: next.SpecGeneration, Outcome: "completed", Summary: "Reused inspected submitted custody"}
	completed, err := store.Report(ctx, worker.ID, task.ID, report)
	if err != nil || !completed.Accepted || completed.Task.Result.RunID != next.ID || completed.Task.Attempts != 2 || completed.Task.Failures != 0 {
		t.Fatal("fresh report did not retain actual admission counters", completed, err)
	}
	replayed, err := store.Report(ctx, worker.ID, task.ID, report)
	if err != nil || !reflect.DeepEqual(completed, replayed) {
		t.Fatal("completion replay changed records", err)
	}
	unchanged, err := store.SoftwareBuild(ctx, worker.ID, build.ID)
	if err != nil || !reflect.DeepEqual(unchanged, submitted) || unchanged.RunID != run.ID {
		t.Fatal("completion rebound original build provenance", unchanged, err)
	}
	runs, err := store.Runs(ctx, worker.ID, task.ID)
	if err != nil || len(runs) != 2 {
		t.Fatal("recovery created extra runs", runs, err)
	}
	for _, retained := range runs {
		if retained.ID == run.ID && (retained.State != "stopped" || retained.Attempt != 1) ||
			retained.ID == next.ID && (retained.State != "completed" || retained.Attempt != 2) {
			t.Fatal("recovery changed retained execution identities", retained)
		}
	}
}
