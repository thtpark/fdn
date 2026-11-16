package foundation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/gitrepo"
)

func requireProjectContributor(ctx context.Context, q queryer, actor, projectID string) error {
	a, err := activeIdentitySubject(ctx, q, actor)
	if err != nil {
		return err
	}
	project, err := get[domain.Project](ctx, q, "projects", projectID)
	if err != nil {
		return err
	}
	if project.Archived {
		return domain.Fail(domain.Denied, "archived project does not grant source or artifact access")
	}
	if authority(a, project) {
		return nil
	}
	var assigned bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=? AND assignee_id=?
AND archived=0 AND status NOT IN ('done','wont-do','cancelled'))`, projectID, actor).Scan(&assigned)
	if err != nil {
		return err
	}
	if !assigned {
		return domain.Fail(domain.Denied, "source and registry bytes require current owning-project authority or assignment")
	}
	return nil
}

func requireRepositoryReader(ctx context.Context, q queryer, actor string, repo domain.HostedRepository) error {
	if repo.Public {
		_, err := activeIdentitySubject(ctx, q, actor)
		return err
	}
	return requireProjectContributor(ctx, q, actor, repo.ProjectID)
}

func (s *Store) registryBytes(ctx context.Context, q queryer, artifact domain.DeliveryArtifact, limit int64) ([]byte, error) {
	file, err := readFile(ctx, q, artifact.FileID)
	if err != nil {
		return nil, err
	}
	if file.RegistryID != artifact.ID || file.State != "ready" || file.Size != artifact.Size ||
		file.SHA256 != artifact.SHA256 || file.Size > limit {
		return nil, domain.Fail(domain.Conflict, "registry artifact does not match its durable immutable bytes")
	}
	return s.fileBytes(ctx, q, file, limit)
}

func (s *Store) fileBytes(ctx context.Context, q queryer, file domain.SharedFile, limit int64) ([]byte, error) {
	if file.State != "ready" || file.Size < 1 || file.Size > limit {
		return nil, domain.Fail(domain.Invalid, "source or artifact must be a complete bounded file")
	}
	if err := requireFileContentReady(ctx, q, file.ID); err != nil {
		return nil, err
	}
	stream, err := s.files.OpenFile(file.ID, file.Name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stream, limit+1))
	err = errors.Join(readErr, stream.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != file.Size || domain.Hash(string(data)) != file.SHA256 {
		return nil, domain.Fail(domain.Prerequisite, "registry source bytes failed complete size/hash verification")
	}
	return data, nil
}

func (s *Store) submitArtifact(ctx context.Context, tx *sql.Tx, actor, project, repo, fileID, kind string, limit int64) (domain.DeliveryArtifact, []byte, error) {
	file, err := readFile(ctx, tx, fileID)
	if err != nil {
		return domain.DeliveryArtifact{}, nil, err
	}
	if file.OwnerID != actor || file.TransferTo != "" || file.DeleteAfterDownload || file.TeamReadable {
		return domain.DeliveryArtifact{}, nil, domain.Fail(domain.Denied, "registry submission requires the uploader's private retained file and explicit custody; transfers/publications cannot be adopted")
	}
	if file.RegistryID != "" {
		artifact, err := get[domain.DeliveryArtifact](ctx, tx, "delivery_artifacts", file.RegistryID)
		if err != nil {
			return artifact, nil, err
		}
		if artifact.ProjectID != project || artifact.RepositoryID != repo || artifact.Kind != kind {
			return artifact, nil, domain.Fail(domain.Conflict, "artifact custody is already bound to another source or product")
		}
		data, err := s.registryBytes(ctx, tx, artifact, limit)
		return artifact, data, err
	}
	data, err := s.fileBytes(ctx, tx, file, limit)
	if err != nil {
		return domain.DeliveryArtifact{}, nil, err
	}
	artifact := domain.DeliveryArtifact{ID: domain.NewID("artifact"), FileID: file.ID, RepositoryID: repo, ProjectID: project,
		OwnerID: actor, Kind: kind, Name: file.Name, SHA256: file.SHA256, Size: file.Size, CreatedAt: s.timestamp()}
	raw, err := encode(artifact)
	if err != nil {
		return artifact, nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO delivery_artifacts(id,data,file_id,project_id) VALUES(?,?,?,?)",
		artifact.ID, raw, artifact.FileID, artifact.ProjectID); err != nil {
		return artifact, nil, err
	}
	file.RegistryID = artifact.ID
	if _, err := tx.ExecContext(ctx, "DELETE FROM file_grants WHERE file_id=?", file.ID); err != nil {
		return artifact, nil, err
	}
	if err := saveFile(ctx, tx, file); err != nil {
		return artifact, nil, err
	}
	if err := fileUpdate(ctx, tx, file); err != nil {
		return artifact, nil, err
	}
	return artifact, data, nil
}

func (s *Store) saveRepository(ctx context.Context, tx *sql.Tx, repo domain.HostedRepository) error {
	if len(repo.Refs) > domain.MaxRepositoryRefs {
		return domain.Fail(domain.Invalid, "repository ref quota exceeded")
	}
	slices.SortFunc(repo.Refs, func(a, b domain.GitRef) int { return strings.Compare(a.Name, b.Name) })
	raw, err := encode(repo)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,data,project_id) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, repo.ID, raw, repo.ProjectID)
	return err
}

func (s *Store) CreateRepository(ctx context.Context, actor string, req domain.RepositoryRequest) (domain.HostedRepository, error) {
	return mutate(ctx, s, actor, "repository.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.HostedRepository, error) {
		var zero domain.HostedRepository
		if !a.Founder {
			return zero, domain.Fail(domain.Denied, "only Founder may create a hosted repository and its source/visibility policy")
		}
		project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return zero, err
		}
		if project.Archived {
			return zero, domain.Fail(domain.Prerequisite, "repository project is archived")
		}
		if err := domain.CheckText(req.Name, "repository name", 128, true); err != nil {
			return zero, err
		}
		if err := domain.CheckText(req.Description, "repository description", 8192, false); err != nil {
			return zero, err
		}
		branch := req.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		if err := gitrepo.ValidateRef("refs/heads/" + branch); err != nil {
			return zero, err
		}
		repo := domain.HostedRepository{ID: domain.NewID("repository"), ProjectID: project.ID, Name: req.Name,
			Description: req.Description, DefaultBranch: branch, Public: req.Public, Revision: 1,
			CreatedBy: actor, CreatedAt: s.timestamp(), Refs: []domain.GitRef{}}
		if req.BundleFileID != "" {
			artifact, data, err := s.submitArtifact(ctx, tx, actor, project.ID, repo.ID, req.BundleFileID, "git-bundle", domain.MaxGitBundleBytes)
			if err != nil {
				return zero, err
			}
			bundle, err := gitrepo.Open(data)
			if err != nil {
				return zero, err
			}
			for name, commit := range bundle.Refs() {
				repo.Refs = append(repo.Refs, domain.GitRef{Name: name, Commit: commit, ArtifactID: artifact.ID})
			}
			if _, ok := repositoryRef(repo, "refs/heads/"+branch); !ok {
				return zero, domain.Fail(domain.Invalid, "imported source bundle does not contain the declared default branch")
			}
		}
		if err := s.saveRepository(ctx, tx, repo); err != nil {
			return zero, err
		}
		return repo, s.deliveryUpdate(ctx, tx, "repository", repo.ID, project.ID)
	})
}

func repositoryRef(repo domain.HostedRepository, name string) (domain.GitRef, bool) {
	for _, ref := range repo.Refs {
		if ref.Name == name {
			return ref, true
		}
	}
	return domain.GitRef{}, false
}

func (s *Store) Repository(ctx context.Context, actor, id string) (domain.HostedRepository, error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return domain.HostedRepository{}, err
	}
	defer snapshot.Close()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return domain.HostedRepository{}, err
	}
	repo, err := get[domain.HostedRepository](ctx, snapshot, "repositories", id)
	if err == nil {
		err = requireRepositoryReader(ctx, snapshot, actor, repo)
	}
	return repo, err
}

func (s *Store) Repositories(ctx context.Context, actor, project, cursor string, limit int) (domain.Page[domain.HostedRepository], error) {
	page, err := deliveryPage[domain.HostedRepository](ctx, s, actor, "repositories", project, "", cursor, limit)
	if err != nil {
		return page, err
	}
	readable := make([]domain.HostedRepository, 0, len(page.Items))
	for _, repo := range page.Items {
		if err := requireRepositoryReader(ctx, s.db, actor, repo); err != nil {
			if domain.AsFault(err).Code == domain.Denied {
				continue
			}
			return page, err
		}
		readable = append(readable, repo)
	}
	page.Items = readable
	return page, nil
}

func (s *Store) repositoryBundle(ctx context.Context, q queryer, repo domain.HostedRepository, commit string) (*gitrepo.Bundle, error) {
	if !gitrepo.Commit(commit) {
		return nil, domain.Fail(domain.Invalid, "repository source must name an exact SHA-1 commit")
	}
	seen := make(map[string]bool)
	for _, ref := range repo.Refs {
		if seen[ref.ArtifactID] {
			continue
		}
		seen[ref.ArtifactID] = true
		artifact, err := get[domain.DeliveryArtifact](ctx, q, "delivery_artifacts", ref.ArtifactID)
		if err != nil {
			return nil, err
		}
		data, err := s.registryBytes(ctx, q, artifact, domain.MaxGitBundleBytes)
		if err != nil {
			return nil, err
		}
		bundle, err := gitrepo.Open(data)
		if err != nil {
			return nil, err
		}
		if _, err := bundle.Tree(commit); err == nil {
			return bundle, nil
		}
	}
	return nil, domain.Fail(domain.NotFound, "commit is not present in the retained hosted source")
}

func (s *Store) validateTaskSource(ctx context.Context, q queryer, project domain.Project, source *domain.TaskSource) error {
	if source == nil {
		return nil
	}
	repo, err := get[domain.HostedRepository](ctx, q, "repositories", source.RepositoryID)
	if err != nil {
		return err
	}
	if repo.ProjectID != project.ID {
		return domain.Fail(domain.Denied, "task source must belong to its own project")
	}
	if source.Ref != "" {
		if err := gitrepo.ValidateRef(source.Ref); err != nil {
			return err
		}
		if !strings.HasPrefix(source.Ref, "refs/heads/work/") {
			return domain.Fail(domain.Invalid, "contribution task source ref must use the work namespace")
		}
	}
	if source.ProductID != "" {
		product, err := get[domain.SoftwareProduct](ctx, q, "software_products", source.ProductID)
		if err != nil {
			return err
		}
		if product.ProjectID != project.ID || product.RepositoryID != repo.ID {
			return domain.Fail(domain.Denied, "task product is not bound to this source/project")
		}
	}
	if source.Commit == "" && len(repo.Refs) == 0 && source.Ref != "" {
		return nil
	}
	_, err = s.repositoryBundle(ctx, q, repo, source.Commit)
	return err
}

func (s *Store) PushRepository(ctx context.Context, actor, id string, req domain.RepositoryPush) (domain.HostedRepository, error) {
	return mutate(ctx, s, actor, "repository.push:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.HostedRepository, error) {
		repo, err := get[domain.HostedRepository](ctx, tx, "repositories", id)
		if err != nil {
			return repo, err
		}
		task, err := get[domain.Task](ctx, tx, "tasks", req.TaskID)
		if err != nil {
			return repo, err
		}
		if task.ActiveRun == nil || task.ActiveRun.ID != req.RunID || task.ActiveRun.StopRequested ||
			task.ActiveRun.AgentID != a.ID || task.ActiveRun.Kind == domain.VerificationRun || task.AssigneeID != actor ||
			task.Source == nil || task.Source.RepositoryID != repo.ID || task.Source.Ref == "" {
			return repo, domain.Fail(domain.Denied, "push requires the current assigned approved source task and its active coding run")
		}
		if !a.Founder && a.Mode != "ai" {
			return repo, domain.Fail(domain.Prerequisite, "Founder pause prevents new source mutations")
		}
		if len(req.Updates) != 1 || req.Updates[0].Name != task.Source.Ref ||
			!strings.HasPrefix(task.Source.Ref, "refs/heads/work/"+actor+"/") || !gitrepo.Commit(req.Updates[0].New) {
			return repo, domain.Fail(domain.Denied, "only the exact task-bound actor work ref may be pushed; protected refs/force/delete are excluded")
		}
		update := req.Updates[0]
		old, exists := repositoryRef(repo, update.Name)
		if (exists && old.Commit != update.Old) || (!exists && update.Old != "") {
			return repo, domain.Fail(domain.Conflict, "source ref changed; reconcile the original push")
		}
		artifact, data, err := s.submitArtifact(ctx, tx, actor, repo.ProjectID, repo.ID, req.BundleFileID, "git-bundle", domain.MaxGitBundleBytes)
		if err != nil {
			return repo, err
		}
		bundle, err := gitrepo.Open(data)
		if err != nil {
			return repo, err
		}
		if bundle.Refs()[update.Name] != update.New {
			return repo, domain.Fail(domain.Invalid, "Git bundle does not contain the declared exact task ref")
		}
		ancestor := task.Source.Commit
		if exists {
			ancestor = old.Commit
		}
		if ancestor != "" {
			forward, err := bundle.IsAncestor(ancestor, update.New)
			if err != nil || !forward {
				return repo, domain.Fail(domain.Conflict, "push must preserve the approved source and ref history; no force replacement")
			}
			retained, err := s.repositoryBundle(ctx, tx, repo, ancestor)
			if err != nil {
				return repo, err
			}
			expected, err := retained.Fingerprint(ancestor)
			if err != nil {
				return repo, err
			}
			actual, err := bundle.Fingerprint(ancestor)
			if err != nil || expected != actual {
				return repo, domain.Fail(domain.Conflict, "incoming source substituted bytes behind a retained commit locator")
			}
		}
		if err := s.repositoryQuota(ctx, tx, repo.ID); err != nil {
			return repo, err
		}
		next := domain.GitRef{Name: update.Name, Commit: update.New, ArtifactID: artifact.ID}
		if exists {
			for i := range repo.Refs {
				if repo.Refs[i].Name == next.Name {
					repo.Refs[i] = next
				}
			}
		} else {
			repo.Refs = append(repo.Refs, next)
		}
		repo.Revision++
		if err := s.saveRepository(ctx, tx, repo); err != nil {
			return repo, err
		}
		return repo, s.deliveryUpdate(ctx, tx, "repository", repo.ID, repo.ProjectID)
	})
}

func (s *Store) repositoryQuota(ctx context.Context, tx *sql.Tx, id string) error {
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(json_extract(data,'$.size')),0) FROM delivery_artifacts
WHERE json_extract(data,'$.repository_id')=?`, id).Scan(&size); err != nil {
		return err
	}
	if size > domain.MaxFileSize {
		return domain.Fail(domain.Prerequisite, "retained source repository exceeded its 1 GiB quota; recovery history was not erased")
	}
	return nil
}

