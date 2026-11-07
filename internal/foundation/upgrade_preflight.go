package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/upgrade"
)

const upgradePreflightPrefix = "upgrade-preflight:"

func readUpgradePreflight(ctx context.Context, q queryer, id string) (domain.UpgradePreflight, error) {
	var result domain.UpgradePreflight
	if !domain.ValidOperation(id) {
		return result, domain.Fail(domain.Invalid, "an exact native preflight ID is required")
	}
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", upgradePreflightPrefix+id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, domain.Fail(domain.NotFound, "native upgrade preflight is not recorded")
		}
		return result, err
	}
	err := json.Unmarshal([]byte(raw), &result)
	return result, err
}

func saveUpgradePreflight(ctx context.Context, tx *sql.Tx, value domain.UpgradePreflight) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		upgradePreflightPrefix+value.ID, string(raw))
	return err
}

func (s *Store) preflightTarget(ctx context.Context, q queryer, request domain.UpgradePreflightRequest) (int64, error) {
	var instance string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='instance_id'").Scan(&instance); err != nil {
		return 0, err
	}
	if request.InstanceID != instance {
		return 0, domain.Fail(domain.Denied, "native preflight belongs to another Foundation instance")
	}
	if request.Role == "foundation" {
		if request.TargetID != instance {
			return 0, domain.Fail(domain.Denied, "Foundation preflight must name this exact service instance")
		}
		return 1, nil
	}
	target, err := activeIdentitySubject(ctx, q, request.TargetID)
	if err != nil {
		return 0, err
	}
	generation, active, err := agentOperationIdentity(ctx, q, target)
	if err != nil {
		return 0, err
	}
	if !active || generation <= 0 {
		return 0, domain.Fail(domain.Denied, "native preflight requires a current enrolled target")
	}
	if target.Founder {
		identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", target.ID)
		if err != nil {
			return 0, err
		}
		if identity.State != "active" {
			return 0, domain.Fail(domain.Denied, "worker maintenance requires the original active Founder computer binding")
		}
	}
	return generation, nil
}

func (s *Store) preflightCurrent(ctx context.Context, q queryer, record domain.UpgradePreflight) error {
	founder, err := activeIdentitySubject(ctx, q, record.FounderID)
	if err != nil {
		return err
	}
	identity, err := get[domain.AgentIdentity](ctx, q, "agent_identities", record.FounderBindingID)
	if err != nil {
		return err
	}
	if !founder.Founder || identity.AgentID != founder.ID || identity.State != "active" || identity.Generation != record.FounderGeneration {
		return domain.Fail(domain.Denied, "native preflight issuer authority or key generation changed")
	}
	generation, err := s.preflightTarget(ctx, q, record.Request)
	if err != nil {
		return err
	}
	if generation != record.TargetGeneration {
		return domain.Fail(domain.Denied, "native preflight target key generation changed")
	}
	return nil
}

