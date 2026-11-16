package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/upgrade"
)

type softwareReceiver interface {
	Handle(context.Context, domain.SoftwareNativeRequest) (domain.SoftwareNativeReply, error)
	PendingResults() ([]domain.SoftwareResult, error)
	AcknowledgeResult(string, string) error
}

type softwareChannel func(context.Context, domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error)

func (a *app) softwareRuntime(ctx context.Context, policyPath, updaterPath, role string) (softwareReceiver, softwareChannel, string, func(), error) {
	if updaterPath != "" {
		return a.foundationSoftwareRuntime(ctx, updaterPath, role)
	}
	policy, err := software.ReadPolicy(policyPath)
	if err != nil {
		return nil, nil, "", nil, err
	}
	var channel softwareChannel
	cleanup := func() {}
	if role == "foundation" {
		client, err := a.rootSoftwareClient()
		if err != nil {
			return nil, nil, "", nil, err
		}
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return softwareMailbox(ctx, client, true, q)
		}
		cleanup = client.Close
	} else if os.Geteuid() == 0 && policy.UID != 0 {
		if runtime.GOOS != "linux" || !policy.AllowPrivileged {
			return nil, nil, "", nil, domain.Fail(domain.Denied, "privileged software receiver requires its exact administrator-provisioned Linux policy")
		}
		executable, err := os.Executable()
		if err != nil {
			return nil, nil, "", nil, err
		}
		binary := filepath.Join(filepath.Dir(executable), "agent-cli")
		if err := upgrade.CheckProtectedPath(binary, uint32(policy.UID)); err != nil {
			return nil, nil, "", nil, err
		}
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return privilegedSoftwareMailbox(ctx, policy.User, binary, a.configFile, q)
		}
	} else {
		res, err := a.load(ctx, true)
		if err != nil {
			return nil, nil, "", nil, err
		}
		if res.client.Credential.AgentID != policy.TargetID {
			_ = res.close()
			return nil, nil, "", nil, domain.Fail(domain.Denied, "software policy target is not this computer's enrolled actor")
		}
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return softwareMailbox(ctx, res.client, false, q)
		}
		cleanup = func() { _ = res.close() }
	}
	native := software.NewNative(policy)
	native.Fetch = func(ctx context.Context, id, path string) error { return fetchSoftwareArtifact(ctx, channel, id, path) }
	native.Lease = func(ctx context.Context, q domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		reply, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "lease", Lease: &q})
		if err != nil {
			return domain.UpgradeLease{}, err
		}
		if reply.Lease == nil {
			return domain.UpgradeLease{}, domain.Fail(domain.Unavailable, "native software lease response is missing")
		}
		return *reply.Lease, nil
	}
	return native, channel, policy.StateDir, cleanup, nil
}

func fetchSoftwareArtifact(ctx context.Context, channel softwareChannel, id, path string) error {
	reply, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "artifact", ArtifactID: id})
	if err != nil {
		return err
	}
	if reply.Artifact == nil || reply.Artifact.ID != id || reply.Artifact.Size < 1 || reply.Artifact.Size > domain.MaxSoftwareBundleBytes {
		return domain.Fail(domain.Invalid, "native software artifact metadata is incomplete or oversized")
	}
	artifact := *reply.Artifact
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	for offset := int64(0); offset < artifact.Size; {
		reply, callErr := channel(ctx, domain.SoftwareMailboxRequest{Action: "chunk", ArtifactID: id, Offset: offset})
		if callErr != nil {
			return errors.Join(callErr, file.Close())
		}
		chunk := reply.Chunk
		if chunk == nil || chunk.FileID != id || chunk.Offset != offset || int64(len(chunk.Data)) != min(int64(domain.FileChunkSize), artifact.Size-offset) || domain.Hash(string(chunk.Data)) != chunk.SHA256 {
			return errors.Join(domain.Fail(domain.Prerequisite, "native artifact chunk failed identity/size/hash verification"), file.Close())
		}
		if _, err := io.MultiWriter(file, hash).Write(chunk.Data); err != nil {
			return errors.Join(err, file.Close())
		}
		offset += int64(len(chunk.Data))
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errors.Join(domain.Fail(domain.Prerequisite, "native artifact complete hash mismatch"), file.Close())
	}
	return errors.Join(file.Sync(), file.Close())
}