func (s *Store) captureResultSource(ctx context.Context, tx *sql.Tx, task *domain.Task) error {
	if task.Source == nil {
		return nil
	}

	source := *task.Source
	if source.Ref != "" {
		repo, err := get[domain.HostedRepository](ctx, tx, "repositories", source.RepositoryID)
		if err != nil {
			return err
		}
		ref, ok := repositoryRef(repo, source.Ref)
		if !ok {
			return domain.Fail(domain.Prerequisite, "contribution completion requires its actual pushed ref")
		}
		source.Commit = ref.Commit
	}
	task.ResultSource = &source
	return nil
}

func (s *Store) requireIntegratedSource(ctx context.Context, tx *sql.Tx, task domain.Task) error {
	if task.ResultSource == nil || task.ResultSource.Ref == "" {
		return nil
	}
	repo, err := get[domain.HostedRepository](ctx, tx, "repositories", task.ResultSource.RepositoryID)
	if err != nil {
		return err
	}
	head, ok := repositoryRef(repo, "refs/heads/"+repo.DefaultBranch)
	if !ok {
		return domain.Fail(domain.Prerequisite, "contribution acceptance requires an actual independently merged integration ref")
	}
	bundle, err := s.repositoryBundle(ctx, tx, repo, head.Commit)
	if err != nil {
		return err
	}
	if !bundle.Contains(task.ResultSource.Commit) {
		return domain.Fail(domain.Prerequisite, "reported contribution is not present in the integration history")
	}
	included, err := bundle.IsAncestor(task.ResultSource.Commit, head.Commit)
	if err != nil {
		return err
	}
	if !included {
		return domain.Fail(domain.Prerequisite, "exact reported contribution is not integrated; a PR URL or success reply cannot accept it")
	}
	return nil
}

