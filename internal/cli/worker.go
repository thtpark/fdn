package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/transport"
	"github.com/thtpark/fdn/internal/work"
)

type resources struct {
	config config.Worker
	state  *local.State
	client *api.Client
	tunnel *transport.Tunnel
}

func (r *resources) close() error {
	if r.client != nil {
		r.client.Close()
	}
	var tunnelErr error
	if r.tunnel != nil {
		tunnelErr = r.tunnel.Close()
	}
	return errors.Join(r.state.Close(), tunnelErr)
}

func (a *app) load(ctx context.Context, network bool) (*resources, error) {
	c, err := config.LoadWorker(a.configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, domain.Fail(domain.Prerequisite, "private worker configuration is missing; run config init with an explicit state path")
		}
		return nil, err
	}
	state, err := local.Open(ctx, c.StateDir)
	if err != nil {
		return nil, err
	}
	r := &resources{config: c, state: state}
	if !network {
		return r, nil
	}
	var credential config.Credential
	if err := config.Read(c.CredentialFile, &credential); err != nil {
		state.Close()
		if os.IsNotExist(err) {
			return nil, domain.Fail(domain.Prerequisite, "enroll this worker with connect first")
		}
		return nil, err
	}
	if credential.AgentID == "" || len(credential.Token) != 43 {
		state.Close()
		return nil, domain.Fail(domain.Prerequisite, "enrollment is incomplete; resume connect with its original invitation")
	}
	client, err := api.NewClient(c.URL, c.CAFile, c.ServerName, credential)
	if err != nil {
		state.Close()
		return nil, err
	}
	r.client = client
	if c.SSH != nil {
		if err := client.Request(ctx, "GET", "/healthz", nil, new(any)); err != nil {
			tunnel, startErr := transport.Start(ctx, *c.SSH, process.Native{})
			if startErr != nil {
				r.close()
				return nil, startErr
			}
			r.tunnel = tunnel
			if err := client.Request(ctx, "GET", "/healthz", nil, new(any)); err != nil {
				r.close()
				return nil, err
			}
		}
	}
	if err := state.BindIdentity(ctx, credential.AgentID); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (a *app) engine(r *resources) *work.Engine {
	e := work.New(r.config, a.configFile, r.client, r.state, provider.New(r.config))
	e.Log = slog.New(slog.NewJSONHandler(a.diagnostic, nil))
	return e
}

func (a *app) service(ctx context.Context, args []string) (value any, err error) {
	if len(args) != 1 {
		return nil, domain.Fail(domain.Invalid, "agent-service accepts serve or status")
	}
	if args[0] == "status" {
		r, err := a.load(ctx, false)
		if err != nil {
			return nil, err
		}
		defer r.close()
		return a.localStatus(ctx, r)
	}
	if args[0] != "serve" {
		return nil, domain.Fail(domain.Invalid, "unknown agent-service command")
	}
	c, err := config.LoadWorker(a.configFile)
	if err != nil {
		return nil, err
	}
	var credential config.Credential
	if err := config.Read(c.CredentialFile, &credential); err != nil {
		return nil, err
	}
	if credential.AgentID == "" || len(credential.Token) != 43 {
		return nil, domain.Fail(domain.Prerequisite, "complete enrollment before starting Agent Service")
	}
	state, err := local.Open(ctx, c.StateDir)
	if err != nil {
		return nil, err
	}
	defer state.Close()
	if err := state.BindIdentity(ctx, credential.AgentID); err != nil {
		return nil, err
	}
	client, err := api.NewClient(c.URL, c.CAFile, c.ServerName, credential)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	engine := work.New(c, a.configFile, client, state, provider.New(c))
	engine.Log = slog.New(slog.NewJSONHandler(a.diagnostic, nil))
	sessions := &remote.Manager{Client: client, State: state, Config: c, Opener: remote.Native{}, Log: engine.Log}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tunnelDone chan error
	if c.SSH != nil {
		tunnelDone = make(chan error, 1)
		go func() {
			tunnelDone <- transport.Maintain(ctx, *c.SSH, process.Native{}, func(problem error) { engine.Log.Warn("SSH tunnel reconnecting", "code", domain.AsFault(problem).Code) })
		}()
		defer func() { cancel(); err = errors.Join(err, <-tunnelDone) }()
	}
	a.emit(map[string]any{"state": "starting", "agent_id": credential.AgentID, "mode": c.Mode}, nil)
	err = engine.Serve(ctx, sessions)
	return map[string]string{"state": "stopped"}, err
}

