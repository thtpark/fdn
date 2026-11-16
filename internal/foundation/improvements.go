package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

func improvementPolicy(ctx context.Context, q queryer) (domain.ImprovementPolicy, error) {
	policy := domain.ImprovementPolicy{ProjectIDs: []string{}}
	var raw string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='improvement-policy'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	err = json.Unmarshal([]byte(raw), &policy)
	return policy, err
}

func (s *Store) ImprovementPolicy(ctx context.Context, actor string) (domain.ImprovementPolicy, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.ImprovementPolicy{}, err
	}
	return improvementPolicy(ctx, s.db)
}

func (s *Store) ensureFoundationProject(ctx context.Context, tx *sql.Tx, founder domain.Agent) (domain.Project, error) {
	project, err := get[domain.Project](ctx, tx, "projects", domain.FoundationProjectID)
	if err == nil {
		if project.Name != "Foundation" || project.OwnerID != founder.ID {
			return project, domain.Fail(domain.Conflict, "reserved Foundation project conflicts with retained state; explicit reconciliation is required")
		}
		return project, nil
	}
	if domain.AsFault(err).Code != domain.NotFound {
		return project, err
	}
	project = domain.Project{ID: domain.FoundationProjectID, Name: "Foundation", OwnerID: founder.ID,
		Description: "Foundation platform features, repairs and internal delivery. Ordinary project authority, independent approval, one work slot and explicit native installation policies apply.",
		Delegates:   []string{}, Revision: 1, Epoch: 1, CreatedAt: s.timestamp()}
	data, err := encode(project)
	if err != nil {
		return project, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO projects(id,data) VALUES(?,?)", project.ID, data); err != nil {
		return project, err
	}
	if err := s.history(ctx, tx, project.ID, founder.ID, "foundation-initialize", project); err != nil {
		return project, err
	}
	return project, s.event(ctx, tx, "project", project.ID, "", project.ID)
}

