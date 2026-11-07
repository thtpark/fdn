package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/backup"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) callUpgradeNative(ctx context.Context, client *api.Client, instance string, target upgrade.Target, native domain.UpgradeNativeRequest, runner process.Runner, remember func(domain.UpgradePreflight) error) (domain.UpgradeNativeReply, error) {
	if err := upgrade.ValidateTarget(target); err != nil {
		return domain.UpgradeNativeReply{}, err
	}
	if !target.FoundationRoute() {
		return upgrade.CallNative(ctx, runner, target, native)
	}
	request := domain.UpgradePreflightRequest{
		Command:    domain.Command{OperationID: "preflight." + domain.Hash(a.operation+"|"+native.ID+"|"+native.Action)},
		InstanceID: instance, TargetID: target.TargetID, Role: target.Role, Platform: target.Platform, Native: native,
	}
	if err := upgrade.ValidatePreflight(request); err != nil {
		return domain.UpgradeNativeReply{}, err
	}
	record, err := api.Call[domain.UpgradePreflight](ctx, client, "POST", "/v1/upgrades/preflight", request)
	if err != nil {
		return domain.UpgradeNativeReply{}, err
	}
	if !domain.ValidOperation(record.ID) || record.FounderID != client.Credential.AgentID ||
		!domain.ValidOperation(record.FounderBindingID) || record.ExpiresAt-record.CreatedAt != domain.UpgradePreflightTTL ||
		record.FounderGeneration <= 0 || record.TargetGeneration <= 0 || record.CreatedAt <= 0 ||
		!reflect.DeepEqual(record.Request, request) {
		return domain.UpgradeNativeReply{}, domain.Fail(domain.Denied, "native preflight acknowledgement belongs to another operation or Founder")
	}
	if remember != nil {
		if err := remember(record); err != nil {
			return domain.UpgradeNativeReply{}, err
		}
	}
	for {
		current, err := api.Call[domain.UpgradePreflight](ctx, client, "GET", api.Path("upgrades", "preflight", record.ID), nil)
		if err != nil {
			return domain.UpgradeNativeReply{}, err
		}
		if current.ID != record.ID || current.FounderID != record.FounderID ||
			current.FounderBindingID != record.FounderBindingID ||
			current.FounderGeneration != record.FounderGeneration || current.TargetGeneration != record.TargetGeneration ||
			current.CreatedAt != record.CreatedAt || current.ExpiresAt != record.ExpiresAt ||
			!reflect.DeepEqual(current.Request, request) {
			return domain.UpgradeNativeReply{}, domain.Fail(domain.Denied, "native preflight changed its original target or authority")
		}
		switch current.State {
		case "completed", "failed":
			if current.Result == nil || current.Revision != 2 || current.CompletedAt < current.CreatedAt ||
				current.State == "completed" && current.CompletedAt >= current.ExpiresAt {
				return domain.UpgradeNativeReply{}, domain.Fail(domain.Unavailable, "native preflight ended without an actual target observation")
			}
			if err := upgrade.ValidatePreflightResult(current, *current.Result, current.CompletedAt); err != nil {
				return domain.UpgradeNativeReply{}, err
			}
			if (current.State == "failed") != (current.Result.Error != nil) {
				return domain.UpgradeNativeReply{}, domain.Fail(domain.Unavailable, "native preflight result does not match its observed state")
			}
			if current.Result.Error != nil {
				return *current.Result, current.Result.Error
			}
			return *current.Result, nil
		case "queued":
			if current.Result != nil || current.Revision != 1 || current.CompletedAt != 0 {
				return domain.UpgradeNativeReply{}, domain.Fail(domain.Unavailable, "queued native preflight is not a completed observation")
			}
		default:
			return domain.UpgradeNativeReply{}, domain.Fail(domain.Unavailable, "native preflight has an unsupported retained state")
		}
		select {
		case <-ctx.Done():
			return domain.UpgradeNativeReply{}, domain.Fail(domain.Unavailable, "native preflight "+record.ID+" remains pending or uncertain; retain this operation and plan, no installation was requested")
		case <-time.After(time.Second):
		}
	}
}

