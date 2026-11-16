package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thtpark/fdn/internal/domain"
)

func (s *Store) softwareTarget(ctx context.Context, q queryer, target, binding string) (int64, error) {
	instance, err := deliveryInstance(ctx, q)
	if err != nil {
		return 0, err
	}
	if target == instance {
		if binding != "" {
			return 0, domain.Fail(domain.Invalid, "Foundation maintenance has no enrolled worker binding")
		}
		return 1, nil
	}
	agent, err := activeIdentitySubject(ctx, q, target)
	if err != nil {
		return 0, err
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", binding)
	if err != nil {
		return 0, err
	}
	if identity.AgentID != agent.ID || identity.State != "active" || identity.Generation < 1 {
		return 0, domain.Fail(domain.Denied, "software target must identify its exact enrolled computer/key generation")
	}
	return identity.Generation, nil
}

func (s *Store) RequestSoftwarePreflight(ctx context.Context, actor string, req domain.SoftwarePreflightRequest) (domain.SoftwarePreflight, error) {
	return mutate(ctx, s, actor, "software.preflight", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwarePreflight, error) {
		var zero domain.SoftwarePreflight
		if !a.Founder {
			return zero, domain.Fail(domain.Denied, "only Founder may request native software inspection or planning")
		}
		if !slices.Contains([]string{"inspect", "challenge", "status"}, req.Native.Action) || !domain.ValidOperation(req.Native.ID) ||
			req.Native.Permit != nil || req.Native.ProductID != req.ProductID {
			return zero, domain.Fail(domain.Invalid, "preflight accepts only exact bounded product inspect/challenge/status; no install authority")
		}
		if _, err := get[domain.SoftwareProduct](ctx, tx, "software_products", req.ProductID); err != nil {
			return zero, err
		}
		generation, err := s.softwareTarget(ctx, tx, req.TargetID, req.BindingID)
		if err != nil {
			return zero, err
		}
		identity, err := currentSoftwareBinding(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM software_preflights WHERE state='queued' AND json_extract(data,'$.expires_at')>?", s.timestamp()).Scan(&pending); err != nil {
			return zero, err
		}
		if pending >= 32 {
			return zero, domain.Fail(domain.Prerequisite, "bounded native software preflight inbox is full")
		}
		control := domain.SoftwarePreflight{ID: domain.NewID("software_control"), FounderID: actor,
			FounderBinding: identity.BindingID(), Generation: identity.Generation, TargetGeneration: generation,
			Request: req, State: "queued", ExpiresAt: s.timestamp() + 900}
		raw, err := encode(control)
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO software_preflights(id,data,target_id,binding_id,state) VALUES(?,?,?,?,?)",
			control.ID, raw, req.TargetID, req.BindingID, control.State)
		return control, err
	})
}

func (s *Store) softwareControlCurrent(ctx context.Context, q queryer, control domain.SoftwarePreflight) error {
	founder, err := activeIdentitySubject(ctx, q, control.FounderID)
	if err != nil {
		return err
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", control.FounderBinding)
	if err != nil {
		return err
	}
	generation, err := s.softwareTarget(ctx, q, control.Request.TargetID, control.Request.BindingID)
	if err != nil {
		return err
	}
	if !founder.Founder || identity.State != "active" || identity.Generation != control.Generation ||
		identity.AgentID != founder.ID || generation != control.TargetGeneration || control.ExpiresAt <= s.timestamp() {
		return domain.Fail(domain.Denied, "software control authority, computer generation or fixed expiry changed")
	}
	return nil
}

func (s *Store) SoftwarePreflight(ctx context.Context, actor, id string) (domain.SoftwarePreflight, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwarePreflight{}, err
	}
	control, err := get[domain.SoftwarePreflight](ctx, s.db, "software_preflights", id)
	if err == nil && control.FounderID != actor {
		err = domain.Fail(domain.Denied, "native software planning is private to its requesting Founder device")
	}
	if err == nil {
		identity, bindErr := currentSoftwareBinding(ctx, s.db, actor)
		if bindErr != nil {
			return control, bindErr
		}
		if identity.BindingID() != control.FounderBinding || identity.Generation != control.Generation {
			return control, domain.Fail(domain.Denied, "software preflight belongs to another Founder computer/generation")
		}
	}
	return control, err
}

