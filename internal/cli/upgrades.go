package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/backup"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) upgradeCommand(ctx context.Context, r *resources, args []string) (any, error) {
	return a.upgradeWith(ctx, r, args, upgrade.NewCatalog(), process.Native{})
}

func (a *app) upgradeWith(ctx context.Context, r *resources, args []string, catalog upgrade.Catalog, runner process.Runner) (any, error) {
	if len(args) == 0 {
		return nil, domain.Fail(domain.Invalid, "upgrade requires check, plan, apply or status")
	}
	if args[0] == "status" {
		fs := flag.NewFlagSet("upgrade status", flag.ContinueOnError)
		input := fs.String("input", "", "private retained native upgrade plan")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		var plan upgrade.Plan
		if *input == "" || *input == "-" {
			return nil, domain.Fail(domain.Invalid, "upgrade status requires the retained private --input PLAN")
		}
		if err := config.Read(*input, &plan); err != nil {
			return nil, err
		}
		var credential config.Credential
		if err := config.Read(r.config.CredentialFile, &credential); err != nil {
			return nil, err
		}
		if plan.Version != 1 || plan.FounderID != credential.AgentID || len(plan.Items) == 0 || len(plan.Items) > 32 {
			return nil, domain.Fail(domain.Denied, "status requires the original Founder's retained private plan; it grants no upgrade authority")
		}
		var results []domain.UpgradeNativeReply
		for _, item := range plan.Items {
			reply, err := upgrade.CallNative(ctx, runner, item.Target,
				domain.UpgradeNativeRequest{Action: "status", ID: item.Proposal.Challenge.ID})
			if err != nil {
				return nil, err
			}
			results = append(results, reply)
		}
		return map[string]any{"plan_state": plan.State, "native_results": results,
			"authority": "retained local evidence only; no live Founder authority or installation is inferred"}, nil
	}
	authority, err := api.Call[domain.UpgradeContext](ctx, r.client, "GET", "/v1/upgrades/context", nil)
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return nil, domain.Fail(domain.Prerequisite, "Foundation does not yet support Founder upgrades; install an upgrade-capable release and provision native policies once through an authorized operator")
		}
		return nil, err
	}
	fs := flag.NewFlagSet("upgrade "+args[0], flag.ContinueOnError)
	input := fs.String("input", "", "private operator targets or retained upgrade plan")
	output := ""
	if args[0] == "plan" {
		fs.StringVar(&output, "output", "", "new private immutable-source plan")
	}
	platform := ""
	if args[0] == "check" {
		fs.StringVar(&platform, "platform", runtime.GOOS+"-"+runtime.GOARCH, "native release platform")
	}
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if args[0] == "check" {
		release, err := catalog.Latest(ctx, platform)
		if err != nil {
			return nil, err
		}
		comparison, compareErr := upgrade.CompareVersion(authority.Version, release.Version)
		if compareErr != nil {
			return nil, compareErr
		}
		if comparison == 0 && authority.Commit != release.Commit {
			return nil, domain.Fail(domain.Conflict, "published release has the same VERSION but different immutable source")
		}
		results := []map[string]any{}
		if *input != "" {
			if *input == "-" {
				return nil, domain.Fail(domain.Invalid, "upgrade inventory requires a private target file, not stdin")
			}
			var targets upgrade.Targets
			if err := config.Read(*input, &targets); err != nil {
				return nil, err
			}
			if len(targets.Targets) == 0 || len(targets.Targets) > 32 {
				return nil, domain.Fail(domain.Invalid, "software inventory requires 1-32 explicitly configured operator targets")
			}
			candidates := map[string]domain.UpgradeRelease{platform: release}
			for i, target := range targets.Targets {
				candidate, ok := candidates[target.Platform]
				if !ok {
					candidate, err = catalog.Latest(ctx, target.Platform)
					if err != nil {
						return nil, err
					}
					candidates[target.Platform] = candidate
				}
				reply, err := upgrade.CallNative(ctx, runner, target,
					domain.UpgradeNativeRequest{Action: "inspect", ID: a.operation + "." + strconv.Itoa(i)})
				if err != nil {
					return nil, err
				}
				if reply.Challenge == nil || reply.Challenge.InstanceID != authority.InstanceID || reply.Challenge.TargetID != target.TargetID ||
					reply.Challenge.Role != target.Role || reply.State != "observed" {
					return nil, domain.Fail(domain.Denied, "native software inventory belongs to another configured target")
				}
				order, err := upgrade.CompareVersion(reply.Version, candidate.Version)
				if err != nil {
					return nil, err
				}
				if order == 0 && reply.Commit != candidate.Commit {
					return nil, domain.Fail(domain.Conflict, "native inventory has the same VERSION but different immutable source")
				}
				results = append(results, map[string]any{"target_id": target.TargetID, "role": target.Role,
					"version": reply.Version, "commit": reply.Commit, "latest": candidate, "upgrade_available": order < 0})
			}
		}
		return map[string]any{"latest": release, "foundation_version": authority.Version,
			"foundation_commit": authority.Commit, "foundation_upgrade_available": comparison < 0,
			"local_version": domain.Version, "local_commit": domain.Commit,
			"targets":                 results,
			"local_development_build": domain.Version == "dev" || strings.HasSuffix(domain.Version, "-dev")}, nil
	}
	if *input == "" || *input == "-" {
		return nil, domain.Fail(domain.Invalid, "upgrade planning/application requires a private --input FILE for durable exact replay")
	}
	inputPath, err := filepath.Abs(*input)
	if err != nil {
		return nil, err
	}
	founder := ""
	for _, agent := range authority.Agents {
		if agent.Founder && agent.Active && !agent.Banned {
			founder = agent.ID
		}
	}
	if args[0] == "plan" {
		if output == "" {
			return nil, domain.Fail(domain.Invalid, "upgrade plan requires --output FILE; no upgrade is automatically applied")
		}
		path, err := filepath.Abs(output)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return nil, domain.Fail(domain.Conflict, "upgrade plan output already exists or cannot be inspected; it was not overwritten")
		}
		var targets upgrade.Targets
		if err := config.Read(inputPath, &targets); err != nil {
			return nil, err
		}
		if len(targets.Targets) == 0 || len(targets.Targets) > 32 {
			return nil, domain.Fail(domain.Invalid, "one explicit rollout contains 1-32 native targets")
		}
		plan := upgrade.Plan{Version: 1, ID: domain.NewID("upgrade"), InstanceID: authority.InstanceID,
			FounderID: founder, State: "planned", RecoveryKey: path + ".agekey", Items: []upgrade.PlanItem{}}
		recipient, err := backup.Keygen(plan.RecoveryKey)
		if err != nil {
			return nil, err
		}
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
		candidates := map[string]domain.UpgradeRelease{}
		var releaseVersion, releaseCommit string
		for i, target := range targets.Targets {
			if err := upgrade.ValidateTarget(target); err != nil {
				return nil, err
			}
			if seen[target.TargetID] {
				return nil, domain.Fail(domain.Invalid, "duplicate native upgrade target")
			}
			seen[target.TargetID] = true
			id := plan.ID + "." + strconv.Itoa(i)
			reply, err := upgrade.CallNative(ctx, runner, target, domain.UpgradeNativeRequest{Action: "challenge", ID: id})
			if err != nil {
				return nil, err
			}
			if reply.Challenge == nil || reply.Challenge.TargetID != target.TargetID ||
				reply.Challenge.InstanceID != plan.InstanceID || reply.Challenge.Role != target.Role {
				return nil, domain.Fail(domain.Denied, "native helper returned a challenge for another computer or Foundation instance")
			}
			release, found := candidates[target.Platform]
			if !found {
				release, err = catalog.Latest(ctx, target.Platform)
				if err != nil {
					return nil, err
				}
				candidates[target.Platform] = release
			}
			if releaseVersion == "" {
				releaseVersion, releaseCommit = release.Version, release.Commit
			} else if release.Version != releaseVersion || release.Commit != releaseCommit {
				return nil, domain.Fail(domain.Conflict, "published release changed during planning; no mixed-source rollout was prepared")
			}
			proposal := domain.UpgradeProposal{Challenge: *reply.Challenge, Release: release,
				BackupRecipient: recipient, Reason: "Founder explicitly requested the exact current release and native target"}
			if err := upgrade.ValidateProposal(proposal); err != nil {
				return nil, err
			}
			plan.Items = append(plan.Items, upgrade.PlanItem{Target: target, Proposal: proposal})
		}
		if err := config.Write(path, plan); err != nil {
			return nil, err
		}
		return map[string]any{"plan_file": path, "recovery_key_file": plan.RecoveryKey, "plan": plan,
			"applied": false, "note": "native challenge/backup recipient prepared; no packages, services or modes changed"}, nil
	}
	if args[0] != "apply" {
		return nil, domain.Fail(domain.Invalid, "unknown upgrade command")
	}
	var plan upgrade.Plan
	if err := config.Read(inputPath, &plan); err != nil {
		return nil, err
	}
	if plan.Version != 1 || !domain.ValidOperation(plan.ID) || plan.InstanceID != authority.InstanceID ||
		plan.FounderID != founder || len(plan.Items) == 0 || len(plan.Items) > 32 {
		return nil, domain.Fail(domain.Denied, "upgrade plan does not belong to this current Founder/Foundation")
	}
	if plan.State != "planned" {
		return nil, domain.Fail(domain.Conflict, "upgrade plan was already submitted; use upgrade status after uncertainty, never rerun installation")
	}
	if _, err := secure.Read(plan.RecoveryKey, domain.MaxBody); err != nil {
		return nil, domain.Fail(domain.Prerequisite, "retained off-host recovery key is required before upgrading")
	}
	lock, err := secure.Acquire(filepath.Join(r.config.StateDir, "upgrade.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	pauseIDs := map[string]bool{founder: true}
	upgradingFoundation := false
	for _, item := range plan.Items {
		if item.Target.Role == "agent" {
			pauseIDs[item.Target.TargetID] = true
		} else {
			upgradingFoundation = true
		}
	}
	for _, agent := range authority.Agents {
		if (pauseIDs[agent.ID] || upgradingFoundation && agent.Active) && agent.Mode != "manual" {
			request := domain.ModeRequest{Command: domain.Command{OperationID: plan.ID + ".pause." + agent.ID, ExpectedRevision: agent.Revision},
				Mode: "manual", Reason: "Founder-authorized native software upgrade; retain all work/session fences and require explicit resumption"}
			if _, err := api.Call[domain.Agent](ctx, r.client, "POST", api.Path("agents", agent.ID, "mode"), request); err != nil {
				return nil, err
			}
		}
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		req := domain.UpgradeAuthorizationRequest{Command: domain.Command{OperationID: plan.ID + ".authorize." + strconv.Itoa(i)}, Proposal: item.Proposal}
		permit, err := api.Call[domain.UpgradePermit](ctx, r.client, "POST", "/v1/upgrades/authorize", req)
		if err != nil {
			return nil, err
		}
		item.Permit = &permit
	}
	plan.State = "submitted"
	if err := config.Write(inputPath, plan); err != nil {
		return nil, err
	}
	for i := len(plan.Items) - 1; i >= 0; i-- {
		item := &plan.Items[i]
		reply, err := upgrade.CallNative(ctx, runner, item.Target,
			domain.UpgradeNativeRequest{Action: "prepare", ID: item.Proposal.Challenge.ID, Permit: item.Permit})
		if err != nil {
			return nil, err
		}
		if reply.State != "prepared" {
			return nil, domain.Fail(domain.Conflict, "native target did not complete its exact cold backup and stopped-service receipt")
		}
		item.Result = &reply
		if err := config.Write(inputPath, plan); err != nil {
			return nil, err
		}
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		reply, err := upgrade.CallNative(ctx, runner, item.Target,
			domain.UpgradeNativeRequest{Action: "apply", ID: item.Proposal.Challenge.ID, Permit: item.Permit})
		if err != nil {
			return nil, err
		}
		if reply.State != "verified" {
			return nil, domain.Fail(domain.Unavailable, "native upgrade has not been verified")
		}
		item.Result = &reply
		if err := config.Write(inputPath, plan); err != nil {
			return nil, err
		}
	}
	plan.State = "verified"
	return map[string]any{"plan": plan, "automation": "paused; explicit Founder resumption is separate"}, config.Write(inputPath, plan)
}

func (a *app) upgradeHelper(ctx context.Context, args []string) (any, error) {
	a.nativeOutput = true
	fs := flag.NewFlagSet("upgrade-helper", flag.ContinueOnError)
	policyPath := fs.String("policy", "", "administrator-provisioned native updater policy")
	if err := parse(fs, args); err != nil {
		return nil, err
	}

	if *policyPath == "" {
		return nil, domain.Fail(domain.Invalid, "upgrade-helper requires --policy FILE and one bounded protobuf request on stdin")
	}
	policy, err := upgrade.ReadPolicy(*policyPath)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native upgrade request exceeds its size bound")
	}
	var request domain.UpgradeNativeRequest
	if err := message.UnmarshalPayload(data, &request); err != nil {
		return nil, err
	}
	reply, nativeErr := upgrade.NewNative(policy).Handle(ctx, request)
	if nativeErr != nil {
		reply.Error = domain.AsFault(nativeErr)
	}
	encoded, err := message.MarshalPayload(reply)
	if err != nil {
		return nil, err
	}
	_, writeErr := a.out.Write(encoded)
	return nil, errors.Join(nativeErr, writeErr)
}

func (a *app) upgradeLease(ctx context.Context, r *resources, args []string) (any, error) {
	a.nativeOutput = true
	if err := requireArgs(args, 0); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native start-lease request is oversized")
	}
	var request domain.UpgradeLeaseRequest
	if err := message.UnmarshalPayload(data, &request); err != nil {
		return nil, err
	}
	lease, leaseErr := api.Call[domain.UpgradeLease](ctx, r.client, "POST", "/v1/upgrades/lease", request)
	reply := domain.UpgradeNativeReply{State: "start-authorized", Lease: &lease}
	if leaseErr != nil {
		reply.Error, reply.Lease = domain.AsFault(leaseErr), nil
	}
	payload, err := message.MarshalPayload(reply)
	if err != nil {
		return nil, err
	}
	_, writeErr := a.out.Write(payload)
	return nil, errors.Join(leaseErr, writeErr)
}
