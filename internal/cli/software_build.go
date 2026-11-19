package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/work"
)

type buildRunJournal struct {
	BuildID string                     `json:"build_id"`
	RunID   string                     `json:"run_id"`
	State   string                     `json:"state"`
	Path    string                     `json:"path"`
	Upload  domain.FileUploadRequest   `json:"upload"`
	Result  domain.SoftwareBuildResult `json:"result"`
}

func (a *app) downloadSoftwareBuild(ctx context.Context, r *resources, args []string) (any, error) {
	id, tail, err := idArgs(args)
	if err != nil {
		return nil, err
	}
	flags := flag.NewFlagSet("build download", flag.ContinueOnError)
	output := flags.String("output", "", "new owner-only destination; existing or partial files are never overwritten")
	if err := parse(flags, tail); err != nil {
		return nil, err
	}
	if *output == "" {
		return nil, domain.Fail(domain.Invalid, "build download requires --output NEW_FILE")
	}
	build, err := api.Call[domain.SoftwareBuild](ctx, r.client, "GET", api.Path("software-builds", id), nil)
	if err != nil {
		return nil, err
	}
	if build.State != "submitted" || build.ArtifactID == "" {
		return nil, domain.Fail(domain.Prerequisite, "only an actually submitted immutable build can be downloaded for review")
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil, domain.Fail(domain.Conflict, "build review destination already exists; original or partial bytes were preserved")
	}
	if err != nil {
		return nil, err
	}
	artifact, downloadErr := r.client.DownloadArtifact(ctx, build.ArtifactID, file, false)
	if err := errors.Join(downloadErr, file.Sync(), file.Close()); err != nil {
		return nil, errors.Join(err, domain.Fail(domain.Prerequisite, "build download did not complete; inspect preserved destination bytes before another read-only attempt"))
	}
	return map[string]any{"build_id": build.ID, "artifact": artifact, "state": "downloaded"}, nil
}

