package foundation

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) proposeIncidentImprovement(ctx context.Context, tx *sql.Tx, episode healthEpisode) error {
	if episode.Incident.State != "active" || !slices.Contains([]string{"phase-deadline-exceeded", "provider-response", "worker-error", "component-error"}, episode.Incident.Kind) {
		return nil
	}
	policy, err := improvementPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return nil
	}
	project, err := get[domain.Project](ctx, tx, "projects", domain.FoundationProjectID)
	if err != nil {
		return err
	}
	if project.Archived {
		return nil
	}
	var existing bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM improvements WHERE incident_key=?)", episode.Incident.ID).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return nil
	}
	request := domain.Improvement{ID: domain.NewID("improvement"), ProjectID: domain.FoundationProjectID,
		Kind: "incident_repair", Title: "Investigate " + episode.Incident.Kind,
		Description:        fmt.Sprintf("A retained Foundation health episode reports %s. Classify software versus configuration/provider/resource causes before proposing a code change. Preserve existing work, retry and recovery fences. Server source: %s (%s).", episode.Incident.Kind, domain.Version, domain.Commit),
		AcceptanceCriteria: "Reproduce or explain the observed condition, independently verify any approved fix and regression tests, and check actual relevant health after any separately approved deployment.",
		RelatedTaskID:      episode.TaskID, IncidentID: episode.Incident.ID, Origin: "service", State: "proposed",
		Revision: 1, CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
	if err := s.saveImprovement(ctx, tx, request); err != nil {
		return err
	}
	return s.event(ctx, tx, "improvement", request.ID, "", request.ProjectID)
}
