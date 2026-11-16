package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/software"
)

func deliveryPlatform(value string) bool {
	return slices.Contains([]string{"linux-amd64", "linux-arm64", "darwin-arm64"}, value)
}

func (s *Store) CreateSoftwareProduct(ctx context.Context, actor string, req domain.SoftwareProductRequest) (domain.SoftwareProduct, error) {
	return mutate(ctx, s, actor, "software.product", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareProduct, error) {
		var zero domain.SoftwareProduct
		if !a.Founder {
			return zero, domain.Fail(domain.Denied, "only Founder may register software source, ownership and trust policy")
		}
		project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return zero, err
		}
		if project.Archived {
			return zero, domain.Fail(domain.Prerequisite, "product project is archived")
		}
		if !slices.Contains([]string{"foundation", "cli", "script", "library", "service", "application"}, req.Kind) ||
			!slices.Contains([]string{"internal", "fork", "vendor", "unknown"}, req.Provenance) || len(req.Capabilities) > 32 || len(req.RecipePaths) > 3 {
			return zero, domain.Fail(domain.Invalid, "unsupported software kind, provenance or capability/recipe bounds")
		}
		for _, field := range []struct{ text, name string }{{req.Name, "software name"}, {req.License, "source/license rights"}, {req.Reason, "software registration reason"}} {
			if err := domain.CheckText(field.text, field.name, 2048, true); err != nil {
				return zero, err
			}
		}
		for _, capability := range req.Capabilities {
			if err := domain.CheckText(capability, "public capability", 128, true); err != nil || strings.ContainsAny(capability, "\r\n") {
				return zero, domain.Fail(domain.Invalid, "capabilities must be bounded single-line public labels, not permissions")
			}
		}
		if req.Provenance == "internal" || req.Provenance == "fork" {
			repo, err := get[domain.HostedRepository](ctx, tx, "repositories", req.RepositoryID)
			if err != nil {
				return zero, err
			}
			if repo.ProjectID != project.ID || (req.RecipePath == "" && len(req.RecipePaths) == 0) {
				return zero, domain.Fail(domain.Invalid, "internal/fork product requires its own source repository and recipe")
			}
			for platform, path := range req.RecipePaths {
				if !deliveryPlatform(platform) || !safeSourcePath(path) {
					return zero, domain.Fail(domain.Invalid, "recipe map requires supported platforms and relative source paths")
				}
			}
			if req.RecipePath != "" && !safeSourcePath(req.RecipePath) {
				return zero, domain.Fail(domain.Invalid, "recipe path must be a relative tracked source file")
			}
		} else if req.RepositoryID != "" || req.RecipePath != "" || len(req.RecipePaths) != 0 {
			return zero, domain.Fail(domain.Invalid, "vendor/unknown tools are observations, not internally buildable or re-signed sources")
		}
		product := domain.SoftwareProduct{ID: domain.NewID("product"), ProjectID: project.ID, RepositoryID: req.RepositoryID,
			Name: req.Name, Kind: req.Kind, Provenance: req.Provenance, RecipePath: req.RecipePath, RecipePaths: req.RecipePaths,
			Capabilities: req.Capabilities, License: req.License, Revision: 1, CreatedBy: actor, CreatedAt: s.timestamp()}
		raw, err := encode(product)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO software_products(id,data,project_id) VALUES(?,?,?)", product.ID, raw, project.ID); err != nil {
			return zero, err
		}
		return product, s.deliveryUpdate(ctx, tx, "software", product.ID, project.ID)
	})
}

func safeSourcePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Store) SoftwareProduct(ctx context.Context, actor, id string) (domain.SoftwareProduct, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwareProduct{}, err
	}
	return get[domain.SoftwareProduct](ctx, s.db, "software_products", id)
}

func (s *Store) SoftwareProducts(ctx context.Context, actor, project, cursor string, limit int) (domain.Page[domain.SoftwareProduct], error) {
	return deliveryPage[domain.SoftwareProduct](ctx, s, actor, "software_products", project, "", cursor, limit)
}