func (a *app) planUpgradeViaFoundation(ctx context.Context, r *resources, authority domain.UpgradeContext, targets upgrade.Targets, path string, catalog upgrade.Catalog, runner process.Runner) (any, error) {
	slices.SortStableFunc(targets.Targets, func(a, b upgrade.Target) int {
		if a.Role == b.Role {
			return 0
		}
		if a.Role == "foundation" {
			return -1
		}
		return 1
	})
	seen := map[string]bool{}
	for _, target := range targets.Targets {
		if err := upgrade.ValidateTarget(target); err != nil {
			return nil, err
		}
		if seen[target.TargetID] {
			return nil, domain.Fail(domain.Invalid, "duplicate native upgrade target")
		}
		seen[target.TargetID] = true
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return nil, err
	}
	fingerprint := domain.Hash(string(raw))
	if err := secure.Dir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var plan upgrade.Plan
	if err := config.Read(path, &plan); os.IsNotExist(err) {
		plan = upgrade.Plan{Version: 2, ID: domain.NewID("upgrade"), InstanceID: authority.InstanceID,
			FounderID: r.client.Credential.AgentID, State: "planning", OperationID: a.operation,
			TargetsSHA256: fingerprint, RecoveryKey: path + ".agekey", Items: []upgrade.PlanItem{}}
		for _, target := range targets.Targets {
			plan.Items = append(plan.Items, upgrade.PlanItem{Target: target})
		}
		if err := config.Write(path, plan); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if plan.Version != 2 || plan.State != "planning" || plan.OperationID != a.operation ||
		plan.InstanceID != authority.InstanceID || plan.FounderID != r.client.Credential.AgentID ||
		plan.TargetsSHA256 != fingerprint || len(plan.Items) != len(targets.Targets) {
		return nil, domain.Fail(domain.Conflict, "retained upgrade planning belongs to another exact operation, target set or state; it was not overwritten")
	}
	for i := range plan.Items {
		if plan.Items[i].Target != targets.Targets[i] {
			return nil, domain.Fail(domain.Conflict, "retained upgrade target or route changed during preflight")
		}
	}
	if plan.BackupRecipient == "" {
		recipient, err := backup.PublicRecipient(plan.RecoveryKey)
		if os.IsNotExist(err) {
			recipient, err = backup.Keygen(plan.RecoveryKey)
		}
		if err != nil {
			return nil, err
		}
		plan.BackupRecipient = recipient
		if err := config.Write(path, plan); err != nil {
			return nil, err
		}
	} else {
		recipient, err := backup.PublicRecipient(plan.RecoveryKey)
		if err != nil {
			return nil, err
		}
		if recipient != plan.BackupRecipient {
			return nil, domain.Fail(domain.Denied, "retained off-host recovery key does not match this upgrade plan")
		}
	}
	candidates := map[string]domain.UpgradeRelease{}
	var version, commit string
	for _, item := range plan.Items {
		if item.Proposal.Release.Version != "" {
			version, commit = item.Proposal.Release.Version, item.Proposal.Release.Commit
			candidates[item.Target.Platform] = item.Proposal.Release
		}
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		native := domain.UpgradeNativeRequest{Action: "challenge", ID: plan.ID + "." + strconv.Itoa(i)}
		reply, err := a.callUpgradeNative(ctx, r.client, plan.InstanceID, item.Target, native, runner, func(record domain.UpgradePreflight) error {
			if item.PreflightID != "" && item.PreflightID != record.ID {
				return domain.Fail(domain.Denied, "retained planning operation acknowledged another native preflight")
			}
			item.PreflightID = record.ID
			return config.Write(path, plan)
		})
		if err != nil {
			return nil, err
		}
		if reply.Challenge == nil || reply.Challenge.ID != native.ID ||
			reply.Challenge.TargetID != item.Target.TargetID || reply.Challenge.Role != item.Target.Role ||
			reply.Challenge.InstanceID != plan.InstanceID {
			return nil, domain.Fail(domain.Denied, "native preflight did not prove this plan's exact target")
		}
		release, found := candidates[item.Target.Platform]
		if !found {
			if version == "" {
				release, err = catalog.Latest(ctx, item.Target.Platform)
			} else {
				release, err = catalog.Published(ctx, version, item.Target.Platform)
			}
			if err != nil {
				return nil, err
			}
			candidates[item.Target.Platform] = release
		}
		if version == "" {
			version, commit = release.Version, release.Commit
		}
		if release.Version != version || release.Commit != commit {
			return nil, domain.Fail(domain.Conflict, "published release source changed during planning; no mixed-source rollout was prepared")
		}
		item.Proposal = domain.UpgradeProposal{Challenge: *reply.Challenge, Release: release,
			BackupRecipient: plan.BackupRecipient, Reason: "Founder explicitly requested the exact current release and native target"}
		if err := upgrade.ValidateProposal(item.Proposal); err != nil {
			return nil, err
		}
		if err := config.Write(path, plan); err != nil {
			return nil, err
		}
	}
	plan.State = "planned"
	return map[string]any{"plan_file": path, "recovery_key_file": plan.RecoveryKey, "plan": plan, "applied": false,
		"note": "target-owned native preflight observed; no packages, services or modes changed"}, config.Write(path, plan)
}