func (s *Store) RequestUpgradePreflight(ctx context.Context, actor string, request domain.UpgradePreflightRequest) (domain.UpgradePreflight, error) {
	return mutate(ctx, s, actor, "upgrade.preflight", request.Command, request, func(tx *sql.Tx, founder domain.Agent) (domain.UpgradePreflight, error) {
		var result domain.UpgradePreflight
		if !founder.Founder {
			return result, domain.Fail(domain.Denied, "only authenticated Founder may request native upgrade preflight")
		}
		if err := upgrade.ValidatePreflight(request); err != nil {
			return result, err
		}
		bindingID := founder.ID
		if certificate, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok {
			bindingID = certificate.BindingID()
		}
		identity, err := get[domain.AgentIdentity](ctx, tx, "agent_identities", bindingID)
		if err != nil {
			return result, err
		}
		if identity.AgentID != founder.ID || identity.State != "active" || identity.Generation <= 0 {
			return result, domain.Fail(domain.Denied, "native preflight requires a current Founder key generation")
		}
		targetGeneration, err := s.preflightTarget(ctx, tx, request)
		if err != nil {
			return result, err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key LIKE 'upgrade-preflight:%'
AND json_extract(value,'$.founder_id')=? AND json_extract(value,'$.state')='queued'
AND json_extract(value,'$.expires_at')>?`, founder.ID, s.timestamp()).Scan(&pending); err != nil {
			return result, err
		}
		if pending >= 32 {
			return result, domain.Fail(domain.Prerequisite, "Founder already has 32 pending native preflight requests")
		}
		now := s.timestamp()
		result = domain.UpgradePreflight{ID: domain.NewID("preflight"), Request: request, FounderID: founder.ID,
			FounderBindingID: bindingID, FounderGeneration: identity.Generation, TargetGeneration: targetGeneration, State: "queued", Revision: 1,
			CreatedAt: now, ExpiresAt: now + domain.UpgradePreflightTTL}
		return result, saveUpgradePreflight(ctx, tx, result)
	})
}

func (s *Store) UpgradePreflight(ctx context.Context, actor, id string) (domain.UpgradePreflight, error) {
	var result domain.UpgradePreflight
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Close()
	founder, err := s.actor(ctx, tx, actor)
	if err != nil {
		return result, err
	}
	result, err = readUpgradePreflight(ctx, tx, id)
	if err != nil {
		return result, err
	}
	if !founder.Founder || result.FounderID != actor {
		return domain.UpgradePreflight{}, domain.Fail(domain.Denied, "native preflight evidence belongs only to its requesting Founder")
	}
	if err := s.preflightCurrent(ctx, tx, result); err != nil {
		return domain.UpgradePreflight{}, err
	}
	if result.ExpiresAt <= s.timestamp() {
		return domain.UpgradePreflight{}, domain.Fail(domain.Conflict, "native preflight expired; retain its evidence and explicitly prepare a new operation")
	}
	return result, nil
}

func (s *Store) NativeUpgradePreflights(ctx context.Context, actor string) ([]domain.UpgradePreflight, error) {
	if _, err := s.actor(ctx, s.db, actor); err != nil {
		return nil, err
	}
	if certificate, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok && certificate.DeviceID != "" {
		return nil, domain.Fail(domain.Denied, "additional Founder devices do not acquire a worker maintenance inbox")
	}
	return s.nativeUpgradePreflights(ctx, actor, "agent")
}

func (s *Store) SystemUpgradePreflights(ctx context.Context) ([]domain.UpgradePreflight, error) {
	var instance string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='instance_id'").Scan(&instance); err != nil {
		return nil, err
	}
	return s.nativeUpgradePreflights(ctx, instance, "foundation")
}

func (s *Store) nativeUpgradePreflights(ctx context.Context, target, role string) ([]domain.UpgradePreflight, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Close()
	rows, err := tx.QueryContext(ctx, `SELECT value FROM settings WHERE key LIKE 'upgrade-preflight:%'
AND json_extract(value,'$.request.target_id')=? AND json_extract(value,'$.request.role')=?
AND json_extract(value,'$.state')='queued' ORDER BY key LIMIT 32`, target, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.UpgradePreflight{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value domain.UpgradePreflight
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		if value.ExpiresAt <= s.timestamp() {
			value.Problem = domain.AsFault(domain.Fail(domain.Conflict, "native preflight expired without renewing its authority"))
		} else if err := s.preflightCurrent(ctx, tx, value); err != nil {
			switch domain.AsFault(err).Code {
			case domain.Denied, domain.Conflict, domain.NotFound:
				value.Problem = domain.AsFault(domain.Fail(domain.Denied, "native preflight authority changed before target admission"))
			default:
				return nil, err
			}
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ReportUpgradePreflight(ctx context.Context, actor string, request domain.UpgradePreflightReport) (domain.UpgradePreflight, error) {
	return mutate(ctx, s, actor, "upgrade.preflight-result:"+request.ID, request.Command, request, func(tx *sql.Tx, target domain.Agent) (domain.UpgradePreflight, error) {
		if certificate, ok := ctx.Value(identityFenceKey{}).(domain.CertificateIdentity); ok && certificate.DeviceID != "" {
			return domain.UpgradePreflight{}, domain.Fail(domain.Denied, "additional Founder devices cannot report another computer's native preflight")
		}
		return s.reportUpgradePreflight(ctx, tx, target.ID, "agent", request)
	})
}

func (s *Store) ReportSystemUpgradePreflight(ctx context.Context, request domain.UpgradePreflightReport) (domain.UpgradePreflight, error) {
	return transact(ctx, s, "native-maintenance", "upgrade.preflight-result:"+request.ID, request.OperationID, request, func(tx *sql.Tx) (domain.UpgradePreflight, error) {
		instance, err := identityInstance(ctx, tx)
		if err != nil {
			return domain.UpgradePreflight{}, err
		}
		return s.reportUpgradePreflight(ctx, tx, instance, "foundation", request)
	})
}

func (s *Store) reportUpgradePreflight(ctx context.Context, tx *sql.Tx, target, role string, request domain.UpgradePreflightReport) (domain.UpgradePreflight, error) {
	record, err := readUpgradePreflight(ctx, tx, request.ID)
	if err != nil {
		return record, err
	}
	if record.Request.TargetID != target || record.Request.Role != role {
		return record, domain.Fail(domain.Denied, "native preflight may be reported only by its exact target boundary")
	}
	if err := expect(record.Revision, request.ExpectedRevision); err != nil {
		return record, err
	}
	if record.State != "queued" {
		return record, domain.Fail(domain.Conflict, "native preflight already has a retained terminal observation")
	}
	if request.Result.Error == nil {
		if record.ExpiresAt <= s.timestamp() {
			return record, domain.Fail(domain.Denied, "expired native preflight cannot become a fresh observation")
		}
		if err := s.preflightCurrent(ctx, tx, record); err != nil {
			return record, err
		}
	}
	if err := upgrade.ValidatePreflightResult(record, request.Result, s.timestamp()); err != nil {
		return record, err
	}
	reply := request.Result
	record.State = "completed"
	if reply.Error != nil {
		record.State = "failed"
		reply.Error = domain.AsFault(domain.Fail(reply.Error.Code, upgrade.PreflightFailureMessage))
	}
	record.Result, record.CompletedAt = &reply, s.timestamp()
	record.Revision++
	return record, saveUpgradePreflight(ctx, tx, record)
}
