package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strconv"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/backup"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type softwarePlanTarget struct {
	TargetID  string `json:"target_id"`
	BindingID string `json:"binding_id"`
}

type softwarePlanInput struct {
	ReleaseID string               `json:"release_id"`
	Targets   []softwarePlanTarget `json:"targets"`
	Reason    string               `json:"reason"`
}

type softwarePlan struct {
	FormatVersion   int                                   `json:"format_version"`
	OperationID     string                                `json:"operation_id"`
	Input           softwarePlanInput                     `json:"input"`
	InputSHA256     string                                `json:"input_sha256"`
	ControlIDs      []string                              `json:"control_ids"`
	Authorizations  []domain.SoftwareAuthorizationRequest `json:"authorizations"`
	DeploymentIDs   []string                              `json:"deployment_ids"`
	BatchID         string                                `json:"batch_id"`
	PublicRecipient string                                `json:"public_recipient"`
	State           string                                `json:"state"`
}

func (a *app) softwarePlanCommand(ctx context.Context, r *resources, leaf string, args []string) (any, error) {
	fs := flag.NewFlagSet("software "+leaf, flag.ContinueOnError)
	input := fs.String("input", "", "private targets file, or retained plan for apply/status")
	output := ""
	if leaf == "plan" {
		fs.StringVar(&output, "output", "", "private durable plan; age key retained beside it")
	}
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *input == "" || (leaf == "plan" && output == "") {
		return nil, domain.Fail(domain.Invalid, "software plan requires --input TARGETS --output PLAN; apply/status require --input PLAN")
	}
	if leaf == "plan" {
		return a.prepareSoftwarePlan(ctx, r, *input, output)
	}
	var plan softwarePlan
	if err := config.Read(*input, &plan); err != nil {
		return nil, err
	}
	if plan.FormatVersion != 1 || !domain.ValidOperation(plan.OperationID) ||
		len(plan.ControlIDs) == 0 || len(plan.ControlIDs) > 32 || len(plan.DeploymentIDs) != len(plan.ControlIDs) {
		return nil, domain.Fail(domain.Invalid, "unsupported or incomplete retained software plan")
	}
	if a.explicitOperation && a.operation != plan.OperationID {
		return nil, domain.Fail(domain.Conflict, "software plan must reuse its original operation")
	}
	a.operation = plan.OperationID
	if leaf == "status" {
		return a.readSoftwarePlan(ctx, r, plan)
	}
	if plan.State != "planned" && plan.State != "queued" {
		return nil, domain.Fail(domain.Prerequisite, "native software planning is incomplete; status does not dispatch new controls")
	}
	pause := domain.SoftwareMaintenanceRequest{Command: domain.Command{OperationID: plan.OperationID + ".pause"},
		ID: plan.BatchID, PreflightIDs: plan.ControlIDs, Reason: plan.Input.Reason}
	if _, err := api.Call[domain.SoftwareMaintenance](ctx, r.client, "POST", "/v1/software-maintenance", pause); err != nil {
		return nil, err
	}
	for i := range plan.Authorizations {
		if plan.DeploymentIDs[i] != "" {
			deployment, err := api.Call[domain.SoftwareDeployment](ctx, r.client, "GET", api.Path("software-deployments", plan.DeploymentIDs[i]), nil)
			if err != nil {
				return nil, err
			}
			if deployment.State != "verified" {
				break
			}
			continue
		}
		req := plan.Authorizations[i]
		req.MaintenanceID = plan.BatchID
		deployment, err := api.Call[domain.SoftwareDeployment](ctx, r.client, "POST", "/v1/software-deployments", req)
		if err != nil {
			return nil, err
		}
		plan.DeploymentIDs[i], plan.State = deployment.ID, "queued"
		if err := config.Write(*input, plan); err != nil {
			return nil, err
		}
		// A native result, not submission, admits the next dependent target.
		break
	}
	return a.readSoftwarePlan(ctx, r, plan)
}

