package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
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
		task.ActiveRun.AgentID != r.client.Credential.AgentID || task.ActiveRun.StopRequested || task.ActiveRun.Kind == domain.VerificationRun ||
		task.BuildID != build.ID || task.SpecGeneration != build.SpecGeneration {
		return nil, domain.Fail(domain.Denied, "build run is a child of its exact admitted coding task, not another execution slot")
	}
	binding, err := r.state.Workspace(ctx, task.WorkspaceRef)
	if err != nil {
		return nil, err
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
		return api.Call[domain.SoftwareBuild](ctx, r.client, "GET", api.Path("software-builds", id), nil)
	}
	if !found {
		if build.Recipe.Platform != runtime.GOOS+"-"+runtime.GOARCH {
			return nil, domain.Fail(domain.Prerequisite, "build recipe targets a different native computer; use its assigned build-capable agent")
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
		for _, step := range build.Recipe.Steps {
			binary, err := exec.LookPath(step.Args[0])
			if extraPath != "" {
				for _, directory := range filepath.SplitList(extraPath) {
					candidate := filepath.Join(directory, step.Args[0])
					if info, probeErr := os.Stat(candidate); probeErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
						binary, err = candidate, nil
						break
					}
				}
			}
			if err != nil {
				return nil, err
			}
			output, diagnostic := process.NewBuffer(256<<10), process.NewBuffer(256<<10)
			env := process.Environment()
			if extraPath != "" {
				for i, entry := range env {
					if strings.HasPrefix(entry, "PATH=") {
						env[i] = "PATH=" + extraPath + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
					}
				}
			}
			if err := (process.Native{}).Run(buildCtx, process.Spec{Path: binary, Args: step.Args[1:], Dir: workspace, Env: env, Stdout: output, Stderr: diagnostic}); err != nil {
				return nil, errors.Join(domain.Fail(domain.Prerequisite, "approved native build step failed; original run and private outputs were retained"), err)
			}
			if _, err := output.Bytes(); err != nil {
				return nil, err
			}
			if _, err := diagnostic.Bytes(); err != nil {
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
			Toolchain: "Native approved recipe; Foundation runtime " + runtime.Version(),
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