func (s *Store) SoftwarePreflightInbox(ctx context.Context, actor string, maintenance bool) ([]domain.SoftwarePreflight, error) {
	target, binding := actor, ""
	if maintenance {
		var err error
		target, err = deliveryInstance(ctx, s.db)
		if err != nil {
			return nil, err
		}
	} else {
		if _, err := s.actor(ctx, s.db, actor); err != nil {
			return nil, err
		}
		identity, err := currentSoftwareBinding(ctx, s.db, actor)
		if err != nil {
			return nil, err
		}
		binding = identity.BindingID()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM software_preflights WHERE target_id=? AND binding_id=? AND state='queued'
ORDER BY id LIMIT 32`, target, binding)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.SoftwarePreflight](rows)
}

func (s *Store) ReportSoftwarePreflight(ctx context.Context, actor string, maintenance bool, req domain.SoftwarePreflightReport) (domain.SoftwarePreflight, error) {
	return transact(ctx, s, actor, "software.preflight-result", req.OperationID, req, func(tx *sql.Tx) (domain.SoftwarePreflight, error) {
		control, err := get[domain.SoftwarePreflight](ctx, tx, "software_preflights", req.ID)
		if err != nil {
			return control, err
		}
		if err := s.softwareReporter(ctx, tx, actor, control.Request.TargetID, control.Request.BindingID, maintenance); err != nil {
			return control, err
		}
		if control.State != "queued" {
			old, err := encode(control.Result)
			if err != nil {
				return control, err
			}
			next, err := encode(&req.Result)
			if err != nil {
				return control, err
			}
			if string(old) != string(next) {
				return control, domain.Fail(domain.Conflict, "native preflight already has a different exact observation")
			}
			return control, nil
		}
		if control.ExpiresAt <= s.timestamp() {
			control.State = "expired"
		} else {
			if err := s.softwareControlCurrent(ctx, tx, control); err != nil {
				return control, err
			}
			if req.Result.Error != nil {
				if err := domain.CheckText(req.Result.Error.Message, "safe native preflight failure", 1024, true); err != nil {
					return control, err
				}
				control.State = "failed"
			} else if control.Request.Native.Action == "challenge" {
				challenge := req.Result.Challenge
				if challenge == nil || challenge.ID != control.Request.Native.ID ||
					challenge.TargetID != control.Request.TargetID || challenge.BindingID != control.Request.BindingID ||
					challenge.ProductID != control.Request.ProductID || challenge.Generation != control.TargetGeneration ||
					!deliveryPlatform(challenge.Platform) || !domain.ValidHash(challenge.PolicySHA256) ||
					len(challenge.Nonce) != 43 || challenge.ExpiresAt <= s.timestamp() || challenge.ExpiresAt > s.timestamp()+900 {
					return control, domain.Fail(domain.Invalid, "native challenge must prove the exact control target/product/generation and bounded lifetime")
				}
				instance, err := identityInstance(ctx, tx)
				if err != nil {
					return control, err
				}
				if challenge.InstanceID != instance {
					return control, domain.Fail(domain.Denied, "native challenge belongs to another Foundation")
				}
				control.State = "observed"
			} else {
				control.State = "observed"
			}
			control.Result = &req.Result
		}
		raw, err := encode(control)
		if err != nil {
			return control, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE software_preflights SET data=?,state=? WHERE id=?", raw, control.State, control.ID)
		return control, err
	})
}

func (s *Store) softwareReporter(ctx context.Context, q queryer, actor, target, binding string, maintenance bool) error {
	instance, err := deliveryInstance(ctx, q)
	if err != nil {
		return err
	}
	if maintenance {
		if actor != "native-maintenance" || target != instance || binding != "" {
			return domain.Fail(domain.Denied, "root maintenance can report only this exact Foundation instance")
		}
		return nil
	}
	if _, err := s.actor(ctx, q, actor); err != nil {
		return err
	}
	identity, err := currentSoftwareBinding(ctx, q, actor)
	if err != nil {
		return err
	}
	if actor != target || binding != identity.BindingID() {
		return domain.Fail(domain.Denied, "native software result belongs to another enrolled computer")
	}
	return nil
}

func (s *Store) AuthorizeSoftware(ctx context.Context, actor string, req domain.SoftwareAuthorizationRequest, sign func(domain.SoftwarePermit) (string, error)) (domain.SoftwareDeployment, error) {
	return mutate(ctx, s, actor, "software.authorize", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.SoftwareDeployment, error) {
		var zero domain.SoftwareDeployment
		if !a.Founder || a.Mode != "manual" || sign == nil {
			return zero, domain.Fail(domain.Denied, "native software installation requires an authenticated paused Founder and an exact signed proposal")
		}
		control, err := get[domain.SoftwarePreflight](ctx, tx, "software_preflights", req.PreflightID)
		if err != nil {
			return zero, err
		}
		if control.FounderID != actor || control.State != "observed" || control.Result == nil || control.Result.Challenge == nil {
			return zero, domain.Fail(domain.Prerequisite, "installation requires this Founder's exact observed native challenge")
		}
		if err := s.softwareControlCurrent(ctx, tx, control); err != nil {
			return zero, err
		}
		identity, err := currentSoftwareBinding(ctx, tx, actor)
		if err != nil {
			return zero, err
		}
		if identity.BindingID() != control.FounderBinding {
			return zero, domain.Fail(domain.Denied, "software plan belongs to another Founder computer")
		}
		challenge := req.Proposal.Challenge
		exact, err := encode(challenge)
		if err != nil {
			return zero, err
		}
		observed, err := encode(control.Result.Challenge)
		if err != nil {
			return zero, err
		}
		if string(exact) != string(observed) {
			return zero, domain.Fail(domain.Conflict, "software proposal changed the native observed challenge")
		}
		release, err := get[domain.SoftwareRelease](ctx, tx, "software_releases", req.Proposal.Release.ID)
		if err != nil {
			return zero, err
		}
		original, err := encode(release)
		if err != nil {
			return zero, err
		}
		proposed, err := encode(req.Proposal.Release)
		if err != nil {
			return zero, err
		}
		if release.Revoked || string(original) != string(proposed) || release.Manifest.ProductID != challenge.ProductID {
			return zero, domain.Fail(domain.Conflict, "install proposal differs from its immutable unrevoked internal release")
		}
		if err := s.softwareQuiescent(ctx, tx, challenge); err != nil {
			return zero, err
		}
		permit := domain.SoftwarePermit{ID: domain.NewID("software_permit"), Proposal: req.Proposal, ActorID: actor,
			ActorBindingID: identity.BindingID(), ActorGeneration: identity.Generation, IssuedAt: s.timestamp(),
			ExpiresAt: min(s.timestamp()+900, control.ExpiresAt, challenge.ExpiresAt)}
		permit.Signature, err = sign(permit)
		if err != nil {
			return zero, err
		}
		if err := s.bindSoftwareMaintenance(ctx, tx, a, req.MaintenanceID, req.PreflightID, permit.ID); err != nil {
			return zero, err
		}
		deployment := domain.SoftwareDeployment{ID: permit.ID, Permit: permit, State: "queued", CreatedAt: s.timestamp(), MaintenanceID: req.MaintenanceID}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM software_deployments WHERE target_id=? AND binding_id=?
AND state IN ('queued','staged','operator-required')`, challenge.TargetID, challenge.BindingID).Scan(&count); err != nil {
			return zero, err
		}
		if count != 0 {
			return zero, domain.Fail(domain.Conflict, "target already has pending/uncertain software maintenance; reconcile its original receipt")
		}
		raw, err := encode(deployment)
		if err != nil {
			return zero, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO software_deployments(id,data,target_id,binding_id,product_id,state) VALUES(?,?,?,?,?,?)",
			deployment.ID, raw, challenge.TargetID, challenge.BindingID, challenge.ProductID, deployment.State)
		return deployment, err
	})
}

