package foundation

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
)

func publicationProject(p domain.Publication) string {
	if p.Scope == domain.ProjectNotes {
		return p.TargetID
	}
	return ""
}

func validatePublication(p domain.Publication) error {
	if !slices.Contains([]string{"file", "diary", "skill", "idea"}, p.Kind) {
		return domain.Fail(domain.Invalid, "publication kind must be file, diary, skill or idea")
	}
	if _, err := domain.NoteID(p.Scope, p.TargetID); err != nil {
		return err
	}
	if p.AllowProjectEdits && p.Scope != domain.ProjectNotes {
		return domain.Fail(domain.Invalid, "delegated editing is limited to an explicit project scope")
	}
	if err := domain.CheckText(p.Title, "publication title", 512, true); err != nil {
		return err
	}
	if err := domain.CheckText(p.Body, "publication Markdown", domain.MaxPublicationBody, len(p.Attachments) == 0); err != nil {
		return err
	}
	if p.Kind == "file" && len(p.Attachments) == 0 {
		return domain.Fail(domain.Invalid, "a file publication requires an explicitly published attachment")
	}
	return checkInlineAttachments(p.Body, p.Attachments)
}

func (s *Store) savePublication(ctx context.Context, tx *sql.Tx, p *domain.Publication, actor, reason string) error {
	content, err := encode(struct {
		Title       string                 `json:"title"`
		Body        string                 `json:"body"`
		Attachments []domain.FileReference `json:"attachments"`
	}{p.Title, p.Body, p.Attachments})
	if err != nil {
		return err
	}
	p.SHA256, p.VersionID = domain.Hash(string(content)), domain.NewID("publication_version")
	p.Revision++
	p.UpdatedBy, p.UpdatedAt = actor, s.timestamp()
	data, err := encode(p)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,data,kind,scope,target_id,archived) VALUES(?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data,archived=excluded.archived`,
		p.ID, data, p.Kind, p.Scope, p.TargetID, p.Archived); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO publication_versions(id,publication_id,data,revision) VALUES(?,?,?,?)",
		p.VersionID, p.ID, data, p.Revision); err != nil {
		return err
	}
	if err := s.history(ctx, tx, p.ID, actor, "publish", p, reason); err != nil {
		return err
	}
	return s.event(ctx, tx, "publication", p.ID, "", publicationProject(*p))
}

func (s *Store) CreatePublication(ctx context.Context, actor string, req domain.PublicationRequest) (domain.Publication, error) {
	return mutate(ctx, s, actor, "publication.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Publication, error) {
		p := domain.Publication{ID: domain.NewID("publication"), Kind: req.Kind, Scope: req.Scope, TargetID: req.TargetID,
			Title: req.Title, Body: req.Body, CreatedBy: a.ID, CreatedAt: s.timestamp(), AllowProjectEdits: req.AllowProjectEdits}
		if _, err := readNote(ctx, tx, p.Scope, p.TargetID); err != nil {
			return p, err
		}
		if p.Scope == domain.ProjectNotes {
			project, err := get[domain.Project](ctx, tx, "projects", p.TargetID)
			if err != nil {
				return p, err
			}
			if project.Archived {
				return p, domain.Fail(domain.Prerequisite, "publication project is archived")
			}
		}
		var err error
		p.Attachments, err = s.resolveAttachments(ctx, tx, a.ID, "", req.AttachmentIDs)
		if err != nil {
			return p, err
		}
		if err := validatePublication(p); err != nil {
			return p, err
		}
		err = s.savePublication(ctx, tx, &p, a.ID, "explicit team publication")
		return p, err
	})
}

func requirePublicationEditor(ctx context.Context, tx *sql.Tx, actor domain.Agent, p domain.Publication) error {
	if actor.ID == p.CreatedBy {
		return nil
	}
	if p.AllowProjectEdits && p.Scope == domain.ProjectNotes {
		project, err := get[domain.Project](ctx, tx, "projects", p.TargetID)
		if err != nil {
			return err
		}
		if !project.Archived && slices.Contains(project.Delegates, actor.ID) {
			return nil
		}
	}
	return domain.Fail(domain.Denied, "only the creator or an explicitly permitted current delegate of this publication's project may edit or archive it")
}

func (s *Store) ChangePublication(ctx context.Context, actor, id string, req domain.PublicationChange) (domain.Publication, error) {
	return mutate(ctx, s, actor, "publication.change:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.Publication, error) {
		p, err := get[domain.Publication](ctx, tx, "publications", id)
		if err != nil {
			return p, err
		}
		if err := requirePublicationEditor(ctx, tx, a, p); err != nil {
			return p, err
		}
		if err := expect(p.Revision, req.ExpectedRevision); err != nil {
			return p, err
		}
		if err := domain.CheckText(req.Reason, "publication change reason", 8192, true); err != nil {
			return p, err
		}
		if req.Title == nil && req.Body == nil && req.AttachmentIDs == nil && req.Archived == nil && req.AllowProjectEdits == nil {
			return p, domain.Fail(domain.Invalid, "publication change requires an editable field")
		}
		if req.AllowProjectEdits != nil {
			if a.ID != p.CreatedBy {
				return p, domain.Fail(domain.Denied, "only the creator may change delegated editing permission")
			}
			p.AllowProjectEdits = *req.AllowProjectEdits
		}
		if req.Title != nil {
			p.Title = *req.Title
		}
		if req.Body != nil {
			p.Body = *req.Body
		}
		if req.AttachmentIDs != nil {
			p.Attachments, err = s.resolveAttachments(ctx, tx, a.ID, "", *req.AttachmentIDs)
			if err != nil {
				return p, err
			}
		}
		if req.Archived != nil {
			p.Archived = *req.Archived
		}
		if err := validatePublication(p); err != nil {
			return p, err
		}
		err = s.savePublication(ctx, tx, &p, a.ID, req.Reason)
		return p, err
	})
}

func (s *Store) Publication(ctx context.Context, actor, id string) (domain.Publication, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.Publication{}, err
	}
	return get[domain.Publication](ctx, s.db, "publications", id)
}

func (s *Store) PublicationVersion(ctx context.Context, actor, id string) (domain.PublicationVersion, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.PublicationVersion{}, err
	}
	p, err := get[domain.Publication](ctx, s.db, "publication_versions", id)
	return domain.PublicationVersion{ID: p.VersionID, PublicationID: p.ID, Publication: p}, err
}

func (s *Store) Publications(ctx context.Context, actor, kind, scope, target, cursor string, limit int, archived bool) (domain.Page[domain.Publication], error) {
	page := domain.Page[domain.Publication]{Items: []domain.Publication{}}
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return page, err
	}
	if kind != "" && !slices.Contains([]string{"file", "diary", "skill", "idea"}, kind) {
		return page, domain.Fail(domain.Invalid, "invalid publication kind filter")
	}
	if scope != "" && !slices.Contains([]string{domain.CompanyNotes, domain.ProjectNotes, domain.AgentNotes}, scope) {
		return page, domain.Fail(domain.Invalid, "invalid publication scope filter")
	}
	if target != "" && scope != domain.ProjectNotes && scope != domain.AgentNotes {
		return page, domain.Fail(domain.Invalid, "target filter requires project or agent scope")
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "publications:" + domain.Hash(kind+":"+scope+":"+target+":"+strconv.FormatBool(archived))
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM publications WHERE id>? AND (?='' OR kind=?)
AND (?='' OR scope=?) AND (?='' OR target_id=?) AND (? OR archived=0) ORDER BY id LIMIT ?`,
		last, kind, kind, scope, scope, target, target, archived, limit+1)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.Publication](rows)
	if err == nil && len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].ID)
	}
	return page, err
}