func (s *Store) MergeRepository(ctx context.Context, actor, id string, req domain.RepositoryMerge) (domain.HostedRepository, error) {
	return mutate(ctx, s, actor, "repository.merge:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.HostedRepository, error) {
		repo, err := get[domain.HostedRepository](ctx, tx, "repositories", id)
		if err != nil {
			return repo, err
		}
		task, err := get[domain.Task](ctx, tx, "tasks", req.TaskID)
		if err != nil {
			return repo, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", repo.ProjectID)
		if err != nil {
			return repo, err
		}
		if task.ActiveRun == nil || task.ActiveRun.ID != req.RunID || task.ActiveRun.AgentID != actor ||
			task.ActiveRun.Kind != domain.VerificationRun || task.ActiveRun.StopRequested ||
			task.AssigneeID == actor || !domain.EligibleApprover(a, project, task, true) ||
			task.ResultSource == nil || task.ResultSource.RepositoryID != repo.ID || task.ResultSource.Ref != req.SourceRef ||
			task.ResultSource.Commit != req.SourceCommit || task.SpecGeneration != req.SpecGeneration || task.ResultGeneration != req.ResultGeneration {
			return repo, domain.Fail(domain.Denied, "integration requires the current independent verification run and exact reported source/result")
		}
		base, ok := repositoryRef(repo, "refs/heads/"+repo.DefaultBranch)
		if (!ok && req.BaseCommit != "") || (ok && base.Commit != req.BaseCommit) {
			return repo, domain.Fail(domain.Conflict, "integration base changed; review a new exact merge candidate")
		}
		tip, ok := repositoryRef(repo, req.SourceRef)
		if !ok || tip.Commit != req.SourceCommit {
			return repo, domain.Fail(domain.Conflict, "contribution head changed since verification")
		}
		if err := domain.CheckText(req.Reason, "integration evidence", 2048, true); err != nil {
			return repo, err
		}
		artifact, data, err := s.submitArtifact(ctx, tx, actor, repo.ProjectID, repo.ID, req.BundleFileID, "git-bundle", domain.MaxGitBundleBytes)
		if err != nil {
			return repo, err
		}
		bundle, err := gitrepo.Open(data)
		if err != nil {
			return repo, err
		}
		for _, prior := range []string{base.Commit, tip.Commit} {
			if prior == "" {
				continue
			}
			ancestor, err := bundle.IsAncestor(prior, req.Commit)
			if err != nil || !ancestor {
				return repo, domain.Fail(domain.Conflict, "merge candidate must retain the exact reviewed base and contribution")
			}
			retained, err := s.repositoryBundle(ctx, tx, repo, prior)
			if err != nil {
				return repo, err
			}
			expected, err := retained.Fingerprint(prior)
			if err != nil {
				return repo, err
			}
			actual, err := bundle.Fingerprint(prior)
			if err != nil || expected != actual {
				return repo, domain.Fail(domain.Conflict, "merge candidate changed retained source bytes behind a commit locator")
			}
		}
		if req.Commit != tip.Commit {
			parents, err := bundle.Parents(req.Commit)
			if err != nil || len(parents) != 2 || parents[0] != base.Commit || parents[1] != tip.Commit {
				return repo, domain.Fail(domain.Invalid, "integration requires the exact two-parent merge candidate or reviewed fast-forward")
			}
		}
		updated := false
		for i := range repo.Refs {
			if repo.Refs[i].Name == base.Name {
				repo.Refs[i] = domain.GitRef{Name: base.Name, Commit: req.Commit, ArtifactID: artifact.ID}
				updated = true
			}
		}
		if !updated {
			repo.Refs = append(repo.Refs, domain.GitRef{Name: "refs/heads/" + repo.DefaultBranch, Commit: req.Commit, ArtifactID: artifact.ID})
		}
		repo.Revision++
		if err := s.repositoryQuota(ctx, tx, repo.ID); err != nil {
			return repo, err
		}
		if err := s.saveRepository(ctx, tx, repo); err != nil {
			return repo, err
		}
		return repo, s.deliveryUpdate(ctx, tx, "repository", repo.ID, repo.ProjectID)
	})
}

