package work

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/secure"
)

type RecoveryInspectionRequest struct {
	TaskID         string   `json:"task_id"`
	SourceRunID    string   `json:"source_run_id"`
	Paths          []string `json:"paths"`
	ExpectedAbsent []string `json:"expected_absent,omitempty"`
}

type RecoveryInspectionCheck struct {
	Path      string   `json:"path"`
	Kind      string   `json:"kind,omitempty"`
	Size      int64    `json:"size,omitempty"`
	SHA256    string   `json:"sha256,omitempty"`
	Entries   []string `json:"entries,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Problem   string   `json:"problem,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

type RecoveryInspection struct {
	InspectionID   string                    `json:"inspection_id"`
	JournalID      string                    `json:"journal_id"`
	RunID          string                    `json:"run_id"`
	TaskID         string                    `json:"task_id"`
	SourceRunID    string                    `json:"source_run_id"`
	SpecGeneration int64                     `json:"spec_generation"`
	WorkspaceRef   string                    `json:"workspace_ref"`
	Request        RecoveryInspectionRequest `json:"request"`
	Checks         []RecoveryInspectionCheck `json:"checks"`
}

func (e *Engine) inspectionCurrent(ctx context.Context, journal local.Journal, launch Launch) error {
	if launch.Recovery == nil || !launch.Recovery.InspectionAuthorized || !launch.Recovery.JoinedProcessVerified ||
		launch.Recovery.FailuresRemaining < 1 || launch.Task == nil || launch.Project == nil {
		return domain.Fail(domain.Prerequisite, "native recovery inspection requires an admitted recovery contract")
	}
	if err := e.validateRunMode(ctx, true); err != nil {
		return err
	}
	task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", journal.TaskID), nil)
	if err != nil {
		return err
	}
	if task.ActiveRun == nil || task.ActiveRun.ID != journal.RunID || task.ActiveRun.Kind != domain.RecoveryRun ||
		task.ActiveRun.StopRequested || task.AssigneeID != e.Client.Credential.AgentID ||
		task.SpecGeneration != journal.SpecGeneration || task.LastRunID != launch.Recovery.SourceRun.ID ||
		task.RecoveryRunID != journal.RunID || task.WorkspaceRef != journal.WorkspaceRef || task.Readiness == nil ||
		task.DiscussionGeneration != journal.DiscussionGeneration {
		return domain.Fail(domain.Conflict, "recovery inspection ownership, source or specification changed")
	}
	project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
	if err != nil {
		return err
	}
	if project.Archived || project.Epoch != launch.Project.Epoch || task.Readiness.ProjectEpoch != project.Epoch {
		return domain.Fail(domain.Denied, "recovery inspection project authority changed")
	}
	_, err = e.State.Workspace(ctx, journal.WorkspaceRef)
	return err
}

func (e *Engine) InspectRecovery(ctx context.Context, operation string, request RecoveryInspectionRequest) (RecoveryInspection, error) {
	var receipt RecoveryInspection
	if !domain.ValidOperation(operation) || !domain.ValidOperation(request.TaskID) || !domain.ValidOperation(request.SourceRunID) ||
		len(request.Paths)+len(request.ExpectedAbsent) == 0 || len(request.Paths)+len(request.ExpectedAbsent) > 8 {
		return receipt, domain.Fail(domain.Invalid, "recovery inspection requires an operation, exact task/source and one to eight local paths")
	}
	paths := append(slices.Clone(request.Paths), request.ExpectedAbsent...)
	for _, path := range paths {
		if err := domain.CheckText(path, "recovery inspection path", 4096, true); err != nil {
			return receipt, err
		}
		if strings.ContainsAny(path, "\x00\r\n") {
			return receipt, domain.Fail(domain.Invalid, "recovery inspection path contains control characters")
		}
	}
	journals, err := e.State.Journals(ctx, true)
	if err != nil {
		return receipt, err
	}
	var selected *local.Journal
	var launch Launch
	for _, journal := range journals {
		if journal.Kind != domain.RecoveryRun || journal.State != "invoking" || journal.PID == 0 ||
			journal.TaskID != request.TaskID || journal.Report != nil {
			continue
		}
		var current Launch
		if err := json.Unmarshal(journal.Context, &current); err != nil {
			return receipt, err
		}
		if current.Recovery == nil || current.Recovery.SourceRun.ID != request.SourceRunID {
			continue
		}
		if selected != nil {
			return receipt, domain.Fail(domain.Conflict, "multiple native recovery invocations require operator inspection")
		}
		copy := journal
		selected, launch = &copy, current
	}
	if selected == nil {
		return receipt, domain.Fail(domain.Prerequisite, "no exact invoking native recovery owns this inspection")
	}
	if err := e.inspectionCurrent(ctx, *selected, launch); err != nil {
		return receipt, err
	}
	key := "recovery-inspection:" + operation
	found, err := e.State.Get(ctx, key, &receipt)
	if err != nil {
		return receipt, err
	}
	if found {
		if receipt.JournalID != selected.ID || receipt.TaskID != request.TaskID ||
			receipt.SourceRunID != request.SourceRunID || !slices.Equal(receipt.Request.Paths, request.Paths) ||
			!slices.Equal(receipt.Request.ExpectedAbsent, request.ExpectedAbsent) {
			return receipt, domain.Fail(domain.Conflict, "inspection operation belongs to a different exact recovery or path list")
		}
		return receipt, nil
	}
	binding, err := e.State.Workspace(ctx, selected.WorkspaceRef)
	if err != nil {
		return receipt, err
	}
	receipt = RecoveryInspection{InspectionID: operation, JournalID: selected.ID, RunID: selected.RunID,
		TaskID: selected.TaskID, SourceRunID: request.SourceRunID, SpecGeneration: selected.SpecGeneration,
		WorkspaceRef: selected.WorkspaceRef, Request: request}
	for index, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(binding.Path, path)
		}
		check, err := inspectRecoveryPath(ctx, filepath.Clean(path))
		if err != nil {
			return receipt, err
		}
		if index >= len(request.Paths) {
			if check.Problem == "not-found" {
				check.Kind, check.Problem, check.Detail = "absent", "", ""
			} else if check.Problem == "" {
				check.Problem, check.Detail = "unexpected-presence", "The path expected to be absent is present; preserve it rather than overwriting."
			}
		}
		receipt.Checks = append(receipt.Checks, check)
	}
	if err := e.inspectionCurrent(ctx, *selected, launch); err != nil {
		return receipt, err
	}
	return receipt, e.State.Put(ctx, key, receipt)
}