func (a *app) runSoftwareBuild(ctx context.Context, r *resources, args []string) (any, error) {
	id, tail, err := idArgs(args)
	if err != nil {
		return nil, err
	}
	fs := flag.NewFlagSet("build run", flag.ContinueOnError)
	if err := parse(fs, tail); err != nil {
		return nil, err
	}
	build, err := api.Call[domain.SoftwareBuild](ctx, r.client, "GET", api.Path("software-builds", id), nil)
	if err != nil {
		return nil, err
	}
	task, err := api.Call[domain.Task](ctx, r.client, "GET", api.Path("tasks", build.TaskID), nil)
	if err != nil {
		return nil, err
	}
	runID := os.Getenv(workRunEnvironment)
	if os.Getenv(workTaskEnvironment) != task.ID || task.ActiveRun == nil || task.ActiveRun.ID != runID ||
		task.ActiveRun.AgentID != r.client.Credential.AgentID || task.ActiveRun.StopRequested || task.ActiveRun.Kind != domain.ExecutionRun ||
		task.AssigneeID != r.client.Credential.AgentID || task.Status != domain.Running || task.ArchivedAt != 0 ||
		task.Source == nil || *task.Source != build.Source || task.BuildID != build.ID || task.SpecGeneration != build.SpecGeneration {
		return nil, domain.Fail(domain.Denied, "build run is a child of its exact admitted coding task, not another execution slot")
	}
	current, err := api.Call[domain.Run](ctx, r.client, "GET", api.Path("runs", runID), nil)
	if err != nil {
		return nil, err
	}
	if current.TaskID != task.ID || current.AgentID != r.client.Credential.AgentID || current.State != "running" ||
		current.SpecGeneration != task.SpecGeneration || current.Kind != domain.ExecutionRun {
		return nil, domain.Fail(domain.Denied, "build execution is not current or its maintenance admission expired")
	}
	if current.MaintenanceID != "" || current.DeadlineAt != 0 {
		if current.MaintenanceID == "" || current.DeadlineAt <= 0 {
			return nil, domain.Fail(domain.Denied, "build execution has incomplete maintenance admission")
		}
		started := time.Now()
		status, err := api.Call[domain.MaintenanceStatus](ctx, r.client, "GET",
			api.Path("maintenance-reservations", current.MaintenanceID), nil)
		if err != nil {
			return nil, err
		}
		reservation := status.Reservation
		if reservation.ID != current.MaintenanceID || reservation.RunID != current.ID ||
			reservation.AgentID != current.AgentID || reservation.ProjectID != task.ProjectID ||
			reservation.SourceID != task.ID || reservation.Kind != current.Kind || reservation.State != "admitted" ||
			reservation.ExpiresAt != current.DeadlineAt || status.ServerObservedAt <= 0 ||
			reservation.ExpiresAt <= status.ServerObservedAt {
			return nil, domain.Fail(domain.Denied, "build maintenance admission changed or expired")
		}
		// Anchor the server's remaining allowance before the request, including its latency.
		deadline := started.Add(time.Duration(reservation.ExpiresAt-status.ServerObservedAt) * time.Second)
		if !time.Now().Before(deadline) {
			return nil, domain.Fail(domain.Denied, "build maintenance observation expired in transit")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	actor, err := api.Call[domain.Agent](ctx, r.client, "GET", api.Path("agents", current.AgentID), nil)
	if err != nil {
		return nil, err
	}
	project, err := api.Call[domain.Project](ctx, r.client, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		return nil, err
	}
	if !actor.Active || actor.Banned || (!actor.Founder && actor.Mode != "ai") || project.Archived ||
		current.ProjectEpoch != project.Epoch || task.Readiness == nil ||
		task.Readiness.SpecGeneration != task.SpecGeneration || task.Readiness.ProjectEpoch != project.Epoch {
		return nil, domain.Fail(domain.Denied, "build execution no longer has current mode and readiness authority")
	}
	approver, err := api.Call[domain.Agent](ctx, r.client, "GET", api.Path("agents", task.Readiness.ActorID), nil)
	if err != nil {
		return nil, err
	}
	if !domain.EligibleApprover(approver, project, task, false) {
		return nil, domain.Fail(domain.Denied, "build readiness authority was revoked")
	}
	binding, err := r.state.Workspace(ctx, task.WorkspaceRef)
	if err != nil {
		return nil, err
	}
	if build.State == "submitted" {
		product, err := api.Call[domain.SoftwareProduct](ctx, r.client, "GET", api.Path("software-products", build.ProductID), nil)
		if err != nil {
			return nil, err
		}
		if product.ProjectID != task.ProjectID || product.RepositoryID != build.Source.RepositoryID {
			return nil, domain.Fail(domain.Prerequisite, "submitted build belongs to different product source")
		}
		original, err := api.Call[domain.Run](ctx, r.client, "GET", api.Path("runs", build.RunID), nil)
		if err != nil {
			return nil, err
		}
		if build.BuilderID != current.AgentID || build.ArtifactID == "" ||
			(build.Source.ProductID != "" && build.Source.ProductID != build.ProductID) ||
			original.TaskID != task.ID || original.AgentID != current.AgentID || original.SpecGeneration != task.SpecGeneration ||
			original.Kind != domain.ExecutionRun || original.Attempt < 1 || original.Attempt > current.Attempt ||
			(original.ID != current.ID && original.State == "running") ||
			!slices.Contains([]string{"running", "completed", "paused", "stopped", "failed", "interrupted"}, original.State) {
			return nil, domain.Fail(domain.Prerequisite, "submitted build does not match retained builder execution provenance")
		}
		artifact, err := r.client.DownloadArtifact(ctx, build.ArtifactID, io.Discard, false)
		if err != nil {
			return nil, err
		}
		if artifact.ProjectID != task.ProjectID || artifact.OwnerID != actor.ID || artifact.Kind != "software-bundle" || artifact.RepositoryID != "" {
			return nil, domain.Fail(domain.Prerequisite, "submitted artifact custody has different ownership")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return build, nil
	}
	if build.State != "prepared" {
		return nil, domain.Fail(domain.Prerequisite, "build has neither prepared inputs nor verified submitted custody")
	}
	key := "build-run:" + build.ID + ":" + runID
	var journal buildRunJournal
	found, err := r.state.Get(ctx, key, &journal)
	if err != nil {
		return nil, err
	}
	if found && (journal.BuildID != build.ID || journal.RunID != runID) {
		return nil, domain.Fail(domain.Conflict, "build operation is bound to different retained inputs")
	}
	if found && journal.State == "running" {
		return nil, domain.Fail(domain.Prerequisite, "interrupted native build has uncertain effects; inspect retained evidence, never blindly rerun it")
	}
	if found && journal.State == "submitted" {
		return nil, domain.Fail(domain.Prerequisite, "local submitted journal has no matching verified remote custody")
	}
	if !found {
		host := runtime.GOOS + "-" + runtime.GOARCH
		if build.Recipe.Platform != host {
			product, err := api.Call[domain.SoftwareProduct](ctx, r.client, "GET", api.Path("software-products", build.ProductID), nil)
			if err != nil {
				return nil, err
			}
			if product.ID != build.ProductID || !slices.Contains(product.Capabilities, "cross-build:"+host+":"+build.Recipe.Platform) {
				return nil, domain.Fail(domain.Prerequisite, "cross-build requires the product's exact explicit host/target capability and later native consumer verification")
			}
		}
		journal = buildRunJournal{BuildID: id, RunID: runID, State: "prepared",
			Path: filepath.Join(r.config.StateDir, "build-"+id+"-"+runID+".tar.gz")}
		if err := r.state.Put(ctx, key, journal); err != nil {
			return nil, err
		}
	}
	if journal.State == "prepared" {
		workspace, err := work.SourceWorkspace(ctx, r.client, build.Source, binding.Path, task.ID+"-"+strconv.FormatInt(task.SpecGeneration, 10))
		if err != nil {
			return nil, err
		}
		head, err := work.GitCommand(ctx, workspace, "", "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(head)) != build.Source.Commit {
			return nil, domain.Fail(domain.Conflict, "build checkout differs from its exact approved source")
		}
		status, err := work.GitCommand(ctx, workspace, "", "status", "--porcelain", "--untracked-files=normal")
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(status))) != 0 {
			return nil, domain.Fail(domain.Conflict, "build source contains changes or unexplained outputs; they were not overwritten")
		}
		for _, output := range build.Recipe.Outputs {
			if _, err := os.Lstat(filepath.Join(workspace, filepath.FromSlash(output.Path))); !errors.Is(err, os.ErrNotExist) {
				return nil, domain.Fail(domain.Conflict, "approved build output already exists; inspect retained bytes before running")
			}
		}
		extraPath, err := stageBuildDependencies(ctx, r.client, build, workspace)
		if err != nil {
			return nil, err
		}
		journal.State = "running"
		if err := r.state.Put(ctx, key, journal); err != nil {
			return nil, err
		}
		buildCtx, cancel := context.WithTimeout(ctx, time.Duration(build.Recipe.TimeoutSeconds)*time.Second)
		defer cancel()
		for index, step := range build.Recipe.Steps {
			if err := runNativeBuildStep(buildCtx, r.config.StateDir, build.ID, runID, index+1, workspace, extraPath, step); err != nil {
				return nil, err
			}
		}
		file, err := os.OpenFile(journal.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		epoch := int64(0)
		if raw, err := work.GitCommand(ctx, workspace, "", "show", "-s", "--format=%ct", build.Source.Commit); err == nil {
			epoch, err = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil {
				return nil, errors.Join(err, file.Close())
			}
		} else {
			return nil, errors.Join(err, file.Close())
		}
		_, bundleErr := software.CreateBundle(file, workspace, build.Recipe.Outputs, epoch)
		if err := errors.Join(bundleErr, file.Sync(), file.Close()); err != nil {
			return nil, err
		}
		source, err := os.Open(journal.Path)
		if err != nil {
			return nil, err
		}
		size, digest, hashErr := transferDigest(source)
		if err := errors.Join(hashErr, source.Close()); err != nil {
			return nil, err
		}
		if size > domain.MaxSoftwareBundleBytes {
			return nil, domain.Fail(domain.Prerequisite, "native build archive exceeds its supported size")
		}
		journal.Upload = domain.FileUploadRequest{Command: domain.Command{OperationID: "op_" + domain.Hash(key+":upload")},
			Name: "build-" + build.ID + ".tar.gz", MediaType: "application/gzip", Size: size, SHA256: digest}
		journal.Result = domain.SoftwareBuildResult{Command: domain.Command{OperationID: "op_" + domain.Hash(key+":result")}, RunID: runID,
			Toolchain: "Approved " + build.Recipe.Platform + " recipe executed on " + runtime.GOOS + "-" + runtime.GOARCH + "; Foundation runtime " + runtime.Version(),
			Summary:   fmt.Sprintf("Executed %d approved native recipe steps and verified every declared output", len(build.Recipe.Steps))}
		journal.State = "compiled"
		if err := r.state.Put(ctx, key, journal); err != nil {
			return nil, err
		}
	}
	source, err := os.Open(journal.Path)
	if err != nil {
		return nil, err
	}
	uploaded, uploadErr := r.client.UploadFile(ctx, journal.Upload, source)
	if err := errors.Join(uploadErr, source.Close()); err != nil {
		return nil, err
	}
	journal.Result.BundleFileID = uploaded.ID
	if err := r.state.Put(ctx, key, journal); err != nil {
		return nil, err
	}
	result, err := api.Call[domain.SoftwareBuild](ctx, r.client, "POST", api.Path("software-builds", id, "result"), journal.Result)
	if err != nil {
		return nil, err
	}
	journal.State = "submitted"
	return result, r.state.Put(context.WithoutCancel(ctx), key, journal)
}

func stageBuildDependencies(ctx context.Context, client *api.Client, build domain.SoftwareBuild, workspace string) (string, error) {
	var directories []string
	for _, dependency := range build.Recipe.Dependencies {
		release, err := api.Call[domain.SoftwareRelease](ctx, client, "GET", api.Path("software-releases", dependency.ReleaseID), nil)
		if err != nil {
			return "", err
		}
		if release.Revoked || release.Manifest.ProductID != dependency.ProductID {
			return "", domain.Fail(domain.Conflict, "source-pinned tool dependency was revoked or replaced")
		}
		selected := (*domain.SoftwarePlatform)(nil)
		for i := range release.Manifest.Platforms {
			platform := &release.Manifest.Platforms[i]
			if platform.Platform == dependency.Platform && platform.SHA256 == dependency.SHA256 {
				selected = platform
			}
		}
		if selected == nil {
			return "", domain.Fail(domain.Conflict, "tool dependency platform/digest differs from its source recipe")
		}
		path := filepath.Join(workspace, ".foundation-dependencies", dependency.ProductID, dependency.ReleaseID)
		var data strings.Builder
		if _, err := client.DownloadArtifact(ctx, selected.ArtifactID, &data, false); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err := software.ExtractBundle([]byte(data.String()), path, selected.Payloads); err != nil {
			return "", err
		}
		if dependency.Purpose == "build" {
			directories = append(directories, filepath.Join(path, "bin"))
		}
	}
	if len(build.Recipe.Dependencies) != 0 {
		file, err := os.OpenFile(filepath.Join(workspace, ".git", "info", "exclude"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return "", err
		}
		_, writeErr := io.WriteString(file, "\n.foundation-dependencies/\n")
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return "", err
		}
	}
	return strings.Join(directories, string(os.PathListSeparator)), nil
}