func (s *Store) softwareQuiescent(ctx context.Context, q queryer, challenge domain.SoftwareChallenge) error {
	instance, err := deliveryInstance(ctx, q)
	if err != nil {
		return err
	}
	target := challenge.TargetID
	if target == instance {
		var active int
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE active=1 AND json_extract(data,'$.mode')!='manual'").Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return domain.Fail(domain.Prerequisite, "Foundation installation requires every active agent explicitly paused")
		}
	} else {
		agent, err := activeIdentitySubject(ctx, q, target)
		if err != nil {
			return err
		}
		generation, err := s.softwareTarget(ctx, q, target, challenge.BindingID)
		if err != nil {
			return err
		}
		if agent.Mode != "manual" || generation != challenge.Generation {
			return domain.Fail(domain.Prerequisite, "software installation requires the exact currently paused enrolled target")
		}
	}
	var busy int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE json_extract(data,'$.active_run') IS NOT NULL
AND (? OR json_extract(data,'$.active_run.agent_id')=?)`, target == instance, target).Scan(&busy); err != nil {
		return err
	}
	if busy != 0 {
		return domain.Fail(domain.Prerequisite, "active work or independent verification blocks native installation")
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE state IN ('consented','active') AND expires_at>?
AND (? OR requester_id=? OR target_id=?)`, s.timestamp(), target == instance, target, target).Scan(&busy); err != nil {
		return err
	}
	if busy != 0 {
		return domain.Fail(domain.Prerequisite, "live assistance/input must settle before native installation")
	}
	return nil
}