func (s *Store) CreateSoftwareBuild(ctx context.Context, actor string, req domain.SoftwareBuildRequest) (domain.SoftwareBuild, error) {
	return mutate(ctx, s, actor, "software.build", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareBuild, error) {
		var zero domain.SoftwareBuild
		product, err := get[domain.SoftwareProduct](ctx, tx, "software_products", req.ProductID)
		if err != nil {
			return zero, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", product.ProjectID)
		if err != nil {
			return zero, err
		}
		if err := requireAuthority(a, project); err != nil {
			return zero, err
		}
		task, err := get[domain.Task](ctx, tx, "tasks", req.TaskID)
		if err != nil {
			return zero, err
		}
		if task.Source == nil || task.Source.RepositoryID != product.RepositoryID || task.ProjectID != project.ID ||
			task.ActiveRun != nil || task.BuildID != "" || task.Source.Ref != "" || task.ArchivedAt != 0 || project.Archived ||
			!deliveryPlatform(req.Platform) || (product.Provenance != "internal" && product.Provenance != "fork") {
			return zero, domain.Fail(domain.Prerequisite, "build requires an unoccupied source-pinned task in the product's own project; vendor/unknown tools cannot be built")
		}
		repo, err := get[domain.HostedRepository](ctx, tx, "repositories", product.RepositoryID)
		if err != nil {
			return zero, err
		}
		bundle, err := s.repositoryBundle(ctx, tx, repo, task.Source.Commit)
		if err != nil {
			return zero, err
		}
		path := product.RecipePath
		if len(product.RecipePaths) != 0 {
			path = product.RecipePaths[req.Platform]
		}
		if path == "" {
			return zero, domain.Fail(domain.Prerequisite, "product has no recipe for this platform")
		}
		data, err := bundle.File(task.Source.Commit, path, 64<<10)
		if err != nil {
			return zero, err
		}
		var recipe domain.BuildRecipe
		if err := config.Decode(data, &recipe); err != nil {
			return zero, err
		}
		if err := software.ValidateRecipe(recipe); err != nil {
			return zero, err
		}
		if recipe.Platform != req.Platform {
			return zero, domain.Fail(domain.Conflict, "requested platform differs from its source-bound recipe")
		}
		for _, dependency := range recipe.Dependencies {
			if err := s.validateSoftwareDependency(ctx, tx, a.ID, dependency); err != nil {
				return zero, err
			}
		}
		build := domain.SoftwareBuild{ID: domain.NewID("build"), ProductID: product.ID, TaskID: task.ID,
			Source: *task.Source, Recipe: recipe, RecipeSHA256: domain.Hash(string(data)), State: "prepared",
			CreatedBy: actor, CreatedAt: s.timestamp()}
		task.BuildID = build.ID
		invalidate(&task)
		build.SpecGeneration = task.SpecGeneration
		if err := s.saveTask(ctx, tx, &task, actor, "build-prepared", "Exact source, recipe, platform and dependencies require new readiness"); err != nil {
			return zero, err
		}
		raw, err := encode(build)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO software_builds(id,data,task_id,product_id) VALUES(?,?,?,?)",
			build.ID, raw, task.ID, product.ID); err != nil {
			return zero, err
		}
		return build, s.deliveryUpdate(ctx, tx, "build", build.ID, project.ID)
	})
}

func (s *Store) SoftwareBuild(ctx context.Context, actor, id string) (domain.SoftwareBuild, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwareBuild{}, err
	}
	build, err := get[domain.SoftwareBuild](ctx, s.db, "software_builds", id)
	if err == nil {
		product, readErr := get[domain.SoftwareProduct](ctx, s.db, "software_products", build.ProductID)
		if readErr != nil {
			return build, readErr
		}
		err = requireProjectContributor(ctx, s.db, actor, product.ProjectID)
	}
	return build, err
}