func (a *app) localStatus(ctx context.Context, r *resources) (any, error) {
	lock, err := secure.Acquire(filepath.Join(r.config.StateDir, "daemon.lock"))
	running := false
	if err != nil {
		if domain.AsFault(err).Code != domain.Conflict {
			return nil, err
		}
		running = true
	} else {
		if err := lock.Close(); err != nil {
			return nil, err
		}
	}
	status, err := a.engine(r).Status(ctx)
	if err != nil {
		return nil, err
	}
	journals, err := r.state.Journals(ctx, true)
	if err != nil {
		return nil, err
	}
	type pending struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		State   string `json:"state"`
		TaskID  string `json:"task_id,omitempty"`
		PID     int    `json:"pid,omitempty"`
		Problem string `json:"problem,omitempty"`
	}
	items := []pending{}
	for _, j := range journals {
		items = append(items, pending{j.ID, j.Kind, j.State, j.TaskID, j.PID, j.Problem})
	}
	var fence remote.Fence
	if _, err := r.state.Get(ctx, "session-fence", &fence); err != nil {
		return nil, err
	}
	return map[string]any{"daemon_running": running, "work": status, "pending_invocations": items, "session_fence": fence}, nil
}

type cliProposal struct {
	Path       string          `json:"path"`
	IntentHash string          `json:"intent_hash"`
	Body       json.RawMessage `json:"body"`
	Result     json.RawMessage `json:"result,omitempty"`
}