func (s *Store) softwarePermitCurrent(ctx context.Context, q queryer, permit domain.SoftwarePermit) error {
	if permit.ExpiresAt <= s.timestamp() || permit.Proposal.Challenge.ExpiresAt <= s.timestamp() {
		return domain.Fail(domain.Denied, "software permit/native challenge expired before admission")
	}
	founder, err := activeIdentitySubject(ctx, q, permit.ActorID)
	if err != nil {
		return err
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", permit.ActorBindingID)
	if err != nil {
		return err
	}
	if !founder.Founder || founder.Mode != "manual" || identity.State != "active" ||
		identity.AgentID != founder.ID || identity.Generation != permit.ActorGeneration {
		return domain.Fail(domain.Denied, "software permit issuer identity, device generation or pause changed")
	}
	release, err := get[domain.SoftwareRelease](ctx, q, "software_releases", permit.Proposal.Release.ID)
	if err != nil {
		return err
	}
	if release.Revoked {
		return domain.Fail(domain.Denied, "software release was revoked before installation admission")
	}
	return s.softwareQuiescent(ctx, q, permit.Proposal.Challenge)
}

func (s *Store) LeaseSoftware(ctx context.Context, actor string, maintenance bool, req domain.UpgradeLeaseRequest, sign func(domain.UpgradeLease) (string, error)) (domain.UpgradeLease, error) {
	var zero domain.UpgradeLease
	if !domain.ValidHash(req.PermitSHA256) || len(req.Nonce) != 43 {
		return zero, domain.Fail(domain.Invalid, "software lease requires its exact permit fingerprint and fresh native nonce")
	}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return zero, err
	}
	defer snapshot.Close()
	rows, err := snapshot.QueryContext(ctx, "SELECT data FROM software_deployments WHERE state IN ('queued','staged')")
	if err != nil {
		return zero, err
	}
	deployments, err := scanJSON[domain.SoftwareDeployment](rows)
	if err != nil {
		return zero, err
	}
	for _, deployment := range deployments {
		permit := deployment.Permit
		if domain.Hash(permit.Signature) != req.PermitSHA256 {
			continue
		}
		if err := s.softwareReporter(ctx, snapshot, actor, permit.Proposal.Challenge.TargetID, permit.Proposal.Challenge.BindingID, maintenance); err != nil {
			return zero, err
		}
		if err := s.softwarePermitCurrent(ctx, snapshot, permit); err != nil {
			return zero, err
		}
		lease := domain.UpgradeLease{PermitSHA256: req.PermitSHA256, Nonce: req.Nonce, IssuedAt: s.timestamp(), ExpiresAt: s.timestamp() + 30}
		lease.Signature, err = sign(lease)
		return lease, err
	}
	return zero, domain.Fail(domain.NotFound, "no exact queued software permit matches the native lease")
}

