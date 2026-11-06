package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type nativePreflightProgress struct {
	Request domain.UpgradePreflight        `json:"request"`
	Report  *domain.UpgradePreflightReport `json:"report,omitempty"`
}

func (n *Native) ControlStep(ctx context.Context) (bool, error) {
	if _, err := os.Lstat(filepath.Join(n.Policy.StateDir, "message-inflight.json")); err == nil {
		return n.MessageStep(ctx)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	handled, err := n.PreflightStep(ctx)
	if handled || err != nil {
		return handled, err
	}
	return n.MessageStep(ctx)
}

func (n *Native) PreflightStep(ctx context.Context) (bool, error) {
	if n.Mailbox == nil {
		return false, domain.Fail(domain.Prerequisite, "native preflight inbox is not configured")
	}
	path := filepath.Join(n.Policy.StateDir, "preflight-inflight.json")
	var progress nativePreflightProgress
	if err := config.Read(path, &progress); os.IsNotExist(err) {
		inbox, err := n.Mailbox(ctx, domain.MessageWorkerCommand{Preflight: true})
		if err != nil {
			return false, err
		}
		if len(inbox.Preflights) == 0 {
			return false, nil
		}
		progress.Request = inbox.Preflights[0]
	} else if err != nil {
		return false, err
	}
	record := progress.Request
	request := record.Request
	if err := ValidatePreflight(request); err != nil {
		return true, err
	}
	if !domain.ValidOperation(record.ID) || record.State != "queued" || record.Revision != 1 ||
		record.ExpiresAt-record.CreatedAt != domain.UpgradePreflightTTL ||
		!domain.ValidOperation(record.FounderID) || !domain.ValidOperation(record.FounderBindingID) ||
		record.FounderGeneration <= 0 || record.TargetGeneration <= 0 ||
		request.InstanceID != n.Policy.InstanceID || request.TargetID != n.Policy.TargetID || request.Role != n.Policy.Role {
		return true, domain.Fail(domain.Denied, "native preflight belongs to another fixed policy or request")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	failed := func(code domain.Code) domain.UpgradeNativeReply {
		return domain.UpgradeNativeReply{State: "failed", Platform: platform,
			Error: domain.AsFault(domain.Fail(code, PreflightFailureMessage))}
	}
	if progress.Report == nil {
		var reply domain.UpgradeNativeReply
		switch {
		case record.Problem != nil:
			reply = failed(record.Problem.Code)
		case record.ExpiresAt <= n.Now().Unix():
			reply = failed(domain.Conflict)
		case request.Platform != platform:
			reply = failed(domain.Conflict)
		default:
			var err error
			reply, err = n.Handle(ctx, request.Native)
			reply.Platform = platform
			if err != nil {
				reply = failed(domain.AsFault(err).Code)
			} else if err := ValidatePreflightResult(record, reply, n.Now().Unix()); err != nil {
				reply = failed(domain.AsFault(err).Code)
			}
		}
		progress.Report = &domain.UpgradePreflightReport{
			Command: domain.Command{OperationID: "preflight." + record.ID + ".result", ExpectedRevision: record.Revision},
			ID:      record.ID, Result: reply,
		}
		if err := config.Write(path, progress); err != nil {
			return true, err
		}
	}
	result, err := n.Mailbox(ctx, domain.MessageWorkerCommand{PreflightReport: progress.Report})
	if err != nil && domain.AsFault(err).Code == domain.Denied && progress.Report.Result.Error == nil {
		progress.Report.Result = failed(domain.Denied)
		progress.Report.OperationID = "preflight." + record.ID + ".failed"
		if saveErr := config.Write(path, progress); saveErr != nil {
			return true, errors.Join(err, saveErr)
		}
		result, err = n.Mailbox(ctx, domain.MessageWorkerCommand{PreflightReport: progress.Report})
	}
	if err != nil {
		return true, err
	}
	updated := result.Preflight
	expected := "completed"
	if progress.Report.Result.Error != nil {
		expected = "failed"
	}
	if updated == nil || updated.ID != record.ID || updated.FounderID != record.FounderID || updated.FounderBindingID != record.FounderBindingID ||
		updated.FounderGeneration != record.FounderGeneration || updated.TargetGeneration != record.TargetGeneration ||
		!reflect.DeepEqual(updated.Request, record.Request) || updated.State != expected ||
		updated.CreatedAt != record.CreatedAt || updated.ExpiresAt != record.ExpiresAt ||
		updated.Revision != record.Revision+1 || !reflect.DeepEqual(updated.Result, &progress.Report.Result) {
		return true, domain.Fail(domain.Unavailable, "native preflight acknowledgement changed; retain the exact observation without repeating it")
	}
	if err := os.Remove(path); err != nil {
		return true, err
	}
	return true, nil
}
