package platform

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
)

func newMessageAccountClient(socket string, helperUID uint32) (*AccountClient, error) {
	if !accountPath.MatchString(socket) || filepath.Clean(socket) != socket || socket == "/" || len(socket) > 103 {
		return nil, errors.New("account helper requires a canonical protected Unix path")
	}
	return &AccountClient{messageDial: func(ctx context.Context) (net.Conn, error) {
		if err := accountAncestors(filepath.Dir(socket), helperUID, false); err != nil {
			return nil, err
		}
		info, err := os.Lstat(socket)
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != helperUID ||
			(info.Mode().Perm() != 0600 && info.Mode().Perm() != 0660) {
			return nil, domain.Fail(domain.Denied, "account helper socket ownership or permissions changed")
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		uid, ok := ingress.UID(ingress.ConnContext(ctx, conn))
		if !ok || uid != helperUID {
			_ = conn.Close()
			return nil, domain.Fail(domain.Denied, "account helper is not the pinned local OS peer")
		}
		return conn, nil
	}}, nil
}

func (c *AccountClient) callMessage(ctx context.Context, path string, value any) (ManagedAccount, error) {
	operation := ""
	switch path {
	case "/v1/accounts/provision":
		operation = "accounts.provision"
	case "/v1/accounts/disable":
		operation = "accounts.disable"
	default:
		return ManagedAccount{}, domain.Fail(domain.Denied, "unknown account operation")
	}
	payload, err := message.MarshalPayload(value)
	if err != nil {
		return ManagedAccount{}, err
	}
	if len(payload) > 64<<10 {
		return ManagedAccount{}, domain.Fail(domain.Invalid, "account operation exceeds its bound")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := c.messageDial(ctx)
	if err != nil {
		return ManagedAccount{}, err
	}
	defer conn.Close()
	session, err := message.NewSession(ctx, conn, nil)
	if err != nil {
		return ManagedAccount{}, err
	}
	defer session.Close()
	data, err := session.Call(ctx, message.Request{Operation: operation, Payload: payload})
	if err != nil {
		return ManagedAccount{}, err
	}
	var account ManagedAccount
	if err := message.UnmarshalPayload(data, &account); err != nil {
		return ManagedAccount{}, err
	}
	_, err = accountUsername(account.AgentID, account.Username)
	absentDisable := operation == "accounts.disable" && account.State == AccountDisabled &&
		account.UID == 0 && account.GID == 0 && account.Fingerprint == "" && account.PublicKey == ""
	if err != nil || account.Username == "" || account.Generation < 1 ||
		(!absentDisable && !validAccountFingerprint(account.Fingerprint)) {
		return ManagedAccount{}, domain.Fail(domain.Unavailable, "invalid managed account reply")
	}
	return account, nil
}

// NewAccountMessageHandler grants only provision/disable to the exact service
// UID. It is never reachable through an ordinary agent SSH gateway.
func NewAccountMessageHandler(manager *AccountManager, serverUID uint32) func(context.Context, net.Conn) error {
	busy := make(chan struct{}, 1)
	return func(ctx context.Context, conn net.Conn) error {
		ctx = ingress.ConnContext(ctx, conn)
		uid, ok := ingress.UID(ctx)
		if !ok || uid != serverUID || manager == nil {
			return domain.Fail(domain.Denied, "configured server OS account required")
		}
		session, err := message.NewSession(ctx, conn, func(call context.Context, request message.Request) ([]byte, error) {
			if request.Target != "" || len(request.Query) != 0 || len(request.Payload) > 64<<10 {
				return nil, domain.Fail(domain.Invalid, "invalid bounded account operation")
			}
			if request.Operation != "accounts.provision" && request.Operation != "accounts.disable" {
				return nil, domain.Fail(domain.Denied, "unknown account operation")
			}
			select {
			case busy <- struct{}{}:
				defer func() { <-busy }()
			default:
				return nil, domain.Fail(domain.Unavailable, "another retained account operation is active")
			}
			var account ManagedAccount
			var err error
			if request.Operation == "accounts.provision" {
				var input AccountProvisionRequest
				if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
					return nil, err
				}
				plan, planErr := manager.Plan(call, AccountRequest{AgentID: input.AgentID, Username: input.Username, PublicKey: input.PublicKey,
					Generation: input.Generation, Reactivate: input.Reactivate})
				err = planErr
				if err == nil {
					account, err = manager.Apply(call, plan, true)
				}
			} else {
				var input AccountDisableRequest
				if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
					return nil, err
				}
				account, err = manager.Disable(call, input, true)
			}
			if err != nil {
				return nil, domain.Fail(domain.Conflict, "account operation remains denied or pending; reconcile retained identity")
			}
			return message.MarshalPayload(account)
		})
		if err != nil {
			return err
		}
		defer session.Close()
		<-session.Done()
		return session.Err()
	}
}
