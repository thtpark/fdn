package foundation

import (
	"context"
	"database/sql"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/gitrepo"
)

// UpstreamRefPrefix is the protected hosted namespace for imported upstream branch tips.
const UpstreamRefPrefix = "refs/heads/upstream/"

// UpstreamRepository records a Founder-fetched upstream branch tip and optionally
// advances the protected default branch by an exact fast-forward or two-parent
// merge of the current default and that tip. It reuses the RepositoryMerge DTO
// (TaskID/RunID/generations must be empty), so the native payload schema is
// unchanged. Foundation never contacts the upstream itself.
func (s *Store) UpstreamRepository(ctx context.Context, actor, id string, req domain.RepositoryMerge) (domain.HostedRepository, error) {
	return mutate(ctx, s, actor, "repository.upstream:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.HostedRepository, error) {
		repo, err := get[domain.HostedRepository](ctx, tx, "repositories", id)
		if err != nil {
			return repo, err
		}
		if !a.Founder {
			return repo, domain.Fail(domain.Denied, "only Founder may import upstream source or advance a protected default branch from it; assign conflicts as source tasks")
		}
		project, err := get[domain.Project](ctx, tx, "projects", repo.ProjectID)
		if err != nil {
			return repo, err
		}
		if project.Archived {
			return repo, domain.Fail(domain.Prerequisite, "repository project is archived")
		}
		if req.TaskID != "" || req.RunID != "" || req.SpecGeneration != 0 || req.ResultGeneration != 0 {
			return repo, domain.Fail(domain.Invalid, "upstream import is not a task integration; task/run/generation fields must be empty")
		}
		if err := expect(repo.Revision, req.ExpectedRevision); err != nil {
			return repo, err
		}
		if err := domain.CheckText(req.Reason, "upstream import reason", 2048, true); err != nil {
			return repo, err
		}
		if err := ValidateUpstreamRef(repo, req.SourceRef); err != nil {
			return repo, err
		}
		if !gitrepo.Commit(req.SourceCommit) || (req.Commit != "" && !gitrepo.Commit(req.Commit)) ||
			(req.BaseCommit != "" && !gitrepo.Commit(req.BaseCommit)) {
			return repo, domain.Fail(domain.Invalid, "upstream import requires exact SHA-1 commits")
		}
		defaultRef := "refs/heads/" + repo.DefaultBranch
		base, hasBase := repositoryRef(repo, defaultRef)
		if (!hasBase && req.BaseCommit != "") || (hasBase && base.Commit != req.BaseCommit) {
			return repo, domain.Fail(domain.Conflict, "default branch changed since the upstream candidate was prepared; sync again")
		}
		tracking, hasTracking := repositoryRef(repo, req.SourceRef)
		trackingChanged := !hasTracking || tracking.Commit != req.SourceCommit
		if !trackingChanged && req.Commit == "" {
			return repo, domain.Fail(domain.Prerequisite, "upstream tip is already imported and no default-branch change was requested")
		}
		artifact, data, err := s.submitArtifact(ctx, tx, actor, repo.ProjectID, repo.ID, req.BundleFileID, "git-bundle", domain.MaxGitBundleBytes)
		if err != nil {
			return repo, err
		}
		bundle, err := gitrepo.Open(data)
		if err != nil {
			return repo, err
		}
		refs := bundle.Refs()
		if trackingChanged && refs[req.SourceRef] != req.SourceCommit {
			return repo, domain.Fail(domain.Invalid, "Git bundle does not declare the exact imported upstream ref")
		}
		if !bundle.Contains(req.SourceCommit) {
			return repo, domain.Fail(domain.Invalid, "Git bundle does not contain the upstream commit")
		}
		if req.Commit != "" && refs[defaultRef] != req.Commit {
			return repo, domain.Fail(domain.Invalid, "Git bundle does not declare the exact default-branch candidate")
		}
		if hasTracking && trackingChanged {
			forward, err := bundle.IsAncestor(tracking.Commit, req.SourceCommit)
			if err != nil || !forward {
				return repo, domain.Fail(domain.Conflict, "upstream history was rewritten since the last import; Foundation will not replace retained upstream history")
			}
		}
		tips := []string{req.SourceCommit}
		if req.Commit != "" {
			if err := upstreamCandidate(bundle, base.Commit, req.SourceCommit, req.Commit); err != nil {
				return repo, err
			}
			tips = append(tips, req.Commit)
		}
		if err := s.sharedHistoryRetained(ctx, tx, repo, bundle, tips...); err != nil {
			return repo, err
		}
		if err := s.repositoryQuota(ctx, tx, repo.ID); err != nil {
			return repo, err
		}
		setRef(&repo, domain.GitRef{Name: req.SourceRef, Commit: req.SourceCommit, ArtifactID: artifact.ID})
		if req.Commit != "" {
			setRef(&repo, domain.GitRef{Name: defaultRef, Commit: req.Commit, ArtifactID: artifact.ID})
		}
		repo.Revision++
		if err := s.saveRepository(ctx, tx, repo); err != nil {
			return repo, err
		}
		if err := s.history(ctx, tx, repo.ID, actor, "repository.upstream", req, req.Reason); err != nil {
			return repo, err
		}
		return repo, s.deliveryUpdate(ctx, tx, "repository", repo.ID, repo.ProjectID)
	})
}

