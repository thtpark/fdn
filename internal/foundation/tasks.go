package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

func validateContent(t domain.Task) error {
	for _, field := range []struct {
		value    string
		name     string
		max      int
		required bool
	}{
		{t.Title, "title", 512, true},
		{t.Description, "description", 65536, false},
		{t.AcceptanceCriteria, "acceptance criteria", 32768, false},
	} {
		if err := domain.CheckText(field.value, field.name, field.max, field.required); err != nil {
			return err
		}
	}
	if t.Priority < 1 || t.Priority > 4 || t.Importance < 1 || t.Importance > 3 {
		return domain.Fail(domain.Invalid, "priority must be 1-4 and importance 1-3")
	}
	return t.RetryPolicy.Validate()
}

func (s *Store) saveTask(ctx context.Context, tx *sql.Tx, t *domain.Task, actor, action string, reasons ...string) error {
	t.Revision++
	t.UpdatedAt = s.timestamp()
	data, err := encode(t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id, data, project_id, assignee_id, status, priority, archived)
VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data, assignee_id=excluded.assignee_id, status=excluded.status, priority=excluded.priority, archived=excluded.archived`,
		t.ID, data, t.ProjectID, t.AssigneeID, t.Status, t.Priority, t.ArchivedAt != 0)
	if err != nil {
		return err
	}
	if err := s.history(ctx, tx, t.ID, actor, action, t, reasons...); err != nil {
		return err
	}
	return s.event(ctx, tx, "task", t.ID, t.ID, t.ProjectID)
}

func (s *Store) CreateTask(ctx context.Context, actor string, req domain.TaskRequest) (domain.Task, error) {
	return mutate(ctx, s, actor, "task.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Task, error) {
		p, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return domain.Task{}, err
		}
		if p.Archived {
			return domain.Task{}, domain.Fail(domain.Prerequisite, "project is archived")
		}
		if req.Priority == 0 {
			req.Priority = 2
		}
		if req.Importance == 0 {
			req.Importance = 2
		}
		t := domain.Task{ID: domain.NewID("task"), ProjectID: req.ProjectID, Title: req.Title,
			Description: req.Description, AcceptanceCriteria: req.AcceptanceCriteria, ParentID: req.ParentID,
			Repetitive: req.Repetitive, Status: domain.Draft, Priority: req.Priority, Importance: req.Importance,
			SpecGeneration: 1, CreatedBy: a.ID, CreatedAt: s.timestamp(),
			Policy:      domain.ApprovalPolicy{RequiredActors: []string{}},
			RetryPolicy: domain.RetryPolicy{MaxRetries: 3, BackoffSeconds: 30}}
		if err := validateContent(t); err != nil {
			return t, err
		}
		if req.ParentID != "" {
			parent, err := get[domain.Task](ctx, tx, "tasks", req.ParentID)
			if err != nil {
				return t, err
			}
			if parent.ProjectID != t.ProjectID {
				return t, domain.Fail(domain.Invalid, "parent must belong to the same project")
			}
		}
		err = s.saveTask(ctx, tx, &t, a.ID, "create")
		return t, err
	})
}

func invalidate(t *domain.Task) {
	t.SpecGeneration++
	t.Readiness, t.Verification, t.RetryApproval, t.Clarity = nil, nil, nil, nil
	t.Attempts, t.Failures, t.NextAttemptAt, t.UnsafeRetry = 0, 0, 0, false
	if t.Waiting != nil && t.Waiting.Kind != "clarification" {
		t.Waiting = nil
	}
	if t.Status == domain.Completed || t.Status == domain.Done {
		t.Status = domain.Todo
	}
	if t.ActiveRun != nil {
		t.ActiveRun.StopRequested = true
		t.Status = domain.Paused
	}
}

func requireAuthority(a domain.Agent, p domain.Project) error {
	if !authority(a, p) {
		return domain.Fail(domain.Denied, "Founder or a current delegate of this project is required")
	}
	return nil
}

func eligibleApprover(a domain.Agent, p domain.Project, t domain.Task, verification bool) bool {
	return domain.EligibleApprover(a, p, t, verification)
}

func (s *Store) validApproval(ctx context.Context, tx *sql.Tx, p domain.Project, t domain.Task, approval *domain.Approval, verify bool) (bool, error) {
	if approval == nil || approval.SpecGeneration != t.SpecGeneration || approval.ProjectEpoch != p.Epoch {
		return false, nil
	}
	actor, err := activeIdentitySubject(ctx, tx, approval.ActorID)
	if err != nil {
		var fault *domain.Fault
		if errors.As(err, &fault) && (fault.Code == domain.NotFound || fault.Code == domain.Denied) {
			return false, nil
		}
		return false, err
	}
	return eligibleApprover(actor, p, t, verify), nil
}

func validateChange(req domain.TaskChange) error {
	fields := map[string]string{
		"edit":   "title description acceptance_criteria priority importance",
		"status": "status", "assign": "agent_id provider model", "provider": "provider model",
		"workspace": "workspace_ref", "policy": "policy", "retry-policy": "retry_policy",
		"approve": "spec_generation", "verify": "spec_generation result_generation decision",
		"retry-approve": "spec_generation run_id attempt", "withdraw": "",
		"clarity": "spec_generation answer_ids", "waiting": "spec_generation waiting",
		"revert": "history_sequence", "archive": "", "stop": "", "release": "run_id stopped_acknowledged",
	}
	allowed, ok := fields[req.Kind]
	if !ok {
		return domain.Fail(domain.Invalid, "unknown task change kind")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	for key := range values {
		if key == "operation_id" || key == "expected_revision" || key == "kind" || key == "reason" {
			continue
		}
		if !slices.Contains(strings.Fields(allowed), key) {
			return domain.Fail(domain.Invalid, "field "+key+" is not valid for "+req.Kind)
		}
	}
	return domain.CheckText(req.Reason, "change reason", 8192, true)
}

func (s *Store) ChangeTask(ctx context.Context, actor, id string, req domain.TaskChange) (domain.Task, error) {
	if err := validateChange(req); err != nil {
		return domain.Task{}, err
	}
	return mutate(ctx, s, actor, "task."+req.Kind+":"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Task, error) {
		t, err := get[domain.Task](ctx, tx, "tasks", id)
		if err != nil {
			return t, err
		}
		p, err := get[domain.Project](ctx, tx, "projects", t.ProjectID)
		if err != nil {
			return t, err
		}
		if err := expect(t.Revision, req.ExpectedRevision); err != nil {
			return t, err
		}
		if t.ArchivedAt != 0 || p.Archived {
			return t, domain.Fail(domain.Prerequisite, "task or project is archived")
		}
		administrative := slices.Contains([]string{"assign", "provider", "retry-approve", "release"}, req.Kind)
		if administrative {
			if err := requireAuthority(a, p); err != nil {
				return t, err
			}
		}
		switch req.Kind {
		case "edit":
			if req.Title == nil && req.Description == nil && req.AcceptanceCriteria == nil && req.Priority == nil && req.Importance == nil {
				return t, domain.Fail(domain.Invalid, "edit must include an editable field")
			}
			if req.Title != nil {
				t.Title = *req.Title
			}
			if req.Description != nil {
				t.Description = *req.Description
			}
			if req.AcceptanceCriteria != nil {
				t.AcceptanceCriteria = *req.AcceptanceCriteria
			}
			if req.Priority != nil {
				t.Priority = *req.Priority
			}
			if req.Importance != nil {
				t.Importance = *req.Importance
			}
			invalidate(&t)
		case "assign", "provider":
			if req.Provider != "" && !domain.ValidProvider(req.Provider) {
				return t, domain.Fail(domain.Invalid, "provider must be codex, copilot, or empty to inherit")
			}
			if req.Model != "" && req.Provider == "" {
				return t, domain.Fail(domain.Invalid, "model requires an explicit provider")
			}
			if err := domain.CheckText(req.Model, "required model", 256, false); err != nil {
				return t, err
			}
			if req.Kind == "assign" {
				if req.AgentID != "" {
					if _, err := activeIdentitySubject(ctx, tx, req.AgentID); err != nil {
						return t, err
					}
				}
				if t.AssigneeID != req.AgentID {
					t.WorkspaceRef = ""
				}
				t.AssigneeID = req.AgentID
			}
			t.Provider, t.Model = req.Provider, req.Model
			invalidate(&t)
		case "workspace":
			if a.ID != t.AssigneeID {
				return t, domain.Fail(domain.Denied, "only the assigned worker sets its workspace reference")
			}
			if err := domain.CheckText(req.WorkspaceRef, "opaque workspace reference", 128, true); err != nil {
				return t, err
			}
			if !domain.ValidOperation(req.WorkspaceRef) {
				return t, domain.Fail(domain.Invalid, "workspace reference must be opaque, not a filesystem path")
			}
			t.WorkspaceRef = req.WorkspaceRef
			invalidate(&t)
		case "policy":
			if req.Policy == nil || len(req.Policy.RequiredActors) > 32 {
				return t, domain.Fail(domain.Invalid, "bounded approval policy is required")
			}
			for _, actor := range req.Policy.RequiredActors {
				if _, err := activeIdentitySubject(ctx, tx, actor); err != nil {
					return t, err
				}
			}
			weaker := (t.Policy.IndependentReadiness && !req.Policy.IndependentReadiness) ||
				(t.Policy.IndependentVerification && !req.Policy.IndependentVerification)
			for _, id := range req.Policy.RequiredActors {
				if len(t.Policy.RequiredActors) > 0 && !slices.Contains(t.Policy.RequiredActors, id) {
					weaker = true
				}
			}
			if len(t.Policy.RequiredActors) != 0 && len(req.Policy.RequiredActors) == 0 {
				weaker = true
			}
			if weaker {
				if err := requireAuthority(a, p); err != nil {
					return t, err
				}
			}
			t.Policy = *req.Policy
			invalidate(&t)
		case "retry-policy":
			if req.RetryPolicy == nil {
				return t, domain.Fail(domain.Invalid, "retry policy is required")
			}
			if err := requireAuthority(a, p); err != nil {
				return t, err
			}
			t.RetryPolicy = *req.RetryPolicy
			invalidate(&t)
		case "status":
			if err := s.transition(&t, a, p, req.Status); err != nil {
				return t, err
			}
		case "approve":
			if !eligibleApprover(a, p, t, false) {
				return t, domain.Fail(domain.Denied, "actor does not satisfy current readiness authority/policy")
			}
			if req.SpecGeneration != t.SpecGeneration {
				return t, domain.Fail(domain.Conflict, "specification changed")
			}
			if t.AssigneeID == "" || t.WorkspaceRef == "" || strings.TrimSpace(t.Description) == "" || strings.TrimSpace(t.AcceptanceCriteria) == "" || t.Waiting != nil || t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "readiness needs instructions, criteria, assignment, workspace, and no unresolved blocker/run")
			}
			if _, err := activeIdentitySubject(ctx, tx, t.AssigneeID); err != nil {
				return t, err
			}
			t.Readiness = &domain.Approval{ActorID: a.ID, SpecGeneration: t.SpecGeneration, ProjectEpoch: p.Epoch, At: s.timestamp(), OperationID: req.OperationID}
		case "withdraw":
			if err := requireAuthority(a, p); err != nil {
				return t, err
			}
			t.Readiness, t.Verification, t.RetryApproval = nil, nil, nil
			if t.ActiveRun != nil {
				t.ActiveRun.StopRequested = true
			}
			if t.Status == domain.Done {
				t.Status = domain.Completed
			}
		case "verify":
			if !eligibleApprover(a, p, t, true) {
				return t, domain.Fail(domain.Denied, "actor does not satisfy current verification authority/policy")
			}
			if req.SpecGeneration != t.SpecGeneration || req.ResultGeneration != t.ResultGeneration {
				return t, domain.Fail(domain.Conflict, "specification or result changed")
			}
			if t.Status != domain.Completed || t.Result == nil || t.Result.SpecGeneration != t.SpecGeneration || t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "only a current completed result can be verified")
			}
			switch req.Decision {
			case "accept":
				t.Status, t.Waiting = domain.Done, nil
				t.Verification = &domain.Approval{ActorID: a.ID, SpecGeneration: t.SpecGeneration, ResultGeneration: t.ResultGeneration, ProjectEpoch: p.Epoch, At: s.timestamp(), OperationID: req.OperationID}
			case "rework":
				invalidate(&t)
				t.Status = domain.Todo
			default:
				return t, domain.Fail(domain.Invalid, "verification decision must be accept or rework")
			}
		case "retry-approve":
			if !eligibleApprover(a, p, t, false) {
				return t, domain.Fail(domain.Denied, "actor does not satisfy retry approval policy")
			}
			if req.SpecGeneration != t.SpecGeneration || req.RunID != t.LastRunID || req.Attempt != t.Attempts+1 {
				return t, domain.Fail(domain.Conflict, "retry must identify the exact current specification, prior run, and next attempt")
			}
			if !t.UnsafeRetry || t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "no stopped unsafe attempt awaits approval")
			}
			t.RetryApproval = &domain.Approval{ActorID: a.ID, SpecGeneration: t.SpecGeneration, ProjectEpoch: p.Epoch, RunID: req.RunID, Attempt: req.Attempt, At: s.timestamp(), OperationID: req.OperationID}
		case "clarity":
			if a.ID != t.AssigneeID {
				return t, domain.Fail(domain.Denied, "only the assigned worker can record understanding")
			}
			if req.SpecGeneration != t.SpecGeneration {
				return t, domain.Fail(domain.Conflict, "specification changed")
			}
			if t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "stop active operations before reviewing continuation")
			}
			if t.Waiting != nil {
				if t.Waiting.Kind == "clarification" {
					if err := s.validateAnswers(ctx, tx, t, req.AnswerIDs); err != nil {
						return t, err
					}
				} else if t.Waiting.Kind == "retry" {
					if err := s.retryReady(ctx, tx, p, t); err != nil {
						return t, err
					}
				} else {
					return t, domain.Fail(domain.Prerequisite, "recorded blocker must be resolved before clarity review")
				}
			}
			t.Clarity = &domain.Clarity{ActorID: a.ID, SpecGeneration: t.SpecGeneration, DiscussionGeneration: t.DiscussionGeneration, ProjectEpoch: p.Epoch, Rationale: req.Reason, AnswerIDs: req.AnswerIDs}
			t.Waiting = nil
			if t.Status == domain.Paused {
				t.Status = domain.Todo
			}
		case "waiting":
			if a.ID != t.AssigneeID {
				return t, domain.Fail(domain.Denied, "only the assigned worker records its blockers")
			}
			if req.SpecGeneration != t.SpecGeneration {
				return t, domain.Fail(domain.Conflict, "specification changed")
			}
			if t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "active runs must pause through a fenced report")
			}
			if err := s.validateWaiting(ctx, tx, t, a.ID, req.Waiting); err != nil {
				return t, err
			}
			waiting := *req.Waiting
			waiting.Since = s.timestamp()
			t.Waiting, t.Clarity, t.Status = &waiting, nil, domain.Paused
		case "revert":
			var data []byte
			if err := tx.QueryRowContext(ctx, "SELECT snapshot FROM histories WHERE source_id = ? AND sequence = ?", t.ID, req.HistorySequence).Scan(&data); err != nil {
				if err == sql.ErrNoRows {
					return t, domain.Fail(domain.NotFound, "snapshot is outside the retained three-history window")
				}
				return t, err
			}
			var prior domain.Task
			if err := json.Unmarshal(data, &prior); err != nil {
				return t, err
			}
			t.Title, t.Description, t.AcceptanceCriteria = prior.Title, prior.Description, prior.AcceptanceCriteria
			t.Priority, t.Importance = prior.Priority, prior.Importance
			invalidate(&t)
		case "archive":
			if !t.Status.Terminal() || t.ActiveRun != nil {
				return t, domain.Fail(domain.Prerequisite, "only stopped terminal tasks can be archived")
			}
			t.ArchivedAt = s.timestamp()
		case "stop":
			if a.ID != t.AssigneeID && !authority(a, p) {
				return t, domain.Fail(domain.Denied, "only the worker or project authority can stop execution")
			}
			if t.ActiveRun == nil {
				return t, domain.Fail(domain.Prerequisite, "no active run")
			}
			t.ActiveRun.StopRequested, t.ActiveRun.AfterStop = true, domain.Paused
		case "release":
			if !req.StoppedAcknowledged || t.ActiveRun == nil || req.RunID != t.ActiveRun.ID {
				return t, domain.Fail(domain.Prerequisite, "exact run and explicit acknowledgment that its process has stopped are required")
			}
			run, err := get[domain.Run](ctx, tx, "runs", req.RunID)
			if err != nil {
				return t, err
			}
			run.State, run.Summary, run.FinishedAt = "interrupted", req.Reason, s.timestamp()
			if err := s.saveRun(ctx, tx, run); err != nil {
				return t, err
			}
			if run.Kind == domain.VerificationRun {
				afterStop := t.ActiveRun.AfterStop
				t.ActiveRun = nil
				if t.Result != nil && t.Result.SpecGeneration == t.SpecGeneration {
					t.Status = domain.Completed
					t.Waiting = &domain.Waiting{Kind: "verification", Reason: req.Reason, Since: s.timestamp()}
				}
				if afterStop == domain.CancelledStatus {
					t.Status, t.Waiting = domain.CancelledStatus, nil
				}
			} else {
				t.ActiveRun, t.Clarity, t.UnsafeRetry, t.Status = nil, nil, true, domain.Paused
				t.Waiting = &domain.Waiting{Kind: "retry", Reason: req.Reason, Since: s.timestamp()}
			}
		}
		if err := validateContent(t); err != nil {
			return t, err
		}
		err = s.saveTask(ctx, tx, &t, a.ID, req.Kind, req.Reason)
		return t, err
	})
}

func (s *Store) transition(t *domain.Task, a domain.Agent, p domain.Project, next domain.Status) error {
	if t.ActiveRun != nil {
		if (a.ID != t.AssigneeID && !authority(a, p)) || (next != domain.Paused && next != domain.CancelledStatus) {
			return domain.Fail(domain.Prerequisite, "active work requires an authorized stop acknowledgment")
		}
		t.ActiveRun.StopRequested, t.ActiveRun.AfterStop, t.Status = true, next, domain.Paused
		return nil
	}
	allowed := map[domain.Status][]domain.Status{
		domain.Draft:      {domain.Backlogged, domain.Todo, domain.WontDo, domain.CancelledStatus},
		domain.Backlogged: {domain.Draft, domain.Todo, domain.WontDo, domain.CancelledStatus},
		domain.Todo:       {domain.Draft, domain.Backlogged, domain.Paused, domain.WontDo, domain.CancelledStatus},
		domain.Paused:     {domain.Todo, domain.Backlogged, domain.CancelledStatus},
	}
	if !slices.Contains(allowed[t.Status], next) {
		return domain.Fail(domain.Prerequisite, "transition requires the dedicated claim, report, verification, or rework operation")
	}
	if t.Waiting != nil && next == domain.Todo {
		return domain.Fail(domain.Prerequisite, "resolve the recorded blocker through clarity review")
	}
	t.Status = next
	return nil
}

func (s *Store) Task(ctx context.Context, actor, id string) (domain.Task, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Task{}, err
	}
	return get[domain.Task](ctx, s.db, "tasks", id)
}

func (s *Store) Tasks(ctx context.Context, actor string, query domain.ListQuery) (domain.Page[domain.Task], error) {
	page := domain.Page[domain.Task]{Items: []domain.Task{}}
	if query.Status != "" && !query.Status.Valid() {
		return page, domain.Fail(domain.Invalid, "unknown task status filter")
	}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	limit, err := limitSize(query.Limit)
	if err != nil {
		return page, err
	}
	queryKey := query
	queryKey.Cursor, queryKey.Limit = "", limit
	raw, err := json.Marshal(queryKey)
	if err != nil {
		return page, err
	}
	last, err := readCursor(query.Cursor, actor, string(raw))
	if err != nil {
		return page, err
	}
	sqlQuery := "SELECT data FROM tasks WHERE id > ?"
	args := []any{last}
	if query.ProjectID != "" {
		sqlQuery += " AND project_id = ?"
		args = append(args, query.ProjectID)
	}
	if query.AssigneeID != "" {
		sqlQuery += " AND assignee_id = ?"
		args = append(args, query.AssigneeID)
	}
	if query.Status != "" {
		sqlQuery += " AND status = ?"
		args = append(args, query.Status)
	}
	if !query.IncludeArchived {
		sqlQuery += " AND archived = 0"
	}
	sqlQuery += " ORDER BY id LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.Task](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, string(raw), page.Items[limit-1].ID)
	}
	return page, nil
}

func (s *Store) Histories(ctx context.Context, actor, source string) ([]domain.History, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(source, "task_") && !strings.HasPrefix(source, "project_") && !strings.HasPrefix(source, "comment_") && !strings.HasPrefix(source, "worklog_") {
		return nil, domain.Fail(domain.Denied, "private or unknown history source")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT sequence, source_id, actor_id, action, reason, created_at, snapshot FROM histories WHERE source_id = ? ORDER BY sequence DESC", source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.History{}
	for rows.Next() {
		var h domain.History
		if err := rows.Scan(&h.Sequence, &h.SourceID, &h.ActorID, &h.Action, &h.Reason, &h.CreatedAt, &h.Snapshot); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) Events(ctx context.Context, actor, project, task, since string, limit int) (domain.Page[domain.Event], error) {
	page := domain.Page[domain.Event]{Items: []domain.Event{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := project + ":" + task + ":" + strconv.Itoa(limit)
	last, err := readCursor(since, actor, key)
	if err != nil {
		return page, err
	}
	if last == "" {
		last = "0"
	}
	sequence, err := strconv.ParseInt(last, 10, 64)
	if err != nil || sequence < 0 {
		return page, domain.Fail(domain.Invalid, "invalid watch cursor")
	}
	query := "SELECT sequence, topic, source_id, task_id, project_id, created_at FROM events WHERE sequence > ?"
	args := []any{sequence}
	if project != "" {
		query += " AND project_id = ?"
		args = append(args, project)
	}
	if task != "" {
		query += " AND task_id = ?"
		args = append(args, task)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY sequence LIMIT ?", args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.Event
		if err := rows.Scan(&event.Sequence, &event.Topic, &event.SourceID, &event.TaskID, &event.ProjectID, &event.CreatedAt); err != nil {
			return page, err
		}
		page.Items = append(page.Items, event)
		sequence = event.Sequence
	}
	page.NextCursor = makeCursor(actor, key, strconv.FormatInt(sequence, 10))
	return page, rows.Err()
}