func (s *Store) ConfigureImprovements(ctx context.Context, actor string, req domain.ImprovementPolicyRequest) (domain.ImprovementPolicy, error) {
	return mutate(ctx, s, actor, "improvement.configure", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ImprovementPolicy, error) {
		var zero domain.ImprovementPolicy
		if !a.Founder {
			return zero, domain.Fail(domain.Denied, "only Founder may configure improvement admission and initialize its reserved project")
		}
		if err := domain.CheckText(req.Reason, "improvement policy reason", 2048, true); err != nil {
			return zero, err
		}
		prior, err := improvementPolicy(ctx, tx)
		if err != nil {
			return zero, err
		}
		if req.ExpectedRevision != prior.Revision {
			return zero, domain.Fail(domain.Conflict, "improvement policy revision changed")
		}
		if len(req.ProjectIDs) > 32 || req.WindowSeconds < 60 || req.WindowSeconds > 86400 ||
			req.SliceSeconds < 1 || req.SliceSeconds > 3600 || req.PerAgentSeconds < req.SliceSeconds ||
			req.PerAgentSeconds > req.WindowSeconds || req.FleetSeconds < req.PerAgentSeconds ||
			req.FleetSeconds > req.WindowSeconds*32 {
			return zero, domain.Fail(domain.Invalid, "explicit bounded window, slice, per-agent and fleet allowances are required")
		}
		if _, err := s.ensureFoundationProject(ctx, tx, a); err != nil {
			return zero, err
		}
		projects := append([]string{domain.FoundationProjectID}, req.ProjectIDs...)
		slices.Sort(projects)
		projects = slices.Compact(projects)
		for _, id := range projects {
			p, err := get[domain.Project](ctx, tx, "projects", id)
			if err != nil {
				return zero, err
			}
			if p.Archived || id == domain.AdhocProjectID {
				return zero, domain.Fail(domain.Invalid, "improvement admission requires active scoped projects; Adhoc cannot bypass its accounting")
			}
		}
		policy := domain.ImprovementPolicy{Revision: prior.Revision + 1, Enabled: req.Enabled, ProjectIDs: projects,
			WindowSeconds: req.WindowSeconds, SliceSeconds: req.SliceSeconds, PerAgentSeconds: req.PerAgentSeconds,
			FleetSeconds: req.FleetSeconds, UpdatedBy: a.ID, UpdatedAt: s.timestamp()}
		raw, err := encode(policy)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('improvement-policy',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET data=json_set(data,'$.active_run.stop_requested',json('true'))
WHERE json_extract(data,'$.active_run') IS NOT NULL AND EXISTS
(SELECT 1 FROM maintenance_reservations m WHERE json_extract(m.data,'$.run_id')=json_extract(tasks.data,'$.active_run.id'))`); err != nil {
			return zero, err
		}
		return policy, s.event(ctx, tx, "improvement-policy", domain.FoundationProjectID, "", domain.FoundationProjectID)
	})
}

func validateImprovement(req domain.ImprovementRequest) error {
	if !slices.Contains([]string{"feature_request", "enhancement", "bug_report", "incident_repair", "maintenance"}, req.Kind) {
		return domain.Fail(domain.Invalid, "improvement kind must be feature_request, enhancement, bug_report, incident_repair or maintenance")
	}
	for _, field := range []struct {
		value, name string
		max         int
	}{{req.Title, "improvement title", 256}, {req.Description, "improvement description", 32768}, {req.AcceptanceCriteria, "improvement acceptance criteria", 16384}} {
		if err := domain.CheckText(field.value, field.name, field.max, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateImprovement(ctx context.Context, actor string, req domain.ImprovementRequest) (domain.Improvement, error) {
	return mutate(ctx, s, actor, "improvement.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Improvement, error) {
		var zero domain.Improvement
		if err := validateImprovement(req); err != nil {
			return zero, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return zero, err
		}
		if project.Archived {
			return zero, domain.Fail(domain.Prerequisite, "improvement project is archived")
		}
		if req.ProductID != "" {
			product, err := get[domain.SoftwareProduct](ctx, tx, "software_products", req.ProductID)
			if err != nil {
				return zero, err
			}
			if product.ProjectID != project.ID {
				return zero, domain.Fail(domain.Invalid, "tool improvements belong to the product's owning project")
			}
		}
		if req.RelatedTaskID != "" {
			if _, err := get[domain.Task](ctx, tx, "tasks", req.RelatedTaskID); err != nil {
				return zero, err
			}
		}
		request := domain.Improvement{ID: domain.NewID("improvement"), ProjectID: project.ID, ProductID: req.ProductID,
			Kind: req.Kind, Title: req.Title, Description: req.Description, AcceptanceCriteria: req.AcceptanceCriteria,
			RelatedTaskID: req.RelatedTaskID, RequesterID: a.ID, Origin: "agent", State: "proposed", Revision: 1,
			CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		if err := s.saveImprovement(ctx, tx, request); err != nil {
			return zero, err
		}
		return request, s.event(ctx, tx, "improvement", request.ID, "", project.ID)
	})
}

func (s *Store) saveImprovement(ctx context.Context, tx *sql.Tx, req domain.Improvement) error {
	data, err := encode(req)
	if err != nil {
		return err
	}
	var incident any
	if req.IncidentID != "" {
		incident = req.IncidentID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO improvements(id,data,project_id,state,incident_key) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data,state=excluded.state`, req.ID, data, req.ProjectID, req.State, incident)
	return err
}

func (s *Store) DecideImprovement(ctx context.Context, actor, id string, req domain.ImprovementDecision) (domain.ImprovementResult, error) {
	return mutate(ctx, s, actor, "improvement.decide:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.ImprovementResult, error) {
		var result domain.ImprovementResult
		request, err := get[domain.Improvement](ctx, tx, "improvements", id)
		if err != nil {
			return result, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", request.ProjectID)
		if err != nil {
			return result, err
		}
		if err := requireAuthority(a, project); err != nil {
			return result, err
		}
		if err := expect(request.Revision, req.ExpectedRevision); err != nil {
			return result, err
		}
		if project.Archived || request.TaskID != "" || request.State == "declined" || request.State == "duplicate" {
			return result, domain.Fail(domain.Conflict, "improvement is not an actionable undecided request")
		}
		if err := domain.CheckText(req.Reason, "improvement decision", 2048, true); err != nil {
			return result, err
		}
		switch req.Decision {
		case "accept":
			if req.AssigneeID == "" || req.DuplicateOf != "" {
				return result, domain.Fail(domain.Invalid, "accept requires an assignee and cannot set a duplicate")
			}
			if _, err := activeIdentitySubject(ctx, tx, req.AssigneeID); err != nil {
				return result, err
			}
			task := domain.Task{ID: domain.NewID("task"), ProjectID: project.ID, Title: request.Title,
				Description: request.Description, AcceptanceCriteria: request.AcceptanceCriteria, Status: domain.Todo,
				AssigneeID: req.AssigneeID, CreatedBy: a.ID, CreatedAt: s.timestamp(), SpecGeneration: 1,
				Priority: 2, Importance: 2, Policy: domain.ApprovalPolicy{IndependentReadiness: true, IndependentVerification: true},
				RetryPolicy: domain.RetryPolicy{MaxRetries: 3, BackoffSeconds: 30}, Source: req.Source}
			if err := s.validateTaskSource(ctx, tx, project, task.Source); err != nil {
				return result, err
			}
			if err := validateContent(task); err != nil {
				return result, err
			}
			if err := s.saveTask(ctx, tx, &task, a.ID, "improvement-accepted", req.Reason); err != nil {
				return result, err
			}
			request.TaskID, request.State, result.Task = task.ID, "accepted", &task
		case "defer", "decline":
			if req.AssigneeID != "" || req.Source != nil || req.DuplicateOf != "" {
				return result, domain.Fail(domain.Invalid, "defer/decline cannot assign work or source")
			}
			request.State = map[string]string{"defer": "deferred", "decline": "declined"}[req.Decision]
		case "duplicate":
			if req.AssigneeID != "" || req.Source != nil || req.DuplicateOf == id {
				return result, domain.Fail(domain.Invalid, "duplicate requires another request and cannot assign work")
			}
			other, err := get[domain.Improvement](ctx, tx, "improvements", req.DuplicateOf)
			if err != nil {
				return result, err
			}
			if other.ProjectID != project.ID || other.State == "duplicate" {
				return result, domain.Fail(domain.Invalid, "duplicate must identify an original request in the same owning project")
			}
			request.State, request.DuplicateOf = "duplicate", other.ID
		default:
			return result, domain.Fail(domain.Invalid, "decision must be accept, defer, decline or duplicate")
		}
		request.Revision++
		request.DecisionReason, request.UpdatedAt = req.Reason, s.timestamp()
		if err := s.saveImprovement(ctx, tx, request); err != nil {
			return result, err
		}
		result.Request = request
		return result, s.event(ctx, tx, "improvement", request.ID, request.TaskID, project.ID)
	})
}

func (s *Store) Improvement(ctx context.Context, actor, id string) (domain.Improvement, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Improvement{}, err
	}
	return get[domain.Improvement](ctx, s.db, "improvements", id)
}