func (a *app) softwareNativeCommand(ctx context.Context, args []string, role string, worker bool) (any, error) {
	flags := flag.NewFlagSet("software native", flag.ContinueOnError)
	policy := flags.String("policy", "", "protected general-product native policy")
	updater := flags.String("updater-policy", "", "explicit Foundation/Agent paired updater policy")
	once := flags.Bool("once", false, "process one native maintenance item")
	if err := parse(flags, args); err != nil {
		return nil, err
	}
	if (*policy == "") == (*updater == "") {
		return nil, domain.Fail(domain.Invalid, "select exactly one --policy or --updater-policy")
	}
	receiver, channel, state, cleanup, err := a.softwareRuntime(ctx, *policy, *updater, role)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if !worker {
		a.nativeOutput = true
		data, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
		if err != nil || len(data) > domain.MaxBody {
			return nil, domain.Fail(domain.Invalid, "native software input is oversized")
		}
		var request domain.SoftwareNativeRequest
		if err := message.UnmarshalPayload(data, &request); err != nil {
			return nil, err
		}
		reply, nativeErr := receiver.Handle(ctx, request)
		if nativeErr != nil {
			reply.Error = domain.AsFault(nativeErr)
		}
		data, err = message.MarshalPayload(reply)
		if err != nil {
			return nil, err
		}
		_, writeErr := a.out.Write(data)
		return nil, errors.Join(nativeErr, writeErr)
	}
	if err := secure.Dir(state); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if strings.Contains(string(data), "fdn-worker-") || strings.Contains(string(data), "fdn-identity-") ||
			strings.Contains(string(data), "fdn-foundation.service") || strings.Contains(string(data), "fdn-accounts.service") {
			return nil, domain.Fail(domain.Denied, "software receiver must be supervised outside the services it stops")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(state, "software-worker.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		handled, err := softwareWorkerStep(ctx, receiver, channel, state)
		if *once {
			return map[string]bool{"request_processed": handled}, err
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, writeErr := fmt.Fprintln(a.diagnostic, domain.AsFault(err)); writeErr != nil {
				return nil, errors.Join(err, writeErr)
			}
		}
		delay := 30 * time.Second
		if handled && err == nil {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func softwareWorkerStep(ctx context.Context, receiver softwareReceiver, channel softwareChannel, state string) (bool, error) {
	results, err := receiver.PendingResults()
	if err != nil {
		return false, err
	}
	for _, result := range results {
		report := domain.SoftwareResultRequest{Command: domain.Command{OperationID: "op_" + domain.Hash(result.PermitID+":"+result.PermitSHA256+":native-result")}, Result: result}
		if _, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "result", Result: &report}); err != nil {
			return true, err
		}
		return true, receiver.AcknowledgeResult(result.PermitID, result.PermitSHA256)
	}
	reply, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "preflights"})
	if err != nil {
		return false, err
	}
	for _, control := range reply.Preflights {
		path := filepath.Join(state, "preflight-"+control.ID+".json")
		var saved domain.SoftwarePreflightReport
		err := config.Read(path, &saved)
		if errors.Is(err, os.ErrNotExist) {
			observation, nativeErr := receiver.Handle(ctx, control.Request.Native)
			if nativeErr != nil {
				observation.Error = domain.AsFault(nativeErr)
			}
			saved = domain.SoftwarePreflightReport{Command: domain.Command{OperationID: "op_" + domain.Hash(control.ID+":result")}, ID: control.ID, Result: observation}
			if err := config.Write(path, saved); err != nil {
				return true, err
			}
		} else if err != nil {
			return true, err
		}
		_, err = channel(ctx, domain.SoftwareMailboxRequest{Action: "preflight-result", PreflightReport: &saved})
		return true, err
	}
	reply, err = channel(ctx, domain.SoftwareMailboxRequest{Action: "inbox"})
	if err != nil {
		return false, err
	}
	for _, deployment := range reply.Deployments {
		permit := deployment.Permit
		reply, nativeErr := receiver.Handle(ctx, domain.SoftwareNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, ProductID: permit.Proposal.Challenge.ProductID, Permit: &permit})
		if nativeErr != nil {
			return true, nativeErr
		}
		if reply.State != "verified" {
			_, nativeErr = receiver.Handle(ctx, domain.SoftwareNativeRequest{Action: "apply", ID: permit.Proposal.Challenge.ID, ProductID: permit.Proposal.Challenge.ProductID, Permit: &permit})
		}
		return true, nativeErr
	}
	return false, nil
}
