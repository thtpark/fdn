package cli

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/remote"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

type resources struct {
	config config.Worker
	state  *local.State
	client *api.Client
}

func (r *resources) close() error {
	if r.client != nil {
		r.client.Close()
	}
	if r.state != nil {
		return r.state.Close()
	}
	return nil
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
	if credential.AgentID == "" || credential.Token != "" {
		state.Close()
		return nil, domain.Fail(domain.Prerequisite, "legacy credentials cannot authenticate; migrate through Founder-approved key enrollment and connect to the local identity service")
	}
	client, err := api.NewLocalClient(c)
	if err != nil {
		state.Close()
		return nil, err
	}
	client.Credential = credential
	r.client = client
	me, err := api.Call[domain.Agent](ctx, client, "GET", "/v1/me", nil)
	if err != nil {
		r.close()
		return nil, err
	}
	if me.ID != credential.AgentID {
		r.close()
		return nil, domain.Fail(domain.Denied, "local identity service does not match the enrolled worker; never borrow another identity")
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
	r, err := a.load(ctx, true)
	if err != nil {
		return nil, err
	}
	defer r.close()
	c, state, client := r.config, r.state, r.client
	credential := client.Credential
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if _, err := guidance.Install(home); err != nil {
		return nil, err
	}
	if _, err := a.linkProviders(); err != nil {
		return nil, err
	}
	engine := work.New(c, a.configFile, client, state, provider.New(c))
	engine.Log = slog.New(slog.NewJSONHandler(a.diagnostic, nil))
	sessions := &remote.Manager{Client: client, State: state, Config: c, Opener: remote.Native{}, Log: engine.Log}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	identitySocket := fs.String("identity-socket", "", "protected socket of this agent's separate identity service")
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
	if *identitySocket != "" {
		c.IdentitySocket, err = filepath.Abs(*identitySocket)
		if err != nil {
			return nil, err
		}
	}
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
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	guide, err := guidance.Install(home)
	if err != nil {
		return nil, err
	}
	if err := privateExact(a.configFile, c); err != nil {
		return nil, err
	}
	return map[string]any{"config": a.configFile, "mode": c.Mode, "enrolled": false, "guidance": guide}, nil
}
