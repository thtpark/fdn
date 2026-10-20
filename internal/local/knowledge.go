package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Knowledge struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Revision  int64  `json:"revision"`
	Archived  bool   `json:"archived"`
	Selected  bool   `json:"selected"`
	UpdatedAt int64  `json:"updated_at"`
}

type KnowledgeChange struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Content          string `json:"content"`
	ExpectedRevision int64  `json:"expected_revision"`
	Archived         bool   `json:"archived"`
	Selected         bool   `json:"selected"`
}

func (s *State) PutKnowledge(ctx context.Context, req KnowledgeChange) (Knowledge, error) {
	var out Knowledge
	if !slices.Contains([]string{"memory", "skill", "knowledge"}, req.Kind) || !namePattern.MatchString(req.Name) {
		return out, domain.Fail(domain.Invalid, "knowledge requires kind memory/skill/knowledge and a safe name")
	}
	if err := domain.CheckText(req.Content, "private knowledge", 256<<10, true); err != nil {
		return out, err
	}
	if req.Archived && req.Selected {
		return out, domain.Fail(domain.Invalid, "archived knowledge cannot be selected")
	}
	id := req.Kind + ":" + req.Name
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM knowledge WHERE id = ?", id).Scan(&data)
	if err == nil {
		if err := json.Unmarshal(data, &out); err != nil {
			return out, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if out.Revision != req.ExpectedRevision {
		return out, domain.Fail(domain.Conflict, "private knowledge revision changed")
	}
	out = Knowledge{ID: id, Kind: req.Kind, Name: req.Name, Content: req.Content, Revision: out.Revision + 1, Archived: req.Archived, Selected: req.Selected, UpdatedAt: time.Now().UTC().Unix()}
	data, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge(id,data,archived) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,archived=excluded.archived", id, data, out.Archived); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_history(id,revision,data) VALUES(?,?,?)", id, out.Revision, data); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_history WHERE id = ? AND revision NOT IN (SELECT revision FROM knowledge_history WHERE id = ? ORDER BY revision DESC LIMIT 3)", id, id); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *State) Knowledge(ctx context.Context, id string) (Knowledge, error) {
	var out Knowledge
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM knowledge WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return out, domain.Fail(domain.NotFound, "private knowledge not found")
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

func (s *State) KnowledgeList(ctx context.Context, archived bool) ([]Knowledge, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM knowledge WHERE archived = 0 OR ? ORDER BY id", archived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Knowledge{}
	for rows.Next() {
		var data []byte
		var k Knowledge
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *State) KnowledgeHistory(ctx context.Context, id string) ([]Knowledge, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM knowledge_history WHERE id = ? ORDER BY revision DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Knowledge{}
	for rows.Next() {
		var data []byte
		var k Knowledge
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *State) SelectedKnowledge(ctx context.Context) ([]Knowledge, error) {
	items, err := s.KnowledgeList(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []Knowledge{}
	size := 0
	for _, item := range items {
		if item.Selected {
			size += len(item.Content)
			out = append(out, item)
		}
	}
	if size > 64<<10 {
		return nil, domain.Fail(domain.Prerequisite, "selected knowledge exceeds the 64 KiB prompt budget; consolidate or deselect entries")
	}
	return out, nil
}
