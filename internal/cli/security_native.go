package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/platform"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/transport"
)

type nativeAccountClient interface {
	Provision(context.Context, platform.AccountProvisionRequest) (platform.ManagedAccount, error)
	Disable(context.Context, platform.AccountDisableRequest) (platform.ManagedAccount, error)
	Close()
}

type nativeInitPlan struct {
	Config      config.Server         `json:"config"`
	PKI         pki.Bundle            `json:"pki"`
	Founder     domain.PublicIdentity `json:"founder"`
	OperationID string                `json:"operation_id"`
}

type nativeInitReceipt struct {
	Founder     domain.PublicIdentity `json:"founder"`
	OperationID string                `json:"operation_id"`
	AgentID     string                `json:"agent_id"`
}

func validateNativeServer(c config.Server) error {
	if c.Listen != "" {
		return domain.Fail(domain.Prerequisite, "legacy TCP configuration is not a native identity boundary; migrate explicitly without resetting state")
	}
	for _, path := range []string{c.StateDir, c.CertFile, c.KeyFile, c.IngressSocket, c.JoinSocket, c.AccountSocket} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n") {
			return domain.Fail(domain.Invalid, "native server paths must be absolute and canonical")
		}
	}
	maintenance := filepath.Join(c.StateDir, "maintenance.sock")
	seen := map[string]bool{}
	for _, socket := range []string{c.IngressSocket, c.JoinSocket, c.AccountSocket, maintenance} {
		if len(socket) > 103 || seen[socket] {
			return domain.Fail(domain.Invalid, "native ingress, join, account and maintenance sockets must be distinct and bounded")
		}
		seen[socket] = true
	}
	if c.IngressGroup <= 0 || int64(c.IngressGroup) > 1<<31-1 {
		return domain.Fail(domain.Invalid, "native ingress requires an explicit non-root ingress-only group")
	}
	return nil
}

