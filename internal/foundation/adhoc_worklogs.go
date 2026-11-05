package foundation

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) adhocWorklog(ctx context.Context, tx *sql.Tx, task domain.Task, actor string, sections ...string) error {
	if task.ProjectID != domain.AdhocProjectID {
		return nil
	}
	body := ""
	write := func() error {
		entry := domain.Entry{ID: domain.NewID("worklog"), TaskID: task.ID, Kind: "worklog", Body: body,
			AuthorID: actor, Mentions: []string{}, Generation: 1, Revision: 1, CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		if err := domain.CheckText(entry.Body, "Adhoc worklog", 65536, true); err != nil {
			return err
		}
		return s.persistEntry(ctx, tx, &entry, actor, "adhoc-worklog")
	}
	for _, section := range sections {
		if len(body)+len(section)+2 > 65536 && body != "" {
			if err := write(); err != nil {
				return err
			}
			body = ""
		}
		if body != "" {
			body += "\n\n"
		}
		body += section
	}
	if body == "" {
		return domain.Fail(domain.Invalid, "Adhoc worklog requires an intentional summary")
	}
	return write()
}

func (s *Store) adhocRunWorklog(ctx context.Context, tx *sql.Tx, task domain.Task, run domain.Run) error {
	if task.ProjectID != domain.AdhocProjectID {
		return nil
	}
	model := run.Model
	if model == "" {
		model = "provider-selected (actual model not observed)"
	}
	header := fmt.Sprintf("## %s: %s\n\nTask: %s\nRun: %s\nSpecification: %d\nProvider: %s\nModel: %s\nTask status: %s",
		run.Kind, run.State, task.ID, run.ID, run.SpecGeneration, run.Provider, model, task.Status)
	if task.Status == domain.Completed {
		header += "\n\nExecution is completed and awaits independent verification; it is not done."
	}
	sections := []string{header}
	if run.State == "running" {
		sections = append(sections, "The run was claimed under current readiness and the single work slot. Provider invocation and completion are not yet confirmed.")
	} else {
		sections = append(sections, run.Summary)
		for _, evidence := range run.Evidence {
			sections = append(sections, "- "+evidence)
		}
	}
	return s.adhocWorklog(ctx, tx, task, run.AgentID, sections...)
}

func (s *Store) adhocCreationWorklog(ctx context.Context, tx *sql.Tx, task domain.Task, actor string) error {
	return s.adhocWorklog(ctx, tx, task, actor,
		fmt.Sprintf("## Created: %s\n\nSelf-assigned to %s under standing Adhoc policy.", task.Title, task.AssigneeID),
		"### Instructions", task.Description, "### Acceptance criteria", task.AcceptanceCriteria,
		"This record grants no additional project, OS, spending or verification authority.")
}