func (s *Store) RepositoryFile(ctx context.Context, actor, id, commit, path string) (domain.RepositoryFile, error) {
	repo, err := s.Repository(ctx, actor, id)
	if err != nil {
		return domain.RepositoryFile{}, err
	}
	bundle, err := s.repositoryBundle(ctx, s.db, repo, commit)
	if err != nil {
		return domain.RepositoryFile{}, err
	}
	data, err := bundle.File(commit, path, domain.MaxBody/2)
	return domain.RepositoryFile{RepositoryID: id, Commit: commit, Path: path, Data: data, SHA256: domain.Hash(string(data))}, err
}

func (s *Store) deliveryUpdate(ctx context.Context, tx *sql.Tx, topic, id, project string) error {
	raw, err := encode(domain.Update{Topic: topic, SourceID: id, ProjectID: project})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO update_changes(source_table,source_id,after_data)
VALUES('delivery',?,?) ON CONFLICT(source_table,source_id) DO UPDATE SET after_data=excluded.after_data`, id, raw)
	return err
}

func (s *Store) Artifact(ctx context.Context, actor, id string) (domain.DeliveryArtifact, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.DeliveryArtifact{}, err
	}
	artifact, err := get[domain.DeliveryArtifact](ctx, s.db, "delivery_artifacts", id)
	if err == nil {
		err = s.requireArtifactReader(ctx, s.db, actor, artifact)
	}
	return artifact, err
}

func (s *Store) requireArtifactReader(ctx context.Context, q queryer, actor string, artifact domain.DeliveryArtifact) error {
	if artifact.RepositoryID != "" {
		repo, err := get[domain.HostedRepository](ctx, q, "repositories", artifact.RepositoryID)
		if err != nil {
			return err
		}
		return requireRepositoryReader(ctx, q, actor, repo)
	}
	if err := requireProjectContributor(ctx, q, actor, artifact.ProjectID); err == nil {
		return nil
	} else if domain.AsFault(err).Code != domain.Denied {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.data FROM software_consumers c JOIN software_releases r
ON r.id=json_extract(c.data,'$.dependency.release_id') WHERE EXISTS
(SELECT 1 FROM json_each(json_extract(r.data,'$.manifest.platforms')) p WHERE json_extract(p.value,'$.artifact_id')=?)`, artifact.ID)
	if err != nil {
		return err
	}
	consumers, err := scanJSON[domain.SoftwareConsumer](rows)
	if err != nil {
		return err
	}
	for _, consumer := range consumers {
		if err := requireProjectContributor(ctx, q, actor, consumer.ProjectID); err == nil {
			return nil
		} else if domain.AsFault(err).Code != domain.Denied {
			return err
		}
	}
	rows, err = q.QueryContext(ctx, `SELECT data FROM software_deployments WHERE target_id=? AND state IN ('queued','staged')
AND EXISTS(SELECT 1 FROM json_each(json_extract(software_deployments.data,'$.permit.proposal.release.manifest.platforms')) p
WHERE json_extract(p.value,'$.artifact_id')=?)`, actor, artifact.ID)
	if err != nil {
		return err
	}
	deployments, err := scanJSON[domain.SoftwareDeployment](rows)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		challenge := deployment.Permit.Proposal.Challenge
		if err := s.softwareReporter(ctx, q, actor, challenge.TargetID, challenge.BindingID, false); err != nil {
			return err
		}
		if err := s.softwarePermitCurrent(ctx, q, deployment.Permit); err != nil {
			return err
		}
		return nil
	}
	return domain.Fail(domain.Denied, "artifact requires current project/consumer access or an exact current target installation permit")
}