func inspectRecoveryPath(ctx context.Context, path string) (check RecoveryInspectionCheck, err error) {
	check.Path = path
	if err := ctx.Err(); err != nil {
		return check, err
	}
	// Failed local checks are observations too, not successful inspection fallbacks.
	observe := func(cause error) RecoveryInspectionCheck {
		check.Problem = "inspection-failed"
		check.Detail = cause.Error()
		switch {
		case errors.Is(cause, os.ErrNotExist):
			check.Problem = "not-found"
		case errors.Is(cause, os.ErrPermission), errors.Is(cause, syscall.ELOOP), domain.AsFault(cause).Code == domain.Denied:
			check.Problem = "access-denied"
		}
		return check
	}
	root, name, err := filetransfer.Root(path)
	if err != nil {
		return observe(err), nil
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Lstat(name)
	if err != nil {
		return observe(err), nil
	}
	if info.IsDir() {
		directory, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return observe(err), nil
		}
		entries, readErr := directory.ReadDir(129)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return observe(readErr), closeErr
		}
		if closeErr != nil {
			return check, closeErr
		}
		check.Kind, check.Truncated = "directory", len(entries) > 128
		for _, entry := range entries[:min(len(entries), 128)] {
			check.Entries = append(check.Entries, entry.Name())
		}
		slices.Sort(check.Entries)
		return check, ctx.Err()
	}
	if !info.Mode().IsRegular() || info.Size() > domain.MaxFileSize {
		return observe(domain.Fail(domain.Denied, "recovery inspection requires a real directory or regular file within 1 GiB")), nil
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return observe(err), nil
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	pinned, err := file.Stat()
	if err != nil {
		return observe(err), nil
	}
	if !os.SameFile(info, pinned) || !pinned.Mode().IsRegular() || pinned.Size() > domain.MaxFileSize {
		return check, domain.Fail(domain.Conflict, "recovery input changed while opening it")
	}
	if err := secure.CheckOwner(pinned); err != nil {
		return observe(err), nil
	}
	check.Kind = "file"
	check.Size, check.SHA256, err = filetransfer.Digest(file)
	if err != nil {
		return observe(err), nil
	}
	current, err := root.Lstat(name)
	if err != nil {
		return observe(err), nil
	}
	if !os.SameFile(info, current) || !current.Mode().IsRegular() || current.Size() != check.Size ||
		current.ModTime() != info.ModTime() {
		return check, domain.Fail(domain.Conflict, "local recovery input changed during inspection")
	}
	return check, ctx.Err()
}

func (e *Engine) validateRecoveryResult(ctx context.Context, journal local.Journal, launch Launch, reply provider.Reply) error {
	if reply.Recovery == nil {
		return domain.Fail(domain.Provider, "recovery returned no native inspection receipt; task metadata is not an investigation")
	}
	if err := reply.Recovery.Validate(reply.Outcome); err != nil {
		return err
	}
	var receipt RecoveryInspection
	found, err := e.State.Get(ctx, "recovery-inspection:"+reply.Recovery.InspectionID, &receipt)
	if err != nil {
		return err
	}
	if !found || receipt.InspectionID != reply.Recovery.InspectionID || receipt.JournalID != journal.ID ||
		receipt.RunID != journal.RunID || receipt.TaskID != journal.TaskID || receipt.SpecGeneration != journal.SpecGeneration ||
		launch.Recovery == nil || receipt.SourceRunID != launch.Recovery.SourceRun.ID ||
		receipt.WorkspaceRef != journal.WorkspaceRef || len(receipt.Checks) == 0 {
		return domain.Fail(domain.Provider, "recovery inspection receipt does not belong to this exact invocation")
	}
	if err := e.inspectionCurrent(ctx, journal, launch); err != nil {
		return err
	}
	for _, check := range receipt.Checks {
		if reply.Outcome != "reconciled" {
			continue
		}
		if check.Problem != "" {
			return domain.Fail(domain.Provider, "recovery cannot claim reconciliation while its native inspection retains a failed check")
		}
		if check.Kind == "file" {
			current, err := inspectRecoveryPath(ctx, check.Path)
			if err != nil {
				return err
			}
			if current.Problem != "" || current.Size != check.Size || current.SHA256 != check.SHA256 {
				return domain.Fail(domain.Conflict, "inspected recovery input changed before reconciliation")
			}
		} else if check.Kind == "absent" {
			current, err := inspectRecoveryPath(ctx, check.Path)
			if err != nil {
				return err
			}
			if current.Problem != "not-found" {
				return domain.Fail(domain.Conflict, "recovery output is no longer absent; preserve the new resource")
			}
		}
	}
	return nil
}