func (s *Store) Improvements(ctx context.Context, actor, project, state, cursor string, limit int) (domain.Page[domain.Improvement], error) {
	return deliveryPage[domain.Improvement](ctx, s, actor, "improvements", project, state, cursor, limit)
}

func maintenanceProject(policy domain.ImprovementPolicy, project string) bool {
	return project == domain.FoundationProjectID || slices.Contains(policy.ProjectIDs, project)
}

func (s *Store) MaintenanceUsage(ctx context.Context, actor string) (domain.MaintenanceUsage, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.MaintenanceUsage{}, err
	}
	return s.maintenanceUsage(ctx, s.db, actor)
}

func (s *Store) maintenanceUsage(ctx context.Context, q queryer, actor string) (domain.MaintenanceUsage, error) {
	policy, err := improvementPolicy(ctx, q)
	if err != nil {
		return domain.MaintenanceUsage{}, err
	}
	usage := domain.MaintenanceUsage{Policy: policy, AgentID: actor, ServerObservedAt: s.timestamp()}
	if policy.WindowSeconds == 0 {
		return usage, nil
	}
	usage.WindowStart = s.timestamp() / int64(policy.WindowSeconds) * int64(policy.WindowSeconds)
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN agent_id=? THEN seconds ELSE 0 END),0),
COALESCE(SUM(seconds),0),count(*) FROM maintenance_reservations WHERE created_at>=?`, actor, usage.WindowStart).
		Scan(&usage.AgentSeconds, &usage.FleetSeconds, &usage.Reservations)
	return usage, err
}

func (s *Store) ReserveMaintenance(ctx context.Context, actor string, req domain.MaintenanceRequest) (domain.MaintenanceReservation, error) {
	return mutate(ctx, s, actor, "maintenance.reserve", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.MaintenanceReservation, error) {
		var zero domain.MaintenanceReservation
		usage, err := s.maintenanceUsage(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		policy := usage.Policy
		if !policy.Enabled || !maintenanceProject(policy, req.ProjectID) || req.Seconds < 1 || req.Seconds > policy.SliceSeconds {
			return zero, domain.Fail(domain.Prerequisite, "maintenance requires enabled Founder policy and a bounded admitted slice")
		}
		if !a.Founder && a.Mode != "ai" {
			return zero, domain.Fail(domain.Prerequisite, "Founder pause blocks improvement admission")
		}
		project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return zero, err
		}
		if project.Archived {
			return zero, domain.Fail(domain.Prerequisite, "maintenance project is archived")
		}
		if !slices.Contains([]string{"triage", "readiness", "preparation", "execution", "verification", "recovery"}, req.Kind) {
			return zero, domain.Fail(domain.Invalid, "unsupported maintenance source kind")
		}
		if req.Kind == "triage" {
			request, err := get[domain.Improvement](ctx, tx, "improvements", req.SourceID)
			if err != nil {
				return zero, err
			}
			if request.ProjectID != project.ID || request.State != "proposed" || !authority(a, project) {
				return zero, domain.Fail(domain.Denied, "triage requires current scoped authority and a proposed request")
			}
		} else {
			task, err := get[domain.Task](ctx, tx, "tasks", req.SourceID)
			if err != nil {
				return zero, err
			}
			review := req.Kind == "verification" || req.Kind == "readiness"
			if task.ProjectID != project.ID || task.ArchivedAt != 0 ||
				(!review && task.AssigneeID != a.ID) ||
				(review && (task.AssigneeID == a.ID || !domain.EligibleApprover(a, project, task, req.Kind == "verification"))) {
				return zero, domain.Fail(domain.Denied, "maintenance source is not assigned or independently reviewable by this actor")
			}
		}
		var occupied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM maintenance_reservations WHERE agent_id=?
AND json_extract(data,'$.state')='reserved' AND json_extract(data,'$.expires_at')>?)`, actor, s.timestamp()).Scan(&occupied); err != nil {
			return zero, err
		}
		if occupied || usage.AgentSeconds+req.Seconds > policy.PerAgentSeconds || usage.FleetSeconds+req.Seconds > policy.FleetSeconds {
			return zero, domain.Fail(domain.Prerequisite, "maintenance slice is occupied or its durable allowance is exhausted")
		}
		reservation := domain.MaintenanceReservation{ID: domain.NewID("maintenance"), AgentID: actor, ProjectID: project.ID,
			SourceID: req.SourceID, Kind: req.Kind, PolicyRevision: policy.Revision, Seconds: req.Seconds,
			CreatedAt: s.timestamp(), ExpiresAt: s.timestamp() + int64(req.Seconds), State: "reserved"}
		raw, err := encode(reservation)
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO maintenance_reservations(id,data,agent_id,seconds,created_at) VALUES(?,?,?,?,?)",
			reservation.ID, raw, actor, req.Seconds, reservation.CreatedAt)
		return reservation, err
	})
}