func (s *Store) ArtifactChunk(ctx context.Context, actor, id string, offset int64) (domain.FileChunk, error) {
	artifact, err := s.Artifact(ctx, actor, id)
	if err != nil {
		return domain.FileChunk{}, err
	}
	return s.readArtifactChunk(ctx, artifact, offset)
}

func (s *Store) readArtifactChunk(ctx context.Context, artifact domain.DeliveryArtifact, offset int64) (domain.FileChunk, error) {
	if offset < 0 || offset%domain.FileChunkSize != 0 || offset >= artifact.Size {
		return domain.FileChunk{}, domain.Fail(domain.Invalid, "artifact offset must identify an existing aligned chunk")
	}

	file, err := readFile(ctx, s.db, artifact.FileID)
	if err != nil {
		return domain.FileChunk{}, err
	}
	if err := requireFileContentReady(ctx, s.db, file.ID); err != nil {
		return domain.FileChunk{}, err
	}
	stream, err := s.files.OpenFile(file.ID, file.Name)
	if err != nil {
		return domain.FileChunk{}, err
	}
	data := make([]byte, min(int64(domain.FileChunkSize), artifact.Size-offset))
	_, readErr := stream.ReadAt(data, offset)
	err = errors.Join(readErr, stream.Close())
	if err != nil {
		return domain.FileChunk{}, err
	}
	var expected string
	if err := s.db.QueryRowContext(ctx, "SELECT sha256 FROM file_chunks WHERE file_id=? AND offset=?", file.ID, offset).Scan(&expected); err != nil {
		return domain.FileChunk{}, err
	}
	if domain.Hash(string(data)) != expected {
		return domain.FileChunk{}, domain.Fail(domain.Prerequisite, "artifact chunk failed integrity verification")
	}
	return domain.FileChunk{FileID: artifact.ID, Offset: offset, Data: bytes.Clone(data), SHA256: expected}, nil
}

func (s *Store) NativeArtifactChunk(ctx context.Context, id string, offset int64) (domain.FileChunk, error) {
	artifact, err := s.NativeArtifact(ctx, id)
	if err != nil {
		return domain.FileChunk{}, err
	}
	return s.readArtifactChunk(ctx, artifact, offset)
}