func (s *Store) SubmitSoftwareBuild(ctx context.Context, actor, id string, req domain.SoftwareBuildResult) (domain.SoftwareBuild, error) {
	return mutate(ctx, s, actor, "software.build-result:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareBuild, error) {
		build, err := get[domain.SoftwareBuild](ctx, tx, "software_builds", id)
		if err != nil {
			return build, err
		}
		task, err := get[domain.Task](ctx, tx, "tasks", build.TaskID)
		if err != nil {
			return build, err
		}
		if task.BuildID != build.ID || task.Source == nil || task.Source.Commit != build.Source.Commit ||
			task.SpecGeneration != build.SpecGeneration || task.AssigneeID != a.ID || task.ActiveRun == nil ||
			task.ActiveRun.ID != req.RunID || task.ActiveRun.AgentID != a.ID || task.ActiveRun.StopRequested ||
			task.ActiveRun.Kind == domain.VerificationRun || build.State != "prepared" {
			return build, domain.Fail(domain.Denied, "build output requires its current assigned source/recipe specification and active execution")
		}
		run, err := get[domain.Run](ctx, tx, "runs", req.RunID)
		if err != nil {
			return build, err
		}
		if run.DeadlineAt != 0 && run.DeadlineAt <= s.timestamp() {
			return build, domain.Fail(domain.Conflict, "build maintenance slice expired; retain partial evidence")
		}
		for _, value := range []string{req.Toolchain, req.Summary} {
			if err := domain.CheckText(value, "bounded build evidence", 2048, true); err != nil {
				return build, err
			}
		}
		product, err := get[domain.SoftwareProduct](ctx, tx, "software_products", build.ProductID)
		if err != nil {
			return build, err
		}
		artifact, data, err := s.submitArtifact(ctx, tx, actor, product.ProjectID, "", req.BundleFileID, "software-bundle", domain.MaxSoftwareBundleBytes)
		if err != nil {
			return build, err
		}
		payloads, err := software.InspectBundle(data)
		if err != nil {
			return build, err
		}
		if len(payloads) != len(build.Recipe.Outputs) {
			return build, domain.Fail(domain.Invalid, "build archive does not contain its exact declared output set")
		}
		for _, output := range build.Recipe.Outputs {
			found := false
			for _, payload := range payloads {
				found = found || (payload.Path == output.Path && payload.Mode == output.Mode)
			}
			if !found {
				return build, domain.Fail(domain.Invalid, "build output names/modes differ from the approved source recipe")
			}
		}
		build.State, build.BuilderID, build.RunID, build.ArtifactID, build.Payloads = "submitted", actor, req.RunID, artifact.ID, payloads
		raw, err := encode(build)
		if err != nil {
			return build, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE software_builds SET data=? WHERE id=?", raw, id); err != nil {
			return build, err
		}
		return build, s.deliveryUpdate(ctx, tx, "build", build.ID, product.ProjectID)
	})
}