func (a *app) prepareSoftwarePlan(ctx context.Context, r *resources, input, output string) (any, error) {
	var request softwarePlanInput
	if err := inputFile(input, a.in, &request); err != nil {
		return nil, err
	}
	if request.ReleaseID == "" || len(request.Targets) == 0 || len(request.Targets) > 32 {
		return nil, domain.Fail(domain.Invalid, "software planning requires one exact internal release and 1-32 targets")
	}
	if err := domain.CheckText(request.Reason, "software planning reason", 2048, true); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	fingerprint := domain.Hash(string(raw))
	var plan softwarePlan
	err = config.Read(output, &plan)
	if err == nil {
		if plan.FormatVersion != 1 || plan.InputSHA256 != fingerprint || (a.explicitOperation && a.operation != plan.OperationID) {
			return nil, domain.Fail(domain.Conflict, "retained plan belongs to different targets/source/operation; it was not replaced")
		}
		a.operation = plan.OperationID
	} else if errors.Is(err, os.ErrNotExist) {
		recipient, err := backup.Keygen(output + ".agekey")
		if err != nil {
			return nil, err
		}
		plan = softwarePlan{FormatVersion: 1, OperationID: a.operation, Input: request, InputSHA256: fingerprint,
			ControlIDs: make([]string, len(request.Targets)), Authorizations: make([]domain.SoftwareAuthorizationRequest, len(request.Targets)),
			DeploymentIDs: make([]string, len(request.Targets)), PublicRecipient: recipient,
			BatchID: "software_batch_" + domain.Hash(a.operation)[:32], State: "pending"}
		if err := config.Write(output, plan); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	release, err := api.Call[domain.SoftwareRelease](ctx, r.client, "GET", api.Path("software-releases", request.ReleaseID), nil)
	if err != nil {
		return nil, err
	}
	if release.Revoked {
		return nil, domain.Fail(domain.Prerequisite, "selected internal release is revoked")
	}
	ready := true
	for i, target := range request.Targets {
		if plan.ControlIDs[i] == "" {
			req := domain.SoftwarePreflightRequest{Command: domain.Command{OperationID: plan.OperationID + ".inspect." + strconv.Itoa(i)},
				TargetID: target.TargetID, BindingID: target.BindingID, ProductID: release.Manifest.ProductID,
				Native: domain.SoftwareNativeRequest{Action: "challenge", ID: plan.OperationID + ".native." + strconv.Itoa(i), ProductID: release.Manifest.ProductID}}
			control, err := api.Call[domain.SoftwarePreflight](ctx, r.client, "POST", "/v1/software-preflights", req)
			if err != nil {
				return nil, err
			}
			plan.ControlIDs[i] = control.ID
			if err := config.Write(output, plan); err != nil {
				return nil, err
			}
		}
		control, err := api.Call[domain.SoftwarePreflight](ctx, r.client, "GET", api.Path("software-preflights", plan.ControlIDs[i]), nil)
		if err != nil {
			return nil, err
		}
		if control.State != "observed" || control.Result == nil || control.Result.Challenge == nil {
			ready = false
			continue
		}
		plan.Authorizations[i] = domain.SoftwareAuthorizationRequest{Command: domain.Command{OperationID: plan.OperationID + ".apply." + strconv.Itoa(i)},
			PreflightID: control.ID, MaintenanceID: plan.BatchID,
			Proposal: domain.SoftwareProposal{Challenge: *control.Result.Challenge, Release: release,
				BackupRecipient: plan.PublicRecipient, Reason: request.Reason}}
	}
	if ready {
		plan.State = "planned"
	}
	if err := config.Write(output, plan); err != nil {
		return nil, err
	}
	return a.readSoftwarePlan(ctx, r, plan)
}

func (a *app) readSoftwarePlan(ctx context.Context, r *resources, plan softwarePlan) (any, error) {
	controls := make([]domain.SoftwarePreflight, 0, len(plan.ControlIDs))
	deployments := make([]domain.SoftwareDeployment, 0, len(plan.DeploymentIDs))
	for _, id := range plan.ControlIDs {
		if id == "" {
			continue
		}
		control, err := api.Call[domain.SoftwarePreflight](ctx, r.client, "GET", api.Path("software-preflights", id), nil)
		if err != nil {
			return nil, err
		}
		controls = append(controls, control)
	}
	for _, id := range plan.DeploymentIDs {
		if id == "" {
			continue
		}
		deployment, err := api.Call[domain.SoftwareDeployment](ctx, r.client, "GET", api.Path("software-deployments", id), nil)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	return map[string]any{"state": plan.State, "release_id": plan.Input.ReleaseID, "controls": controls, "deployments": deployments,
		"maintenance_id": plan.BatchID, "installation_verified": len(deployments) == len(plan.ControlIDs) && allSoftwareVerified(deployments)}, nil
}

func allSoftwareVerified(deployments []domain.SoftwareDeployment) bool {
	for _, deployment := range deployments {
		if deployment.State != "verified" {
			return false
		}
	}
	return len(deployments) != 0
}
