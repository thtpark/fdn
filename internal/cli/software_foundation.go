package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) foundationSoftwareRuntime(ctx context.Context, path, role string) (softwareReceiver, softwareChannel, string, func(), error) {
	policy, err := upgrade.ReadPolicy(path)
	if err != nil {
		return nil, nil, "", nil, err
	}
	if !policy.EnableInternalSoftware || (role == "foundation") != (policy.Role == "foundation") {
		return nil, nil, "", nil, domain.Fail(domain.Denied, "internal Foundation delivery requires its explicit matching existing native updater role")
	}
	var channel softwareChannel
	cleanup := func() {}
	if policy.Role == "foundation" {
		client, err := a.rootSoftwareClient()
		if err != nil {
			return nil, nil, "", nil, err
		}
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return softwareMailbox(ctx, client, true, q)
		}
		cleanup = client.Close
	} else if os.Geteuid() == 0 {
		binary := filepath.Join(policy.BinaryDir, "agent-cli")
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return privilegedSoftwareMailbox(ctx, policy.User, binary, policy.ConfigFile, q)
		}
	} else {
		res, err := a.load(ctx, true)
		if err != nil {
			return nil, nil, "", nil, err
		}
		if res.client.Credential.AgentID != policy.TargetID {
			_ = res.close()
			return nil, nil, "", nil, domain.Fail(domain.Denied, "internal updater does not match this actor's own enrolled computer")
		}
		channel = func(ctx context.Context, q domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
			return softwareMailbox(ctx, res.client, false, q)
		}
		cleanup = func() { _ = res.close() }
	}
	native := upgrade.NewNative(policy)
	bridge := &upgrade.SoftwareBridge{Native: native, Trust: upgrade.SoftwareBridgeTrust{
		VerifyPermit: software.VerifyPermit, VerifyRelease: software.VerifyRelease, Extract: software.ExtractBundle,
		Fetch: func(ctx context.Context, id, path string) error { return fetchSoftwareArtifact(ctx, channel, id, path) },
		Lease: func(ctx context.Context, q domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
			reply, err := channel(ctx, domain.SoftwareMailboxRequest{Action: "lease", Lease: &q})
			if err != nil {
				return domain.UpgradeLease{}, err
			}
			if reply.Lease == nil {
				return domain.UpgradeLease{}, errors.New("software start proof is missing")
			}
			return *reply.Lease, nil
		}}}
	return bridge, channel, policy.StateDir, cleanup, nil
}