func (s *Store) validateMaintenance(ctx context.Context, tx *sql.Tx, actor, project, source, kind, id string) (domain.MaintenanceReservation, error) {
	policy, err := improvementPolicy(ctx, tx)
	if err != nil {
		return domain.MaintenanceReservation{}, err
	}

	if !maintenanceProject(policy, project) {
		if id != "" {
			return domain.MaintenanceReservation{}, domain.Fail(domain.Invalid, "ordinary work cannot borrow a maintenance reservation")
		}
		return domain.MaintenanceReservation{}, nil
	}
	if id == "" {
		return domain.MaintenanceReservation{}, domain.Fail(domain.Prerequisite, "Foundation/tool improvement work requires a durable maintenance reservation")
	}
	reservation, err := get[domain.MaintenanceReservation](ctx, tx, "maintenance_reservations", id)
	if err != nil {
		return reservation, err
	}
	if !policy.Enabled || reservation.PolicyRevision != policy.Revision || reservation.AgentID != actor ||
		reservation.ProjectID != project || reservation.SourceID != source || reservation.Kind != kind ||
		reservation.ExpiresAt <= s.timestamp() || reservation.State != "reserved" {
		return reservation, domain.Fail(domain.Conflict, "maintenance policy, source, actor or admitted slice changed/expired")
	}
	return reservation, nil
}