// ValidateUpstreamRef accepts only refs/heads/upstream/NAME refs that cannot
// collide with the repository's default branch namespace.
func ValidateUpstreamRef(repo domain.HostedRepository, ref string) error {
	if err := gitrepo.ValidateRef(ref); err != nil || !strings.HasPrefix(ref, UpstreamRefPrefix) || ref == UpstreamRefPrefix {
		return domain.Fail(domain.Invalid, "upstream tracking ref must be refs/heads/upstream/BRANCH")
	}
	if repo.DefaultBranch == "upstream" || strings.HasPrefix(repo.DefaultBranch, "upstream/") {
		return domain.Fail(domain.Invalid, "default branch collides with the protected upstream tracking namespace")
	}
	return nil
}

// sharedHistoryRetained walks the incoming history from tips and, at each first
// commit already retained by a current hosted ref, requires an identical
// whole-graph fingerprint. Equal fingerprints cover every ancestor below it, so
// third-party bytes cannot hide behind an existing SHA-1 commit locator.
func (s *Store) sharedHistoryRetained(ctx context.Context, q queryer, repo domain.HostedRepository, bundle *gitrepo.Bundle, tips ...string) error {
	var retained []*gitrepo.Bundle
	loaded := make(map[string]bool)
	for _, ref := range repo.Refs {
		if loaded[ref.ArtifactID] {
			continue
		}
		loaded[ref.ArtifactID] = true
		artifact, err := get[domain.DeliveryArtifact](ctx, q, "delivery_artifacts", ref.ArtifactID)
		if err != nil {
			return err
		}
		data, err := s.registryBytes(ctx, q, artifact, domain.MaxGitBundleBytes)
		if err != nil {
			return err
		}
		existing, err := gitrepo.Open(data)
		if err != nil {
			return err
		}
		retained = append(retained, existing)
	}
	seen := make(map[string]bool)
	pending := append([]string{}, tips...)
	for len(pending) > 0 {
		commit := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[commit] {
			continue
		}
		seen[commit] = true
		var owner *gitrepo.Bundle
		for _, existing := range retained {
			if existing.Contains(commit) {
				owner = existing
				break
			}
		}
		if owner != nil {
			expected, err := owner.Fingerprint(commit)
			if err != nil {
				return err
			}
			actual, err := bundle.Fingerprint(commit)
			if err != nil || expected != actual {
				return domain.Fail(domain.Conflict, "upstream candidate must retain the exact hosted source bytes behind each existing commit")
			}
			continue
		}
		parents, err := bundle.Parents(commit)
		if err != nil {
			return err
		}
		pending = append(pending, parents...)
	}
	return nil
}

func upstreamCandidate(bundle *gitrepo.Bundle, base, upstream, commit string) error {
	if base == "" || commit == upstream {
		if base != "" {
			forward, err := bundle.IsAncestor(base, upstream)
			if err != nil || !forward {
				return domain.Fail(domain.Conflict, "default branch has diverged from upstream; submit an exact two-parent merge")
			}
		} else if commit != upstream {
			return domain.Fail(domain.Invalid, "an empty default branch can only adopt the exact upstream tip")
		}
		return nil
	}
	parents, err := bundle.Parents(commit)
	if err != nil || len(parents) != 2 || parents[0] != base || parents[1] != upstream {
		return domain.Fail(domain.Invalid, "upstream integration requires the exact upstream fast-forward or a two-parent merge of the current default and upstream tip")
	}
	return nil
}

func setRef(repo *domain.HostedRepository, ref domain.GitRef) {
	for i := range repo.Refs {
		if repo.Refs[i].Name == ref.Name {
			repo.Refs[i] = ref
			return
		}
	}
	repo.Refs = append(repo.Refs, ref)
}