func (s *Store) SoftwareInbox(ctx context.Context, actor string, maintenance bool) ([]domain.SoftwareDeployment, error) {
	target, binding := actor, ""
	if maintenance {
		var err error
		target, err = deliveryInstance(ctx, s.db)
		if err != nil {
			return nil, err
		}
	} else {
		if _, err := s.actor(ctx, s.db, actor); err != nil {
			return nil, err
		}
		identity, err := currentSoftwareBinding(ctx, s.db, actor)
		if err != nil {
			return nil, err
		}
		binding = identity.BindingID()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM software_deployments WHERE target_id=? AND binding_id=?
AND state IN ('queued','staged') ORDER BY id LIMIT 32`, target, binding)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.SoftwareDeployment](rows)
}

func (s *Store) SoftwareDeployment(ctx context.Context, actor, id string) (domain.SoftwareDeployment, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return domain.SoftwareDeployment{}, err
	}
	deployment, err := get[domain.SoftwareDeployment](ctx, s.db, "software_deployments", id)
	if err == nil && actor != deployment.Permit.ActorID && actor != deployment.Permit.Proposal.Challenge.TargetID {
		err = domain.Fail(domain.Denied, "software deployment is private to its exact Founder and target")
	}
	return deployment, err
}

func (s *Store) ReportSoftware(ctx context.Context, actor string, maintenance bool, req domain.SoftwareResultRequest) (domain.SoftwareDeployment, error) {
	return transact(ctx, s, actor, "software.result", req.OperationID, req, func(tx *sql.Tx) (domain.SoftwareDeployment, error) {
		deployment, err := get[domain.SoftwareDeployment](ctx, tx, "software_deployments", req.Result.PermitID)
		if err != nil {
			return deployment, err
		}
		permit, result := deployment.Permit, req.Result
		challenge := permit.Proposal.Challenge
		if err := s.softwareReporter(ctx, tx, actor, challenge.TargetID, challenge.BindingID, maintenance); err != nil {
			return deployment, err
		}
		if result.PermitSHA256 != domain.Hash(permit.Signature) || result.ProductID != challenge.ProductID ||
			result.ReleaseID != permit.Proposal.Release.ID || !slices.Contains([]string{"staged", "verified", "failed", "operator-required"}, result.State) ||
			len(result.Code) > 128 {
			return deployment, domain.Fail(domain.Invalid, "software result must bind its exact signed product/release and explicit native outcome")
		}
		if deployment.Result != nil && deployment.State != "staged" {
			prior, err := encode(deployment.Result)
			if err != nil {
				return deployment, err
			}
			next, err := encode(&result)
			if err != nil {
				return deployment, err
			}
			if string(prior) != string(next) {
				return deployment, domain.Fail(domain.Conflict, "software receipt has a different settled native outcome")
			}
			return deployment, nil
		}
		if result.State == "verified" {
			matched := false
			for _, platform := range permit.Proposal.Release.Manifest.Platforms {
				matched = matched || (platform.Platform == challenge.Platform && platform.ArtifactID == result.ArtifactID && platform.SHA256 == result.SHA256)
			}
			if !matched || !domain.ValidHash(result.BackupSHA256) {
				return deployment, domain.Fail(domain.Invalid, "verified installation requires exact platform bytes and encrypted cold-backup evidence")
			}
			if !maintenance {
				generation, err := s.softwareTarget(ctx, tx, challenge.TargetID, challenge.BindingID)
				if err != nil || generation != challenge.Generation {
					return deployment, domain.Fail(domain.Conflict, "installation target generation changed; retain native receipt")
				}
				item := domain.SoftwareInventory{BindingID: challenge.BindingID, AgentID: challenge.TargetID, Generation: generation,
					ReleaseID: result.ReleaseID, ReceiptID: permit.ID, ObservedAt: s.timestamp(),
					Observation: domain.SoftwareObservation{ProductID: challenge.ProductID, State: "managed", Version: permit.Proposal.Release.Manifest.Version,
						SHA256: result.SHA256, Manager: "foundation-registry"}}
				raw, err := encode(item)
				if err != nil {
					return deployment, err
				}
				id := "inventory_" + domain.Hash(item.BindingID + ":" + challenge.ProductID)[:32]
				if _, err := tx.ExecContext(ctx, `INSERT INTO software_inventory(id,data,binding_id,product_id) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET data=excluded.data`, id, raw, item.BindingID, challenge.ProductID); err != nil {
					return deployment, err
				}
			}
		}
		deployment.State, deployment.Result = result.State, &result
		raw, err := encode(deployment)
		if err != nil {
			return deployment, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE software_deployments SET data=?,state=? WHERE id=?", raw, deployment.State, deployment.ID); err != nil {
			return deployment, err
		}
		return deployment, s.finishSoftwareMaintenance(ctx, tx, deployment.MaintenanceID)
	})
}

func (s *Store) SoftwareInventory(ctx context.Context, actor string) ([]domain.SoftwareInventory, error) {
	agent, err := s.actor(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	query := "SELECT data FROM software_inventory"
	var args []any
	if !agent.Founder {
		query += " WHERE json_extract(data,'$.agent_id')=?"
		args = append(args, actor)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id LIMIT 200", args...)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.SoftwareInventory](rows)
}

func (s *Store) SoftwareConsumers(ctx context.Context, actor, project string) ([]domain.SoftwareConsumer, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM software_consumers WHERE project_id=? ORDER BY id LIMIT 200", project)
	if err != nil {
		return nil, err
	}
	return scanJSON[domain.SoftwareConsumer](rows)
}

func (s *Store) NativeArtifact(ctx context.Context, id string) (domain.DeliveryArtifact, error) {
	artifact, err := get[domain.DeliveryArtifact](ctx, s.db, "delivery_artifacts", id)
	if err != nil {
		return artifact, err
	}
	instance, err := deliveryInstance(ctx, s.db)
	if err != nil {
		return artifact, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM software_deployments WHERE target_id=? AND state IN ('queued','staged')", instance)
	if err != nil {
		return artifact, err
	}
	deployments, err := scanJSON[domain.SoftwareDeployment](rows)
	if err != nil {
		return artifact, err
	}
	for _, deployment := range deployments {
		for _, platform := range deployment.Permit.Proposal.Release.Manifest.Platforms {
			if platform.ArtifactID == id {
				if err := s.softwarePermitCurrent(ctx, s.db, deployment.Permit); err != nil {
					return artifact, err
				}
				return artifact, nil
			}
		}
	}
	return artifact, domain.Fail(domain.Denied, "root maintenance reads only its exact authorized staged artifact, not arbitrary registry bytes")
}

func decodeNativeSoftware(data []byte, value any) error {
	if len(data) == 0 {
		return domain.Fail(domain.Invalid, "native software payload is empty")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return errors.Join(domain.Fail(domain.Invalid, "invalid retained native software payload"), err)
	}
	return nil
}
