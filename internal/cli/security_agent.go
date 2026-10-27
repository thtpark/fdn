package cli

import (
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func (a *app) connect(ctx context.Context, args []string) (any, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	c, err := config.LoadWorker(a.configFile)
	if err != nil {
		return nil, err
	}
	client, err := api.NewLocalClient(c)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var status api.LocalStatus
	if err := client.Request(ctx, "GET", "/local/status", nil, &status); err != nil {
		return nil, err
	}
	if !status.Connected {
		return nil, domain.Fail(domain.Prerequisite, "the separate identity service must complete Founder-approved activation and owner admission first")
	}
	me, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	if me.ID == "" || me.ID != status.Identity.AgentID || me.ID != status.Agent.ID {
		return nil, domain.Fail(domain.Denied, "local identity service returned inconsistent identity")
	}
	var old config.Credential
	if err := config.Read(c.CredentialFile, &old); err == nil {
		if old.AgentID != me.ID {
			return nil, domain.Fail(domain.Denied, "worker state belongs to a different identity; never copy credentials")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	state, err := local.Open(ctx, c.StateDir)
	if err != nil {
		return nil, err
	}
	defer state.Close()
	if err := state.BindIdentity(ctx, me.ID); err != nil {
		return nil, err
	}
	if me.Mode != "ai" && me.Mode != "manual" {
		return nil, domain.Fail(domain.Prerequisite, "authoritative agent mode is missing")
	}
	if err := config.Write(c.CredentialFile, config.Credential{AgentID: me.ID}); err != nil {
		return nil, err
	}
	c.Mode = me.Mode
	if err := config.Write(a.configFile, c); err != nil {
		return nil, err
	}
	return me, nil
}

func (a *app) identityCommand(ctx context.Context, r *resources, args []string) (any, error) {
	if args[0] == "identity" {
		if err := requireArgs(args, 1); err != nil {
			return nil, err
		}
		return api.Call[domain.AgentIdentity](ctx, r.client, "GET", "/v1/identity/status", nil)
	}
	kind := args[0]
	tail := args[1:]
	target := ""
	if kind == "recover" {
		var err error
		target, tail, err = idArgs(tail)
		if err != nil {
			return nil, err
		}
	}
	fs := flag.NewFlagSet("agent "+kind, flag.ContinueOnError)
	input := fs.String("input", "", "target's signed public identity request")
	output := fs.String("output", "", "public pinned connection profile")
	reason := fs.String("reason", "", "intentional identity recovery reason")
	if err := parse(fs, tail); err != nil {
		return nil, err
	}
	if *input == "" {
		return nil, domain.Fail(domain.Invalid, "--input with the target's public key request is required")
	}
	var public domain.PublicIdentity
	if err := inputFile(*input, a.in, &public); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	var err error
	if kind == "register" {
		req := domain.RegisterIdentityRequest{Identity: public}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		raw, err = a.write(ctx, r, "/v1/identity/register", req, "")
	} else {
		req := domain.RecoverIdentityRequest{Reason: *reason, Identity: public}
		if err := a.metadata(&req.Command); err != nil {
			return nil, err
		}
		raw, err = a.write(ctx, r, api.Path("identity", target, "recover"), req, api.Path("agents", target))
	}
	if err != nil {
		return nil, err
	}
	var identity domain.AgentIdentity
	if err := json.Unmarshal(raw, &identity); err != nil {
		return nil, err
	}
	if *output != "" {
		profile, err := api.Call[config.ConnectionProfile](ctx, r.client, "GET", "/local/profile", nil)
		if err != nil {
			return nil, err
		}
		profile.Identity = identity
		profile.Identity.OwnerKey = ""
		profile.Identity.OwnerGeneration = 0
		var previous config.ConnectionProfile
		if err := config.Read(*output, &previous); err == nil {
			if previous.Identity.AgentID != identity.AgentID || previous.Identity.Generation != identity.Generation || previous.Identity.SSHFingerprint != identity.SSHFingerprint {
				return nil, domain.Fail(domain.Conflict, "connection profile already belongs to another identity or key generation")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if err := config.Write(*output, profile); err != nil {
			return nil, err
		}
	}
	return identity, nil
}