func (s *Store) PublicationVersions(ctx context.Context, actor, id, cursor string, limit int) (domain.Page[domain.PublicationVersion], error) {
	page := domain.Page[domain.PublicationVersion]{Items: []domain.PublicationVersion{}}
	if _, err := s.Publication(ctx, actor, id); err != nil {
		return page, err
	}
	limit, err := limitSize(limit)
	if err != nil {
		return page, err
	}
	key := "publication-versions:" + id
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	revision := int64(math.MaxInt64)
	if last != "" {
		revision, err = strconv.ParseInt(last, 10, 64)
		if err != nil || revision < 1 {
			return page, domain.Fail(domain.Invalid, "invalid publication version cursor")
		}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM publication_versions WHERE publication_id=? AND revision<? ORDER BY revision DESC LIMIT ?", id, revision, limit+1)
	if err != nil {
		return page, err
	}
	versions, err := scanJSON[domain.Publication](rows)
	if err != nil {
		return page, err
	}
	if len(versions) > limit {
		versions = versions[:limit]
		page.NextCursor = makeCursor(actor, key, strconv.FormatInt(versions[limit-1].Revision, 10))
	}
	for _, p := range versions {
		page.Items = append(page.Items, domain.PublicationVersion{ID: p.VersionID, PublicationID: p.ID, Publication: p})
	}
	return page, nil
}