func (s *Store) CreateSoftwareRelease(ctx context.Context, actor string, req domain.SoftwareReleaseRequest, sign func(domain.SoftwareManifest) (string, string, error)) (domain.SoftwareRelease, error) {
	return mutate(ctx, s, actor, "software.release", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareRelease, error) {
		var zero domain.SoftwareRelease
		product, err := get[domain.SoftwareProduct](ctx, tx, "software_products", req.ProductID)
		if err != nil {
			return zero, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", product.ProjectID)
		if err != nil {
			return zero, err
		}
		if err := requireAuthority(a, project); err != nil {
			return zero, err
		}
		if project.Archived || len(req.BuildIDs) == 0 || len(req.BuildIDs) > 3 || sign == nil {
			return zero, domain.Fail(domain.Prerequisite, "release needs complete reviewed builds and a separately provisioned distribution signer")
		}
		if err := domain.CheckText(req.Reason, "release publication reason", 2048, true); err != nil {
			return zero, err
		}
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return zero, err
		}
		release := domain.SoftwareRelease{ID: domain.NewID("release"), BuildIDs: req.BuildIDs, CreatedBy: actor}
		manifest := domain.SoftwareManifest{FormatVersion: domain.SoftwareManifestVersion, InstanceID: instance,
			ReleaseID: release.ID, ProductID: product.ID, ProjectID: project.ID, Kind: product.Kind, CreatedAt: s.timestamp(),
			Platforms: []domain.SoftwarePlatform{}}
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(revision),0)+1 FROM software_releases WHERE product_id=?", product.ID).Scan(&manifest.Revision); err != nil {
			return zero, err
		}
		seen := make(map[string]bool)
		for _, id := range req.BuildIDs {
			build, err := get[domain.SoftwareBuild](ctx, tx, "software_builds", id)
			if err != nil {
				return zero, err
			}
			task, err := get[domain.Task](ctx, tx, "tasks", build.TaskID)
			if err != nil {
				return zero, err
			}
			if build.ProductID != product.ID || build.State != "submitted" || task.BuildID != build.ID ||
				task.Status != domain.Done || task.SpecGeneration != build.SpecGeneration || task.Verification == nil ||
				task.Verification.ActorID == build.BuilderID || build.BuilderID == actor || seen[build.Recipe.Platform] {
				return zero, domain.Fail(domain.Denied, "release requires distinct platform builds with current independent task verification; a builder cannot promote itself")
			}
			seen[build.Recipe.Platform] = true
			if manifest.Version == "" {
				manifest.Version, manifest.Source = build.Recipe.Version, build.Source
			} else if manifest.Version != build.Recipe.Version || manifest.Source.Commit != build.Source.Commit || manifest.Source.RepositoryID != build.Source.RepositoryID {
				return zero, domain.Fail(domain.Conflict, "all platform builds must bind one exact source and version")
			}
			artifact, err := get[domain.DeliveryArtifact](ctx, tx, "delivery_artifacts", build.ArtifactID)
			if err != nil {
				return zero, err
			}
			if _, err := s.registryBytes(ctx, tx, artifact, domain.MaxSoftwareBundleBytes); err != nil {
				return zero, err
			}
			manifest.Platforms = append(manifest.Platforms, domain.SoftwarePlatform{Platform: build.Recipe.Platform,
				ArtifactID: artifact.ID, SHA256: artifact.SHA256, Size: artifact.Size, Profile: build.Recipe.Profile,
				Payloads: build.Payloads, Dependencies: build.Recipe.Dependencies})
		}
		if len(product.RecipePaths) != 0 && len(seen) != len(product.RecipePaths) {
			return zero, domain.Fail(domain.Prerequisite, "all product platform recipes must complete before atomic release publication")
		}
		slices.SortFunc(manifest.Platforms, func(a, b domain.SoftwarePlatform) int { return strings.Compare(a.Platform, b.Platform) })
		release.Manifest = manifest
		release.KeyID, release.Signature, err = sign(manifest)
		if err != nil {
			return zero, err
		}
		raw, err := encode(release)
		if err != nil {
			return zero, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO software_releases(id,data,product_id,revision) VALUES(?,?,?,?)",
			release.ID, raw, product.ID, manifest.Revision); err != nil {
			return zero, err
		}
		return release, s.deliveryUpdate(ctx, tx, "release", release.ID, project.ID)
	})
}

func (s *Store) SoftwareRelease(ctx context.Context, actor, id string) (domain.SoftwareRelease, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwareRelease{}, err
	}
	return get[domain.SoftwareRelease](ctx, s.db, "software_releases", id)
}

func (s *Store) RevokeSoftwareRelease(ctx context.Context, actor, id string, req domain.SoftwareReleaseChange) (domain.SoftwareRelease, error) {
	return mutate(ctx, s, actor, "software.release-revoke:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareRelease, error) {
		release, err := get[domain.SoftwareRelease](ctx, tx, "software_releases", id)
		if err != nil {
			return release, err
		}
		project, err := get[domain.Project](ctx, tx, "projects", release.Manifest.ProjectID)
		if err != nil {
			return release, err
		}
		if err := requireAuthority(a, project); err != nil {
			return release, err
		}
		if err := domain.CheckText(req.Reason, "release revocation reason", 2048, true); err != nil {
			return release, err
		}
		release.Revoked = true
		raw, err := encode(release)
		if err != nil {
			return release, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE software_releases SET data=? WHERE id=?", raw, id)
		return release, err
	})
}

func (s *Store) validateSoftwareDependency(ctx context.Context, q queryer, actor string, dependency domain.SoftwareDependency) error {
	if !slices.Contains([]string{"build", "runtime", "workspace"}, dependency.Purpose) {
		return domain.Fail(domain.Invalid, "dependency purpose must be build, runtime or workspace")
	}
	release, err := get[domain.SoftwareRelease](ctx, q, "software_releases", dependency.ReleaseID)
	if err != nil {
		return err
	}
	if release.Revoked || release.Manifest.ProductID != dependency.ProductID {
		return domain.Fail(domain.Conflict, "dependency release is revoked or belongs to another product")
	}
	for _, platform := range release.Manifest.Platforms {
		if platform.Platform == dependency.Platform && platform.SHA256 == dependency.SHA256 {
			artifact, err := get[domain.DeliveryArtifact](ctx, q, "delivery_artifacts", platform.ArtifactID)
			if err != nil {
				return err
			}
			return s.requireArtifactReader(ctx, q, actor, artifact)
		}
	}
	return domain.Fail(domain.Conflict, "dependency must bind an exact published platform digest")
}