func nativeCurrentIngressReady(group int) error {
	if group <= 0 || group == os.Getegid() {
		return domain.Fail(domain.Denied, "ingress group must be separate from the server's primary DB group")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	if !slices.Contains(groups, group) {
		return domain.Fail(domain.Prerequisite, "current service process lacks ingress group membership; prepare the root helper and restart with initgroups or SupplementaryGroups")
	}
	return nil
}

func nativeAccountConnection(c config.Server, supplied nativeAccountClient) (nativeAccountClient, error) {
	if supplied != nil {
		return supplied, nil
	}
	return platform.NewAccountClient(c.AccountSocket)
}

type nativeIdentityRegistry interface {
	Identity(context.Context, string) (domain.AgentIdentity, error)
	BindIdentityAccount(context.Context, string, string, int64, string) (domain.AgentIdentity, error)
}

func provisionNativeIdentity(ctx context.Context, store nativeIdentityRegistry, client nativeAccountClient, c config.Server, identity domain.AgentIdentity) (domain.AgentIdentity, error) {
	if identity.State == "banned" || identity.State == "revoked" || identity.State == "restore-pending" {
		return identity, domain.Fail(domain.Denied, "banned, revoked or quarantined identity requires explicit recovery with fresh keys")
	}
	current, err := store.Identity(ctx, identity.AgentID)
	if err != nil {
		return identity, err
	}
	if current.Generation != identity.Generation || current.RequestID != identity.RequestID || current.SSHUser != identity.SSHUser || current.UID != identity.UID || current.SSHFingerprint != identity.SSHFingerprint || current.SSHKey != identity.SSHKey || (current.State != "pending" && current.State != "provisioned" && current.State != "active") {
		return current, domain.Fail(domain.Conflict, "native provisioning request is no longer the approved current identity")
	}
	canonical, fingerprint, err := platform.CanonicalAccountKey(identity.SSHKey)
	if err != nil || fingerprint != identity.SSHFingerprint {
		return identity, domain.Fail(domain.Denied, "registry public key fingerprint is not canonical")
	}
	account, err := client.Provision(ctx, platform.AccountProvisionRequest{AgentID: identity.AgentID, Username: identity.SSHUser, PublicKey: canonical, Generation: identity.Generation, Reactivate: identity.State == "pending" || identity.Generation > 1})
	if err != nil {
		return identity, domain.Fail(domain.Prerequisite, "native account provisioning remains pending; reconcile the retained identity")
	}
	if !domain.ValidAccountUsername(identity.SSHUser) || account.AgentID != identity.AgentID || account.Username != identity.SSHUser || account.UID == 0 || account.GID != uint32(c.IngressGroup) ||
		account.Generation != identity.Generation || account.Fingerprint != fingerprint || account.PublicKey != canonical || account.State != platform.AccountActive || identity.UID >= 0 && identity.UID != int64(account.UID) {
		return identity, domain.Fail(domain.Denied, "native provisioner returned a mismatched immutable account binding")
	}
	bound, err := store.BindIdentityAccount(ctx, identity.AgentID, account.Username, int64(account.UID), account.Fingerprint)
	if err != nil {
		return identity, err
	}
	if bound.Generation != identity.Generation || bound.SSHFingerprint != identity.SSHFingerprint || bound.RequestID != identity.RequestID || bound.SSHUser != account.Username || bound.UID != int64(account.UID) {
		return bound, domain.Fail(domain.Conflict, "native account binding belongs to a superseded identity generation")
	}
	current, err = store.Identity(ctx, identity.AgentID)
	if err != nil {
		return bound, err
	}
	if current.Generation != identity.Generation || current.RequestID != identity.RequestID || current.SSHFingerprint != identity.SSHFingerprint || current.UID != int64(account.UID) || current.SSHUser != account.Username || (current.State != "provisioned" && current.State != "active") {
		return current, domain.Fail(domain.Conflict, "native account provision was superseded; current authoritative identity remains fenced")
	}
	return current, nil
}

func disableNativeIdentity(ctx context.Context, client nativeAccountClient, identity domain.AgentIdentity) error {
	account, err := client.Disable(ctx, platform.AccountDisableRequest{AgentID: identity.AgentID, Username: identity.SSHUser, Generation: identity.Generation})
	if err != nil {
		return domain.Fail(domain.Prerequisite, "application access is denied; native account cleanup remains pending")
	}
	if !domain.ValidAccountUsername(identity.SSHUser) || account.AgentID != identity.AgentID || account.Username != identity.SSHUser || account.State != platform.AccountDisabled || account.Generation != identity.Generation || identity.UID >= 0 && int64(account.UID) != identity.UID {
		return domain.Fail(domain.Denied, "native account cleanup did not match the retained identity")
	}
	return nil
}

func reconcileNativeIdentities(ctx context.Context, store *foundation.Store, client nativeAccountClient, c config.Server) error {
	identities, err := store.ManagedIdentities(ctx)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		switch identity.State {
		case "banned", "revoked", "restore-pending":
			err = disableNativeIdentity(ctx, client, identity)
		case "pending", "provisioned", "active":
			_, err = provisionNativeIdentity(ctx, store, client, c, identity)
		default:
			return domain.Fail(domain.Prerequisite, "unknown retained native identity state")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func reconcileNativePending(ctx context.Context, store *foundation.Store, client nativeAccountClient, c config.Server, disabled map[string]bool) error {
	identities, err := store.ManagedIdentities(ctx)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		switch identity.State {
		case "pending":
			if _, err := provisionNativeIdentity(ctx, store, client, c, identity); err != nil {
				return err
			}
		case "banned", "revoked", "restore-pending":
			key := identity.AgentID + "/" + identity.SSHFingerprint + "/" + identity.State + "/" + strconv.FormatInt(identity.Generation, 10)
			if !disabled[key] {
				if err := disableNativeIdentity(ctx, client, identity); err != nil {
					return err
				}
				disabled[key] = true
			}
		}
	}
	return nil
}

func startNativeReconciliation(ctx context.Context, store *foundation.Store, client nativeAccountClient, c config.Server, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		disabled := map[string]bool{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				attempt, stop := context.WithTimeout(ctx, 30*time.Second)
				err := reconcileNativePending(attempt, store, client, c, disabled)
				stop()
				if err != nil && ctx.Err() == nil {
					log.Warn("native identity reconciliation remains pending", "code", domain.AsFault(err).Code)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func readNativePublic(path string) (domain.PublicIdentity, error) {
	var public domain.PublicIdentity
	if path == "" || path == "-" {
		return public, domain.Fail(domain.Invalid, "a protected public identity file is required")
	}
	data, err := secure.ReadPublic(path, domain.MaxBody)
	if err != nil {
		return public, err
	}
	if err := config.Decode(data, &public); err != nil {
		return public, err
	}
	_, _, err = device.Validate(public)
	return public, err
}

func (a *app) initNativeFoundation(ctx context.Context, args []string, supplied nativeAccountClient) (any, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	state := fs.String("state", "", "private Foundation directory")
	request := fs.String("founder-request", "", "protected Founder public identity file")
	apiSocket := fs.String("ingress-socket", "", "protected API Unix socket")
	joinSocket := fs.String("join-socket", "", "protected admission Unix socket")
	group := fs.Int("ingress-group", -1, "ingress-only numeric OS group")
	accountSocket := fs.String("account-socket", "", "protected root account helper Unix socket")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *state == "" || *request == "" {
		return nil, domain.Fail(domain.Invalid, "--state and --founder-request are required")
	}
	if supplied == nil && os.Geteuid() == 0 {
		return nil, domain.Fail(domain.Denied, "initialize Foundation as its dedicated unprivileged service account")
	}
	public, err := readNativePublic(*request)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(*state)
	if err != nil {
		return nil, err
	}
	c := config.Server{StateDir: root, CertFile: filepath.Join(root, "server.pem"), KeyFile: filepath.Join(root, "server-key.pem"), IngressSocket: *apiSocket, JoinSocket: *joinSocket, IngressGroup: *group, AccountSocket: *accountSocket}
	if err := validateNativeServer(c); err != nil {
		return nil, err
	}
	if supplied == nil {
		if err := nativeCurrentIngressReady(c.IngressGroup); err != nil {
			return nil, err
		}
	}
	if err := nativeProtectedAncestors(filepath.Dir(root), uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	if err := secure.Dir(root); err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(root, "foundation.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	client, err := nativeAccountConnection(c, supplied)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	receiptPath := filepath.Join(root, "init-identity.json")
	existing, readErr := config.LoadServer(a.configFile)
	if readErr == nil {
		if existing != c {
			return nil, domain.Fail(domain.Conflict, "existing configuration differs; initialization never overwrites identities or state")
		}
		var receipt nativeInitReceipt
		if err := config.Read(receiptPath, &receipt); err != nil {
			return nil, domain.Fail(domain.Prerequisite, "retained native initialization receipt is required; state was not reset")
		}
		if receipt.Founder != public || a.explicitOperation && a.operation != receipt.OperationID {
			return nil, domain.Fail(domain.Conflict, "native initialization requires its exact original public identity and operation")
		}
		a.operation = receipt.OperationID
		store, err := foundation.Open(ctx, filepath.Join(root, "foundation.sqlite"))
		if err != nil {
			return nil, err
		}
		defer store.Close()
		identity, err := store.Identity(ctx, receipt.AgentID)
		if err != nil {
			return nil, err
		}
		if identity.State == "pending" {
			identity, err = provisionNativeIdentity(ctx, store, client, c, identity)
		}
		if err == nil {
			planPath := filepath.Join(root, "init-plan.json")
			var retained nativeInitPlan
			if readErr := config.Read(planPath, &retained); readErr == nil {
				if retained.Config != c || retained.Founder != receipt.Founder || retained.OperationID != receipt.OperationID {
					return nil, domain.Fail(domain.Conflict, "retained initialization plan does not match its receipt")
				}
				if err = os.Remove(planPath); err == nil {
					err = secure.SyncDir(root)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				err = readErr
			}
		}
		return map[string]any{"initialized": true, "changed": false, "config": a.configFile, "ca_file": filepath.Join(root, "ca.pem"), "identity": identity}, err
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	planPath := filepath.Join(root, "init-plan.json")
	var plan nativeInitPlan
	readErr = config.Read(planPath, &plan)
	if errors.Is(readErr, os.ErrNotExist) {
		bundle, err := pki.Generate(time.Now())
		if err != nil {
			return nil, err
		}
		plan = nativeInitPlan{Config: c, PKI: bundle, Founder: public, OperationID: a.operation}
		if err := config.Write(planPath, plan); err != nil {
			return nil, err
		}
	} else if readErr != nil {
		return nil, readErr
	}
	if plan.Config != c || plan.Founder != public || !domain.ValidOperation(plan.OperationID) || a.explicitOperation && a.operation != plan.OperationID {
		return nil, domain.Fail(domain.Conflict, "resume initialization with its exact original native paths, public identity and operation")
	}
	a.operation = plan.OperationID
	for _, file := range []struct {
		name string
		data []byte
	}{{"ca.pem", plan.PKI.CA}, {"ca-key.pem", plan.PKI.CAKey}, {"server.pem", plan.PKI.Cert}, {"server-key.pem", plan.PKI.Key}} {
		path := filepath.Join(root, file.name)
		if err := secure.Create(path, file.data); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
			data, err := secure.Read(path, domain.MaxBody)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(data, file.data) {
				return nil, domain.Fail(domain.Conflict, "existing TLS files differ from retained initialization; identities were not reset")
			}
		}
	}
	store, err := foundation.Open(ctx, filepath.Join(root, "foundation.sqlite"))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	identity, err := store.BootstrapIdentity(ctx, public, plan.OperationID)
	if err != nil {
		return nil, err
	}
	if err := privateExact(receiptPath, nativeInitReceipt{Founder: public, OperationID: plan.OperationID, AgentID: identity.AgentID}); err != nil {
		return nil, err
	}
	if err := privateExact(a.configFile, c); err != nil {
		return nil, err
	}
	identity, err = provisionNativeIdentity(ctx, store, client, c, identity)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(planPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := secure.SyncDir(root); err != nil {
		return nil, err
	}
	return map[string]any{"initialized": true, "changed": true, "config": a.configFile, "ca_file": filepath.Join(root, "ca.pem"), "identity": identity}, nil
}

func (a *app) serveNativeFoundation(ctx context.Context, args []string, supplied nativeAccountClient) (any, error) {
	if len(args) != 0 {
		return nil, domain.Fail(domain.Invalid, "serve has no leaf flags")
	}
	if supplied == nil && os.Geteuid() == 0 {
		return nil, domain.Fail(domain.Denied, "Foundation API must run as its dedicated unprivileged service account")
	}
	c, err := config.LoadServer(a.configFile)
	if err != nil {
		return nil, err
	}
	if err := validateNativeServer(c); err != nil {
		return nil, err
	}
	if supplied == nil {
		if err := nativeCurrentIngressReady(c.IngressGroup); err != nil {
			return nil, err
		}
	}
	if err := nativeProtectedAncestors(c.StateDir, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(c.StateDir, "tls-renewal.json")); err == nil {
		return nil, domain.Fail(domain.Prerequisite, "complete retained TLS renewal before serving")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(c.StateDir, "foundation.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	store, err := foundation.Open(ctx, filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	client, err := nativeAccountConnection(c, supplied)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := reconcileNativeIdentities(ctx, store, client, c); err != nil {
		return nil, err
	}
	service, err := api.New(store, slog.New(slog.NewJSONHandler(a.diagnostic, nil)))
	if err != nil {
		return nil, err
	}
	defer service.Close()
	if err := service.ConfigureIdentity(api.IdentityOptions{CAFile: filepath.Join(c.StateDir, "ca.pem"), CAKeyFile: filepath.Join(c.StateDir, "ca-key.pem"),
		Provision: func(ctx context.Context, identity domain.AgentIdentity) (domain.AgentIdentity, error) {
			return provisionNativeIdentity(ctx, store, client, c, identity)
		},
		Disable: func(ctx context.Context, identity domain.AgentIdentity) error {
			return disableNativeIdentity(ctx, client, identity)
		},
	}); err != nil {
		return nil, err
	}
	mainTLS, err := pki.ServerMutual(c.CertFile, c.KeyFile, filepath.Join(c.StateDir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	joinTLS := mainTLS.Clone()
	joinTLS.ClientAuth = tls.VerifyClientCertIfGiven
	verifyPresentedClient := joinTLS.VerifyConnection
	joinTLS.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 && len(state.VerifiedChains) == 0 {
			return nil
		}
		return verifyPresentedClient(state)
	}
	type endpoint struct {
		path    string
		group   int
		purpose string
		tls     *tls.Config
	}
	mainTLS.NextProtos, joinTLS.NextProtos = []string{api.MessageALPN}, []string{api.MessageALPN}
	endpoints := []endpoint{{c.IngressSocket, c.IngressGroup, "api", mainTLS}, {c.JoinSocket, c.IngressGroup, "join", joinTLS}, {filepath.Join(c.StateDir, "maintenance.sock"), -1, "maintenance", nil}}
	ctx, cancelConnections := context.WithCancel(ctx)
	defer cancelConnections()
	var listeners []net.Listener
	done := make(chan error, len(endpoints))
	remaining := 0
	defer func() {
		cancelConnections()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		for remaining > 0 {
			<-done
			remaining--
		}
	}()
	for _, endpoint := range endpoints {
		listener, err := ingress.Listen(endpoint.path, endpoint.group)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		remaining++
		go func() {
			done <- transport.ServeConnections(ctx, listener, func(call context.Context, conn net.Conn) error {
				if endpoint.tls != nil {
					conn = tls.Server(conn, endpoint.tls.Clone())
				}
				return service.ServeMessageConnection(call, conn, endpoint.purpose)
			}, func(err error) {
				service.Log.Warn("native message connection closed", "purpose", endpoint.purpose, "code", domain.AsFault(err).Code)
			})
		}()
	}
	stopReconciliation := startNativeReconciliation(ctx, store, client, c, service.Log)
	defer stopReconciliation()
	if a.emit(map[string]any{"state": "listening", "ingress_socket": c.IngressSocket, "join_socket": c.JoinSocket, "version": domain.Version}, nil) != 0 {
		return nil, domain.Fail(domain.Unavailable, "could not publish native listener readiness")
	}
	select {
	case err := <-done:
		remaining--
		return nil, err
	case <-ctx.Done():
		_ = service.Close()
		cancelConnections()
		return map[string]string{"state": "stopped"}, nil
	}
}

func readRootNativeConfig(path string, value any) error { return readOwnedNativeConfig(path, value, 0) }
func readOwnedNativeConfig(path string, value any, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return domain.Fail(domain.Invalid, "a protected absolute root configuration file is required")
	}
	if err := nativeProtectedAncestors(filepath.Dir(path), owner); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != owner || info.Mode().Perm()&0022 != 0 || info.Size() > domain.MaxBody {
		return domain.Fail(domain.Denied, "native configuration must be bounded, root-owned and non-writable by other accounts")
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if err != nil {
		return err
	}
	if len(data) > domain.MaxBody {
		return domain.Fail(domain.Invalid, "native configuration exceeds its limit")
	}
	return config.Decode(data, value)
}

func nativeProtectedAncestors(path string, owner uint32) error {
	current := "/"
	for _, component := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...) {
		if component != "" {
			current = filepath.Join(current, component)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != owner) || info.Mode().Perm()&0022 != 0 {
			return domain.Fail(domain.Denied, "native configuration ancestors must be protected and non-symlinked")
		}
	}
	return nil
}

func (a *app) nativeGateway(ctx context.Context, args []string) (any, error) {
	a.nativeOutput = true
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	input := fs.String("input", "", "root-owned fixed gateway configuration")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	var options ingress.ProxyOptions
	if err := readRootNativeConfig(*input, &options); err != nil {
		return nil, err
	}
	reader, ok := a.in.(io.ReadCloser)
	if !ok {
		return nil, domain.Fail(domain.Prerequisite, "gateway requires interruptible SSH stdin")
	}
	writer, ok := a.out.(io.WriteCloser)
	if !ok {
		return nil, domain.Fail(domain.Prerequisite, "gateway requires interruptible SSH stdout")
	}
	return nil, ingress.Proxy(ctx, os.Getenv("SSH_ORIGINAL_COMMAND"), reader, writer, options)
}

func (a *app) nativeAccounts(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 || args[0] != "serve" {
		return nil, domain.Fail(domain.Invalid, "accounts requires serve --input ROOT_CONFIG")
	}
	fs := flag.NewFlagSet("accounts serve", flag.ContinueOnError)
	input := fs.String("input", "", "root-owned native account helper configuration")
	if err := parse(fs, args[1:]); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, domain.Fail(domain.Denied, "native account helper requires explicit Linux root execution")
	}
	var c platform.AccountServiceConfig
	if err := readRootNativeConfig(*input, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Apply {
		return nil, domain.Fail(domain.Denied, "root account helper configuration must explicitly approve apply")
	}
	manager, err := platform.NewNativeAccountManager(c.Accounts, nil)
	if err != nil {
		return nil, err
	}
	if _, err := manager.Prepare(ctx, c.Apply); err != nil {
		return nil, err
	}
	if _, err := manager.Reconcile(ctx, c.Apply); err != nil {
		return nil, err
	}
	listener, err := ingress.Listen(c.Socket, int(c.Accounts.ServerGID))
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(a.diagnostic, nil))
	done := make(chan error, 1)
	go func() {
		done <- transport.ServeConnections(ctx, listener, platform.NewAccountMessageHandler(manager, c.Accounts.ServerUID), func(err error) {
			logger.Warn("account message connection closed", "code", domain.AsFault(err).Code)
		})
	}()
	if a.emit(map[string]any{"state": "listening", "account_socket": c.Socket}, nil) != 0 {
		cancel()
		_ = listener.Close()
		<-done
		return nil, domain.Fail(domain.Unavailable, "could not publish helper readiness")
	}
	select {
	case err := <-done:
		return nil, err
	case <-ctx.Done():
		cancel()
		_ = listener.Close()
		serveErr := <-done
		return map[string]string{"state": "stopped"}, serveErr
	}
}

func readMaintenanceConfig(path string) (config.Server, uint32, error) {
	var c config.Server
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return c, 0, domain.Fail(domain.Invalid, "maintenance requires an absolute canonical protected server configuration")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return c, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return c, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > domain.MaxBody {
		return c, 0, domain.Fail(domain.Denied, "maintenance requires the protected server configuration")
	}
	if err := nativeProtectedAncestors(filepath.Dir(path), stat.Uid); err != nil {
		return c, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if err != nil || len(data) > domain.MaxBody {
		return c, 0, domain.Fail(domain.Invalid, "invalid maintenance configuration")
	}
	if err := config.Decode(data, &c); err != nil {
		return c, 0, err
	}
	if err := validateNativeServer(c); err != nil {
		return c, 0, err
	}
	if err := nativeProtectedAncestors(c.StateDir, stat.Uid); err != nil {
		return c, 0, err
	}
	state, err := os.Lstat(c.StateDir)
	if err != nil {
		return c, 0, err
	}
	owner, ok := state.Sys().(*syscall.Stat_t)
	if !ok || !state.IsDir() || state.Mode().Perm() != 0700 || owner.Uid != stat.Uid {
		return c, 0, domain.Fail(domain.Denied, "server configuration and private state owner differ")
	}
	return c, owner.Uid, nil
}

func (a *app) recoverNativeFounder(ctx context.Context, args []string) (any, error) {
	fs := flag.NewFlagSet("recover-founder", flag.ContinueOnError)
	input := fs.String("input", "", "new Founder public identity file")
	reason := fs.String("reason", "", "explicit operator recovery reason")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if os.Geteuid() != 0 {
		return nil, domain.Fail(domain.Denied, "offline Founder recovery requires the local root operator")
	}
	if err := domain.CheckText(*reason, "recovery reason", 2048, true); err != nil {
		return nil, err
	}
	public, err := readNativePublic(*input)
	if err != nil {
		return nil, err
	}
	c, serverUID, err := readMaintenanceConfig(a.configFile)
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(c.StateDir, "maintenance.sock")
	client := api.NewMessageClient("unix://maintenance.local", func(ctx context.Context) (net.Conn, error) {
		info, err := os.Lstat(socket)
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || stat.Uid != serverUID {
			return nil, domain.Fail(domain.Denied, "protected maintenance socket ownership changed")
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		uid, ok := ingress.UID(ingress.ConnContext(ctx, conn))
		if !ok || uid != serverUID {
			_ = conn.Close()
			return nil, domain.Fail(domain.Denied, "maintenance is not the pinned local service OS peer")
		}
		return conn, nil
	})
	defer client.Close()
	var identity domain.AgentIdentity
	err = client.Request(ctx, "POST", "/v1/identity/recover-founder", domain.RecoverIdentityRequest{Command: domain.Command{OperationID: a.operation}, Reason: *reason, Identity: public}, &identity)
	return map[string]any{"identity": identity, "local_root_recovery": true}, err
}