func (s *Store) SettleMaintenance(ctx context.Context, actor, id string, req domain.MaintenanceSettlement) (domain.MaintenanceReservation, error) {
	return mutate(ctx, s, actor, "maintenance.settle:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.MaintenanceReservation, error) {
		reservation, err := get[domain.MaintenanceReservation](ctx, tx, "maintenance_reservations", id)
		if err != nil {
			return reservation, err
		}
		if reservation.AgentID != a.ID {
			return reservation, domain.Fail(domain.Denied, "only the admitted actor may settle its own maintenance receipt")
		}
		// Settlement releases scheduling, never the conservatively charged bound.
		reservation.State = "settled"
		raw, err := encode(reservation)
		if err != nil {
			return reservation, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE maintenance_reservations SET data=? WHERE id=?", raw, id)
		return reservation, err
	})
}

func deliveryPage[T any](ctx context.Context, s *Store, actor, table, project, state, cursor string, limit int) (domain.Page[T], error) {
	page := domain.Page[T]{Items: []T{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := table + ":" + project + ":" + state
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	query, args := "SELECT data FROM "+table+" WHERE id>?", []any{last}
	if project != "" {
		query += " AND project_id=?"
		args = append(args, project)
	}
	if state != "" {
		if table != "improvements" || !slices.Contains([]string{"proposed", "accepted", "deferred", "declined", "duplicate"}, state) {
			return page, domain.Fail(domain.Invalid, "unsupported delivery state filter")
		}
		query += " AND state=?"
		args = append(args, state)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[T](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		raw, err := encode(page.Items[limit-1])
		if err != nil {
			return page, err
		}
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || strings.TrimSpace(item.ID) == "" {
			return page, domain.Fail(domain.Internal, "delivery page item omitted its stable ID")
		}
		page.NextCursor = makeCursor(actor, key, item.ID)
		page.Items = page.Items[:limit]
	}
	return page, nil
}