func (s *Store) BindSoftwareConsumer(ctx context.Context, actor string, req domain.SoftwareConsumerRequest) (domain.SoftwareConsumer, error) {
	return mutate(ctx, s, actor, "software.consumer", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareConsumer, error) {
		var zero domain.SoftwareConsumer
		project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
		if err != nil {
			return zero, err
		}
		if err := requireAuthority(a, project); err != nil {
			return zero, err
		}
		if project.Archived {
			return zero, domain.Fail(domain.Prerequisite, "consumer project is archived")
		}
		if err := s.validateSoftwareDependency(ctx, tx, actor, req.Dependency); err != nil {
			return zero, err
		}
		if err := domain.CheckText(req.Reason, "consumer binding reason", 2048, true); err != nil {
			return zero, err
		}
		id := "consumer_" + domain.Hash(req.ProjectID + ":" + req.Dependency.ProductID + ":" + req.Dependency.Purpose)[:32]
		revision := int64(0)
		prior, err := get[domain.SoftwareConsumer](ctx, tx, "software_consumers", id)
		if err == nil {
			revision = prior.Revision
		} else if domain.AsFault(err).Code != domain.NotFound {
			return zero, err
		}
		if req.ExpectedRevision != revision {
			return zero, domain.Fail(domain.Conflict, "consumer version binding changed")
		}
		consumer := domain.SoftwareConsumer{ID: id, ProjectID: project.ID, Dependency: req.Dependency,
			Revision: revision + 1, UpdatedBy: actor, UpdatedAt: s.timestamp()}
		raw, err := encode(consumer)
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO software_consumers(id,data,project_id,product_id,purpose) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, id, raw, project.ID, req.Dependency.ProductID, req.Dependency.Purpose)
		return consumer, err
	})
}

func currentSoftwareBinding(ctx context.Context, q queryer, actor string) (domain.AgentIdentity, error) {
	binding := actor
	if ci, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok {
		binding = ci.BindingID()
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", binding)
	if err == nil && (identity.AgentID != actor || identity.State != "active" || identity.Generation < 1) {
		err = domain.Fail(domain.Denied, "software operation requires this computer's current enrollment")
	}
	return identity, err
}

func (s *Store) ObserveSoftware(ctx context.Context, actor string, req domain.SoftwareObservationRequest) (domain.SoftwareInventory, error) {
	return mutate(ctx, s, actor, "software.observe", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareInventory, error) {
		var zero domain.SoftwareInventory
		if !slices.Contains([]string{"observed", "unknown", "absent"}, req.Observation.State) || len(req.Observation.Manager) > 128 ||
			strings.ContainsAny(req.Observation.Version+req.Observation.Manager, "/\\\r\n") ||
			(req.Observation.SHA256 != "" && !domain.ValidHash(req.Observation.SHA256)) {
			return zero, domain.Fail(domain.Invalid, "software observations contain safe metadata only; managed state requires a native receipt")
		}
		if err := domain.CheckText(req.Observation.Version, "tool version", 128, false); err != nil {
			return zero, err
		}
		if _, err := get[domain.SoftwareProduct](ctx, tx, "software_products", req.Observation.ProductID); err != nil {
			return zero, err
		}
		identity, err := currentSoftwareBinding(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		item := domain.SoftwareInventory{BindingID: identity.BindingID(), AgentID: actor, Generation: identity.Generation,
			Observation: req.Observation, ObservedAt: s.timestamp()}
		id := "inventory_" + domain.Hash(item.BindingID + ":" + item.Observation.ProductID)[:32]
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT data FROM software_inventory WHERE id=?", id).Scan(&raw)
		if err == nil {
			var prior domain.SoftwareInventory
			if err := json.Unmarshal(raw, &prior); err != nil {
				return zero, err
			}
			item.ReleaseID, item.ReceiptID = prior.ReleaseID, prior.ReceiptID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
		raw, err = encode(item)
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO software_inventory(id,data,binding_id,product_id) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, id, raw, item.BindingID, item.Observation.ProductID)
		return item, err
	})
}
