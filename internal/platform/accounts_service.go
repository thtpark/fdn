package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
)

// AccountServiceConfig is a root-owned helper configuration. Its listener uses
// Accounts.ServerGID for filesystem reachability, but every HTTP request is also
// bound to Accounts.ServerUID by the kernel. The ingress-only group cannot reach
// this socket and the helper exposes no shell, general commands or file writes.
type AccountServiceConfig struct {
	Socket   string         `json:"socket"`
	Apply    bool           `json:"apply"`
	Accounts AccountOptions `json:"accounts"`
}

func (c *AccountServiceConfig) Validate() error {
	if err := c.Accounts.normalize(); err != nil {
		return err
	}
	if !accountPath.MatchString(c.Socket) || filepath.Clean(c.Socket) != c.Socket || c.Socket == "/" || len(c.Socket) > 103 ||
		c.Socket == c.Accounts.APISocket || c.Socket == c.Accounts.JoinSocket || c.Accounts.ServerUID == 0 {
		return errors.New("account helper requires a distinct protected Unix socket and an unprivileged server UID")
	}
	return nil
}

type AccountProvisionRequest = domain.AccountProvisionRequest

type AccountDisableRequest = domain.AccountDisableRequest

type accountServiceReply struct {
	Account ManagedAccount `json:"account"`
	Error   string         `json:"error,omitempty"`
}

// NewAccountHandler exposes only fixed, typed provision/disable operations.
// Pair it with ingress.Listen and http.Server.ConnContext = ingress.ConnContext.
func NewAccountHandler(manager *AccountManager, serverUID uint32) http.Handler {
	busy := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		reply := func(status int, account ManagedAccount, problem string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(accountServiceReply{Account: account, Error: problem})
		}
		uid, ok := ingress.UID(r.Context())
		if !ok || uid != serverUID || manager == nil {
			reply(http.StatusForbidden, ManagedAccount{}, "configured server OS account required")
			return
		}
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || (r.URL.Path != "/v1/accounts/provision" && r.URL.Path != "/v1/accounts/disable") {
			reply(http.StatusNotFound, ManagedAccount{}, "unknown account operation")
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			reply(http.StatusUnsupportedMediaType, ManagedAccount{}, "JSON account operation required")
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			reply(http.StatusServiceUnavailable, ManagedAccount{}, "another retained account operation is active")
			return
		}
		decode := func(value any) error {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(value); err != nil {
				return err
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				return errors.New("one typed account operation required")
			}
			return nil
		}
		var account ManagedAccount
		var err error
		if r.URL.Path == "/v1/accounts/provision" {
			var request AccountProvisionRequest
			if err := decode(&request); err != nil {
				reply(http.StatusBadRequest, ManagedAccount{}, "invalid account provision request")
				return
			}
			var plan AccountPlan
			plan, err = manager.Plan(r.Context(), AccountRequest{AgentID: request.AgentID, PublicKey: request.PublicKey, Generation: request.Generation, Reactivate: request.Reactivate})
			if err == nil {
				account, err = manager.Apply(r.Context(), plan, true)
			}
		} else {
			var request AccountDisableRequest
			if err := decode(&request); err != nil {
				reply(http.StatusBadRequest, ManagedAccount{}, "invalid account disable request")
				return
			}
			account, err = manager.Disable(r.Context(), request.AgentID, request.Generation, true)
		}
		if err != nil {
			reply(http.StatusConflict, ManagedAccount{}, "account operation remains denied or pending; reconcile its retained identity")
			return
		}
		reply(http.StatusOK, account, "")
	})
}

// AccountClient reaches only a protected root-owned Unix helper socket. There
// is no URL/network override and redirects are never followed.
type AccountClient struct {
	http        *http.Client
	messageDial func(context.Context) (net.Conn, error)
}

func NewAccountClient(socket string) (*AccountClient, error) {
	return newMessageAccountClient(socket, 0)
}

func newAccountClient(socket string, helperUID uint32) (*AccountClient, error) {
	if !accountPath.MatchString(socket) || filepath.Clean(socket) != socket || socket == "/" || len(socket) > 103 {
		return nil, errors.New("account helper requires a canonical protected Unix path")
	}
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 2 * time.Minute, MaxConnsPerHost: 4, IdleConnTimeout: time.Minute,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != "accounts.local:80" {
				return nil, errors.New("account helper client cannot dial another origin")
			}
			if err := accountAncestors(filepath.Dir(socket), helperUID, false); err != nil {
				return nil, err
			}
			info, err := os.Lstat(socket)
			if err != nil {
				return nil, err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != helperUID || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0660) {
				return nil, errors.New("account helper socket ownership or permissions changed")
			}
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
			if err != nil {
				return nil, err
			}
			uid, ok := ingress.UID(ingress.ConnContext(ctx, conn))
			if !ok || uid != helperUID {
				_ = conn.Close()
				return nil, errors.New("account helper is not the pinned local root OS peer")
			}
			return conn, nil
		}}
	return &AccountClient{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *AccountClient) Close() {
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *AccountClient) Provision(ctx context.Context, request AccountProvisionRequest) (ManagedAccount, error) {
	account, err := c.call(ctx, "/v1/accounts/provision", request)
	if err == nil && (account.AgentID != request.AgentID || account.Generation != request.Generation) {
		return ManagedAccount{}, ErrAccountConflict
	}
	return account, err
}

func (c *AccountClient) Disable(ctx context.Context, agentID string, generation int64) (ManagedAccount, error) {
	account, err := c.call(ctx, "/v1/accounts/disable", AccountDisableRequest{AgentID: agentID, Generation: generation})
	if err == nil && (account.AgentID != agentID || account.Generation != generation) {
		return ManagedAccount{}, ErrAccountConflict
	}
	return account, err
}

func (c *AccountClient) call(ctx context.Context, path string, value any) (ManagedAccount, error) {
	if c.messageDial != nil {
		return c.callMessage(ctx, path, value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ManagedAccount{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://accounts.local"+path, bytes.NewReader(data))
	if err != nil {
		return ManagedAccount{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return ManagedAccount{}, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return ManagedAccount{}, errors.New("invalid bounded account helper reply")
	}
	var reply accountServiceReply
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return ManagedAccount{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ManagedAccount{}, errors.New("invalid trailing account helper reply")
	}
	if response.StatusCode != http.StatusOK || reply.Error != "" {
		return ManagedAccount{}, errors.New("native account provisioning remains pending or denied")
	}
	name, nameErr := AccountUsername(reply.Account.AgentID)
	absentDisable := path == "/v1/accounts/disable" && reply.Account.State == AccountDisabled && reply.Account.UID == 0 && reply.Account.GID == 0 && reply.Account.Fingerprint == "" && reply.Account.PublicKey == ""
	if nameErr != nil || reply.Account.Username != name || reply.Account.Generation < 1 || !absentDisable && !validAccountFingerprint(reply.Account.Fingerprint) {
		return ManagedAccount{}, errors.New("invalid managed account helper identity")
	}
	return reply.Account, nil
}