func (a *app) write(ctx context.Context, r *resources, path string, input any, revisionSource string) (json.RawMessage, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	intent := domain.Hash(string(data))
	var command domain.Command
	if err := json.Unmarshal(data, &command); err != nil {
		return nil, err
	}
	key := "cli-proposal:" + command.OperationID
	var p cliProposal
	found, err := r.state.Get(ctx, key, &p)
	if err != nil {
		return nil, err
	}
	if found {
		if p.Path != path || p.IntentHash != intent {
			return nil, domain.Fail(domain.Conflict, "operation ID is bound to a different exact local proposal")
		}
	} else {
		if command.ExpectedRevision == 0 && revisionSource != "" {
			var current struct {
				Revision int64 `json:"revision"`
			}
			if err := r.client.Request(ctx, "GET", revisionSource, nil, &current); err != nil {
				return nil, err
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				return nil, err
			}
			fields["expected_revision"], err = json.Marshal(current.Revision)
			if err != nil {
				return nil, err
			}
			data, err = json.Marshal(fields)
			if err != nil {
				return nil, err
			}
		}
		p = cliProposal{Path: path, IntentHash: intent, Body: data}
		if err := r.state.Put(ctx, key, p); err != nil {
			return nil, err
		}
	}
	result, err := api.Call[json.RawMessage](ctx, r.client, "POST", path, p.Body)
	if err != nil {
		return nil, err
	}
	p.Result = result
	if err := r.state.Put(ctx, key, p); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *app) initWorker(args []string) (any, error) {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	state := fs.String("state", "", "private worker state")
	url := fs.String("url", "https://localhost:8443", "Foundation TLS URL")
	ca := fs.String("ca-file", "", "trusted public CA file")
	providerName := fs.String("provider", "codex", "preferred provider")
	workspace := fs.String("workspace-root", "", "owned workspace parent")
	allow := fs.Bool("allow-workspace-tools", false, "explicitly authorize tools in generated owned workspaces")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(a.configFile); err == nil {
		return nil, domain.Fail(domain.Conflict, "configuration already exists; never reset an enrolled worker")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if *state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		*state = filepath.Join(home, ".local", "share", "fdn")
	}
	root, err := filepath.Abs(*state)
	if err != nil {
		return nil, err
	}
	c := config.DefaultWorker(root)
	c.URL, c.Provider, c.AllowWorkspaceTools = *url, *providerName, *allow
	if *ca != "" {
		c.CAFile, err = filepath.Abs(*ca)
		if err != nil {
			return nil, err
		}
	}
	if *workspace != "" {
		c.WorkspaceRoot, err = filepath.Abs(*workspace)
		if err != nil {
			return nil, err
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := secure.Dir(c.StateDir); err != nil {
		return nil, err
	}
	if err := privateExact(a.configFile, c); err != nil {
		return nil, err
	}
	return map[string]any{"config": a.configFile, "mode": c.Mode, "enrolled": false}, nil
}

type enrollment struct {
	Request    domain.EnrollRequest `json:"request"`
	Credential config.Credential    `json:"credential"`
}

func (a *app) connect(ctx context.Context, args []string) (any, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	invitation := fs.String("invitation-file", "", "private bootstrap/invitation file")
	name := fs.String("name", "", "agent display name")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *invitation == "" || *name == "" {
		return nil, domain.Fail(domain.Invalid, "--invitation-file and --name are required")
	}
	c, err := config.LoadWorker(a.configFile)
	if err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(c.StateDir, "work.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var bundle config.InvitationBundle
	if err := config.Read(*invitation, &bundle); err != nil {
		return nil, err
	}
	pendingPath := filepath.Join(c.StateDir, "enrollment.json")
	var pending enrollment
	err = config.Read(pendingPath, &pending)
	if os.IsNotExist(err) {
		var existing config.Credential
		if err := config.Read(c.CredentialFile, &existing); err == nil {
			return nil, domain.Fail(domain.Conflict, "credentials already exist; do not enroll a duplicate identity")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		token := domain.NewToken()
		pending = enrollment{Request: domain.EnrollRequest{Command: domain.Command{OperationID: a.operation}, InvitationToken: bundle.Token, Name: *name, CredentialHash: domain.Hash(token)}, Credential: config.Credential{Token: token}}
		if err := config.Write(pendingPath, pending); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if pending.Request.InvitationToken != bundle.Token || pending.Request.Name != *name {
		return nil, domain.Fail(domain.Conflict, "resume enrollment with the exact retained invitation and name")
	}
	if a.explicitOperation && a.operation != pending.Request.OperationID {
		return nil, domain.Fail(domain.Conflict, "resume enrollment with its retained original operation ID")
	}
	a.operation = pending.Request.OperationID
	client, err := api.NewClient(c.URL, c.CAFile, c.ServerName, config.Credential{})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var tunnel *transport.Tunnel
	if c.SSH != nil {
		if err := client.Request(ctx, "GET", "/healthz", nil, new(any)); err != nil {
			tunnel, err = transport.Start(ctx, *c.SSH, process.Native{})
			if err != nil {
				return nil, err
			}
			defer tunnel.Close()
		}
	}
	agent, err := api.Call[domain.Agent](ctx, client, "POST", "/v1/enroll", pending.Request)
	if err != nil {
		return nil, err
	}
	pending.Credential.AgentID = agent.ID
	if err := privateExact(c.CredentialFile, pending.Credential); err != nil {
		return nil, err
	}
	if agent.Founder {
		c.Mode = "manual"
		if err := config.Write(a.configFile, c); err != nil {
			return nil, err
		}
	}
	if err := os.Remove(pendingPath); err != nil {
		return nil, err
	}
	return agent, nil
}

func (a *app) invite(ctx context.Context, r *resources, args []string) (any, error) {
	fs := flag.NewFlagSet("invitation create", flag.ContinueOnError)
	output := fs.String("output", "", "private invitation output")
	ttl := fs.Int("ttl", 900, "expiry seconds")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *output == "" || *ttl < 60 || *ttl > 86400 {
		return nil, domain.Fail(domain.Invalid, "--output and --ttl within 60-86400 seconds are required")
	}
	var bundle config.InvitationBundle
	err := config.Read(*output, &bundle)
	if os.IsNotExist(err) {
		bundle = config.InvitationBundle{Token: domain.NewToken(), OperationID: a.operation, ExpiresAt: time.Now().Unix() + int64(*ttl)}
		if err := config.Write(*output, bundle); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if a.explicitOperation && a.operation != bundle.OperationID {
		return nil, domain.Fail(domain.Conflict, "invitation file is bound to a different operation")
	}
	a.operation = bundle.OperationID
	req := domain.InvitationRequest{Command: domain.Command{OperationID: bundle.OperationID}, TokenHash: domain.Hash(bundle.Token), ExpiresAt: bundle.ExpiresAt}
	inv, err := a.write(ctx, r, "/v1/invitations", req, "")
	return map[string]any{"invitation": inv, "secret_file": *output, "expires_at": bundle.ExpiresAt}, err
}
