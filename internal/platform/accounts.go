package platform

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/crypto/ssh"
)

const AccountInventoryVersion = 1

var (
	ErrAccountApplyRequired = errors.New("managed accounts require explicit apply")
	ErrAccountConflict      = errors.New("managed account inventory or OS identity conflicts with the plan")
	ErrAccountRetired       = errors.New("retired accounts require explicit same-identity recovery with a fresh key")
	agentAccountID          = regexp.MustCompile(`^(agent|device)_[0-9a-f]{32}$`)
	accountPath             = regexp.MustCompile(`^/[a-zA-Z0-9/_.-]+$`)
)

// AccountOptions are administrator-owned installation inputs, not API payloads.
// StateDir is a dedicated private inventory directory. PolicyDir contains only
// root-owned public SSH policy, keys and non-writable account homes. The main
// sshd configuration must Include PolicyDir/sshd.conf; apply checks its effective
// policy and reloads only the explicitly selected ssh/sshd systemd service.
// No account is enabled before that check/reload succeeds.
type AccountOptions struct {
	StateDir      string `json:"state_dir"`
	PolicyDir     string `json:"policy_dir"`
	GatewayBinary string `json:"gateway_binary"`
	APISocket     string `json:"api_socket"`
	JoinSocket    string `json:"join_socket"`
	ServerUID     uint32 `json:"server_uid"`
	ServerGroup   string `json:"server_group"`
	ServerGID     uint32 `json:"server_gid"`
	IngressGroup  string `json:"ingress_group"`
	IngressGID    uint32 `json:"ingress_gid"`
	UIDMin        uint32 `json:"uid_min"`
	UIDMax        uint32 `json:"uid_max"`
	SSHDConfig    string `json:"sshd_config"`
	SSHDService   string `json:"sshd_service"`
}

type AccountRequest struct {
	AgentID    string
	Username   string
	PublicKey  string
	Generation int64
	Reactivate bool
}

type AccountState = domain.AccountState

const (
	AccountPending  AccountState = "pending"
	AccountActive   AccountState = "active"
	AccountRetiring AccountState = "retiring"
	AccountDisabled AccountState = "disabled"
)

type ManagedAccount = domain.ManagedAccount

// AccountInventory is retained even after disable. Neither names, UIDs nor key
// fingerprints from another immutable identity are ever recycled. Pending is
// written durably BEFORE OS changes, so reconciliation can recover a lost ack.
type AccountInventory struct {
	Version       int              `json:"version"`
	Revision      uint64           `json:"revision"`
	OptionsHash   string           `json:"options_hash"`
	GroupReserved bool             `json:"group_reserved"`
	NextUID       uint32           `json:"next_uid"`
	Accounts      []ManagedAccount `json:"accounts"`
}

// AccountPlan contains no executable command or caller-selected OS path.
// Apply regenerates it under the inventory lock and rejects modified/stale plans.
type AccountPlan struct {
	Revision    uint64
	AgentID     string
	Username    string
	UID         uint32
	PublicKey   string
	Fingerprint string
	Generation  int64
	Reactivate  bool
}

// AccountUser and AccountGroup describe the narrowly scoped OS adapter seam.
// Shell is /bin/sh only as sshd's fixed ForceCommand dispatcher, not an SSH shell
// grant. Expired/retired accounts are additionally assigned nologin.
type AccountUser struct {
	Username string
	UID      uint32
	GID      uint32
	Home     string
	Shell    string
	Marker   string
	Groups   []uint32
}

type AccountGroup struct {
	Name    string
	GID     uint32
	Members []string
}

type AccountSSHPolicy struct {
	Username           string
	ForcedCommand      string
	AuthorizedKeysFile string
	MainConfig         string
}

// AccountSystem permits recording fixtures without changing host users or SSH.
// The native implementation is private and uses only fixed process.Runner
// operations. NewNativeAccountManager also enforces Linux + effective root at
// every mutation; substituting a test adapter does not expose a root endpoint.
type AccountSystem interface {
	LookupUser(context.Context, string) (AccountUser, bool, error)
	LookupUID(context.Context, uint32) (AccountUser, bool, error)
	LookupGroup(context.Context, string) (AccountGroup, bool, error)
	LookupGID(context.Context, uint32) (AccountGroup, bool, error)
	CreateGroup(context.Context, AccountGroup) error
	GrantServerIngress(context.Context, AccountUser) error
	CreateUser(context.Context, AccountUser) error
	ConfigureUser(context.Context, AccountUser) error
	DisableUser(context.Context, AccountUser) error
	EnableUser(context.Context, AccountUser) error
	ActivateSSH(context.Context, AccountSSHPolicy) error
}

type AccountManager struct {
	options        AccountOptions
	system         AccountSystem
	native         bool
	filesystemRoot string
	mu             sync.Mutex
}

// NewAccountManager constructs an injected/recording manager. Production uses
// NewNativeAccountManager, never a user-supplied OS adapter.
func NewAccountManager(options AccountOptions, system AccountSystem) (*AccountManager, error) {
	return newAccountManager(options, system, "/")
}

// A logical root is only an injected-system seam, never an AccountOptions input.
// Native managers always validate public traversal from the real filesystem root.
func newAccountManager(options AccountOptions, system AccountSystem, filesystemRoot string) (*AccountManager, error) {
	if err := options.normalize(); err != nil {
		return nil, err
	}
	if system == nil {
		return nil, errors.New("managed accounts require a native or recording OS adapter")
	}
	if _, native := system.(*nativeAccountSystem); native && filesystemRoot != "/" {
		return nil, errors.New("native account systems require the real filesystem root")
	}
	for _, path := range []string{options.StateDir, options.PolicyDir} {
		if _, err := accountRelativePath(filesystemRoot, path); err != nil {
			return nil, err
		}
	}
	return &AccountManager{options: options, system: system, filesystemRoot: filesystemRoot}, nil
}

func accountUsername(agentID, name string) (string, error) {
	if !agentAccountID.MatchString(agentID) {
		return "", errors.New("managed accounts require an immutable Foundation agent or Founder device ID")
	}
	if !domain.ValidAccountUsername(name) {
		return "", errors.New("managed accounts require a non-root OS-safe username")
	}
	return name, nil
}

// CanonicalAccountKey accepts one ordinary SSH public key without key options,
// certificates or weak/legacy RSA/DSA keys. Comments do not affect identity.
func CanonicalAccountKey(value string) (canonical, fingerprint string, err error) {
	if len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r") {
		return "", "", errors.New("invalid managed SSH public key")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(value)))
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return "", "", errors.New("exactly one SSH public key without caller-supplied options is required")
	}
	if _, certificate := key.(*ssh.Certificate); certificate {
		return "", "", errors.New("SSH certificates are not managed account keys")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
	case ssh.KeyAlgoRSA:
		cryptoKey, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return "", "", errors.New("invalid RSA SSH key")
		}
		public, ok := cryptoKey.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || public.N.BitLen() < 3072 {
			return "", "", errors.New("managed RSA keys require at least 3072 bits")
		}
	default:
		return "", "", errors.New("unsupported managed SSH key type")
	}
	return string(ssh.MarshalAuthorizedKey(key)), ssh.FingerprintSHA256(key), nil
}

func (o *AccountOptions) normalize() error {
	if o.IngressGroup == "" {
		o.IngressGroup = "fdn_ingress"
	}
	if o.UIDMin == 0 && o.UIDMax == 0 {
		o.UIDMin, o.UIDMax = 200000, 299999
	}
	if o.SSHDConfig == "" {
		o.SSHDConfig = "/etc/ssh/sshd_config"
	}
	if o.SSHDService == "" {
		o.SSHDService = "sshd"
	}
	for _, path := range []string{o.StateDir, o.PolicyDir, o.GatewayBinary, o.APISocket, o.JoinSocket, o.SSHDConfig} {
		if !accountPath.MatchString(path) || path == "/" || filepath.Clean(path) != path {
			return errors.New("managed account paths must be absolute, canonical, shell/sshd-safe paths")
		}
	}
	if strings.HasPrefix(o.PolicyDir+"/", o.StateDir+"/") || strings.HasPrefix(o.StateDir+"/", o.PolicyDir+"/") ||
		o.APISocket == o.JoinSocket || len(o.APISocket) > 103 || len(o.JoinSocket) > 103 {
		return errors.New("managed inventory/policy directories and API/join sockets must be separate")
	}
	if !username.MatchString(o.IngressGroup) || !username.MatchString(o.ServerGroup) ||
		o.IngressGroup == o.ServerGroup || o.IngressGID == o.ServerGID || o.IngressGID < 10000 ||
		o.IngressGID > 1<<31-1 || o.ServerGID == 0 || o.ServerGID > 1<<31-1 || o.ServerUID == 0 {
		return errors.New("ingress requires a dedicated non-DB OS group with an explicit reserved GID")
	}
	if o.UIDMin < 10000 || o.UIDMax > 1<<31-2 || o.UIDMax < o.UIDMin || o.UIDMax-o.UIDMin > 1000000 {
		return errors.New("invalid bounded managed account UID range")
	}
	if o.SSHDService != "ssh" && o.SSHDService != "sshd" {
		return errors.New("managed accounts support only the native ssh or sshd service")
	}
	return nil
}

func (m *AccountManager) optionsHash() string {
	data, _ := json.Marshal(m.options)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (m *AccountManager) Plan(ctx context.Context, request AccountRequest) (AccountPlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inventory, err := m.loadInventory()
	if err != nil {
		return AccountPlan{}, err
	}
	return m.plan(ctx, inventory, request)
}

func (m *AccountManager) plan(ctx context.Context, inventory AccountInventory, request AccountRequest) (AccountPlan, error) {
	name, err := accountUsername(request.AgentID, request.Username)
	if err != nil {
		return AccountPlan{}, err
	}
	canonical, fingerprint, err := CanonicalAccountKey(request.PublicKey)
	if err != nil {
		return AccountPlan{}, err
	}
	if request.Generation < 1 {
		return AccountPlan{}, errors.New("managed account operations require a positive identity key generation")
	}
	plan := AccountPlan{Revision: inventory.Revision, AgentID: request.AgentID, Username: name, PublicKey: canonical, Fingerprint: fingerprint, Generation: request.Generation, Reactivate: request.Reactivate}
	var existing *ManagedAccount
	for index := range inventory.Accounts {
		account := &inventory.Accounts[index]
		if account.AgentID != request.AgentID && (account.Fingerprint == fingerprint || slices.Contains(account.PreviousFingerprints, fingerprint) || account.Username == name) {
			return AccountPlan{}, ErrAccountConflict
		}
		if account.AgentID == request.AgentID {
			existing = account
			if account.Username != name || request.Generation < account.Generation || request.Generation == account.Generation && fingerprint != account.Fingerprint {
				return AccountPlan{}, ErrAccountConflict
			}
			if request.Generation > account.Generation && fingerprint == account.Fingerprint {
				return AccountPlan{}, ErrAccountRetired
			}
			if (account.State == AccountRetiring || account.State == AccountDisabled) && (!request.Reactivate || request.Generation <= account.Generation) {
				return AccountPlan{}, ErrAccountRetired
			}
			if fingerprint != account.Fingerprint && slices.Contains(account.PreviousFingerprints, fingerprint) {
				return AccountPlan{}, ErrAccountRetired
			}
			plan.UID = account.UID
		}
	}
	// Reject stale/key-conflicting requests before invoking even read-only OS tools.
	if err := m.checkGroup(ctx, inventory); err != nil {
		return AccountPlan{}, err
	}
	if plan.UID != 0 {
		if _, _, err := m.observe(ctx, *existing); err != nil {
			return AccountPlan{}, err
		}
		return plan, nil
	}
	if existing == nil && len(inventory.Accounts) >= 4096 {
		return AccountPlan{}, errors.New("managed account inventory limit reached")
	}
	if _, exists, err := m.system.LookupUser(ctx, name); err != nil {
		return AccountPlan{}, err
	} else if exists {
		return AccountPlan{}, ErrAccountConflict
	}
	for uid, probes := inventory.NextUID, 0; uid <= m.options.UIDMax && probes < 4096; uid, probes = uid+1, probes+1 {
		if uid == m.options.ServerUID {
			continue
		}
		if _, exists, err := m.system.LookupUID(ctx, uid); err != nil {
			return AccountPlan{}, err
		} else if !exists {
			plan.UID = uid
			return plan, nil
		}
	}
	return AccountPlan{}, errors.New("no unused managed account UID in the bounded allocation window")
}

func (m *AccountManager) checkGroup(ctx context.Context, inventory AccountInventory) error {
	serverUser, exists, err := m.system.LookupUID(ctx, m.options.ServerUID)
	if err != nil {
		return err
	}
	if !exists || serverUser.GID != m.options.ServerGID {
		return errors.New("server UID must already have its pinned primary DB group")
	}
	server, exists, err := m.system.LookupGroup(ctx, m.options.ServerGroup)
	if err != nil {
		return err
	}
	if !exists || server.GID != m.options.ServerGID {
		return errors.New("server DB group must already exist with its pinned GID")
	}
	byName, named, err := m.system.LookupGroup(ctx, m.options.IngressGroup)
	if err != nil {
		return err
	}
	byID, numbered, err := m.system.LookupGID(ctx, m.options.IngressGID)
	if err != nil {
		return err
	}
	if !inventory.GroupReserved {
		if named || numbered {
			return ErrAccountConflict
		}
		return nil
	}
	if named != numbered || named && (byName.GID != m.options.IngressGID || byID.Name != m.options.IngressGroup) {
		return ErrAccountConflict
	}
	for _, member := range byName.Members {
		if !slices.ContainsFunc(inventory.Accounts, func(account ManagedAccount) bool { return account.UID > 0 && account.Username == member }) {
			user, exists, err := m.system.LookupUser(ctx, member)
			if err != nil {
				return err
			}
			if !exists || user.UID != m.options.ServerUID {
				return ErrAccountConflict
			}
		}
	}
	return nil
}

func (m *AccountManager) desired(account ManagedAccount) AccountUser {
	return AccountUser{Username: account.Username, UID: account.UID, GID: m.options.IngressGID,
		Home: filepath.Join(m.options.PolicyDir, "homes", account.Username), Shell: "/bin/sh",
		Marker: "fdn-agent=" + account.AgentID, Groups: []uint32{m.options.IngressGID}}
}

func (m *AccountManager) observe(ctx context.Context, account ManagedAccount) (AccountUser, bool, error) {
	byName, named, err := m.system.LookupUser(ctx, account.Username)
	if err != nil {
		return AccountUser{}, false, err
	}
	byID, numbered, err := m.system.LookupUID(ctx, account.UID)
	if err != nil {
		return AccountUser{}, false, err
	}
	marker := m.desired(account).Marker
	if named != numbered || named && (byName.UID != account.UID || byID.Username != account.Username || byName.Marker != marker || byID.Marker != marker) {
		return AccountUser{}, false, ErrAccountConflict
	}
	return byName, named, nil
}

// Apply requires explicit approval even for a recording adapter. A completed
// exact replay is idempotent; changed identities/keys and modified stale plans
// fail closed. Reconcile is the explicit recovery path for retained pending work.
func (m *AccountManager) Apply(ctx context.Context, plan AccountPlan, apply bool) (ManagedAccount, error) {
	if err := m.guard(ctx, apply); err != nil {
		return ManagedAccount{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, err := m.lockInventory()
	if err != nil {
		return ManagedAccount{}, err
	}
	defer lock.Close()
	inventory, err := m.loadInventory()
	if err != nil {
		return ManagedAccount{}, err
	}
	expected, err := m.plan(ctx, inventory, AccountRequest{AgentID: plan.AgentID, Username: plan.Username, PublicKey: plan.PublicKey, Generation: plan.Generation, Reactivate: plan.Reactivate})
	if err != nil {
		return ManagedAccount{}, err
	}
	index := slices.IndexFunc(inventory.Accounts, func(account ManagedAccount) bool { return account.AgentID == plan.AgentID })
	comparable := expected
	comparable.Revision = plan.Revision
	if comparable != plan || plan.Revision > inventory.Revision {
		return ManagedAccount{}, ErrAccountConflict
	}
	if index >= 0 && inventory.Accounts[index].State == AccountActive && inventory.Accounts[index].Fingerprint == plan.Fingerprint {
		if err := m.verifyActive(ctx, inventory.Accounts[index]); err == nil {
			return cloneAccount(inventory.Accounts[index]), nil
		} else if plan.Revision != inventory.Revision {
			return ManagedAccount{}, ErrAccountConflict
		}
	}
	if plan.Revision != inventory.Revision {
		return ManagedAccount{}, ErrAccountConflict
	}
	if index < 0 {
		inventory.Accounts = append(inventory.Accounts, ManagedAccount{AgentID: plan.AgentID, Username: plan.Username})
		index = len(inventory.Accounts) - 1
	}
	account := &inventory.Accounts[index]
	if account.UID == 0 {
		account.UID, account.GID = plan.UID, m.options.IngressGID
		inventory.NextUID = plan.UID + 1
	}
	account.Generation = plan.Generation
	if account.Fingerprint != "" && account.Fingerprint != plan.Fingerprint && !slices.Contains(account.PreviousFingerprints, account.Fingerprint) {
		account.PreviousFingerprints = append(account.PreviousFingerprints, account.Fingerprint)
	}
	account.PublicKey, account.Fingerprint, account.State = plan.PublicKey, plan.Fingerprint, AccountPending
	inventory.GroupReserved = true
	if err := m.saveInventory(&inventory); err != nil {
		return ManagedAccount{}, err
	}
	if err := m.preparePolicy(); err != nil {
		return cloneAccount(inventory.Accounts[index]), err
	}
	if err := m.reconcileAccount(ctx, &inventory, index); err != nil {
		return cloneAccount(inventory.Accounts[index]), err
	}
	return cloneAccount(inventory.Accounts[index]), nil
}

// Reconcile repairs only retained identities. Unmanaged collisions are never
// adopted, and retired records stay retired even if their OS account was removed.
func (m *AccountManager) Reconcile(ctx context.Context, apply bool) (AccountInventory, error) {
	if err := m.guard(ctx, apply); err != nil {
		return AccountInventory{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, err := m.lockInventory()
	if err != nil {
		return AccountInventory{}, err
	}
	defer lock.Close()
	inventory, err := m.loadInventory()
	if err != nil {
		return AccountInventory{}, err
	}
	if len(inventory.Accounts) == 0 {
		return inventory, nil
	}
	if err := m.preparePolicy(); err != nil {
		return inventory, err
	}
	for index := range inventory.Accounts {
		account := &inventory.Accounts[index]
		if account.State == AccountRetiring || account.State == AccountDisabled {
			err = m.retireAccount(ctx, &inventory, index)
		} else {
			account.State = AccountPending
			if err = m.saveInventory(&inventory); err == nil {
				err = m.reconcileAccount(ctx, &inventory, index)
			}
		}
		if err != nil {
			return inventory, err
		}
	}
	return inventory, nil
}

// Disable removes SSH keys, expires/locks the retained OS account, and retains
// every identity reservation. The caller must also revoke application authority;
// this operation is not a promise to terminate already established SSH sessions.
func (m *AccountManager) Disable(ctx context.Context, request AccountDisableRequest, apply bool) (ManagedAccount, error) {
	if err := m.guard(ctx, apply); err != nil {
		return ManagedAccount{}, err
	}
	name, err := accountUsername(request.AgentID, request.Username)
	if err != nil {
		return ManagedAccount{}, err
	}
	if request.Generation < 1 {
		return ManagedAccount{}, errors.New("managed account disable requires a positive identity key generation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, err := m.lockInventory()
	if err != nil {
		return ManagedAccount{}, err
	}
	defer lock.Close()
	inventory, err := m.loadInventory()
	if err != nil {
		return ManagedAccount{}, err
	}
	index := slices.IndexFunc(inventory.Accounts, func(account ManagedAccount) bool { return account.AgentID == request.AgentID })
	if index >= 0 && (name != inventory.Accounts[index].Username || request.Generation < inventory.Accounts[index].Generation) {
		return ManagedAccount{}, ErrAccountConflict
	}
	if index < 0 {
		if len(inventory.Accounts) >= 4096 {
			return ManagedAccount{}, errors.New("managed account inventory limit reached")
		}
		inventory.Accounts = append(inventory.Accounts, ManagedAccount{AgentID: request.AgentID, Username: name, Generation: request.Generation, State: AccountRetiring})
		index = len(inventory.Accounts) - 1
		if err := m.saveInventory(&inventory); err != nil {
			return ManagedAccount{}, err
		}
	} else if inventory.Accounts[index].State != AccountDisabled || inventory.Accounts[index].Generation != request.Generation {
		inventory.Accounts[index].Generation, inventory.Accounts[index].State = request.Generation, AccountRetiring
		if err := m.saveInventory(&inventory); err != nil {
			return ManagedAccount{}, err
		}
	}
	if err := m.preparePolicy(); err != nil {
		return ManagedAccount{}, err
	}
	err = m.retireAccount(ctx, &inventory, index)
	return cloneAccount(inventory.Accounts[index]), err
}

// Inventory returns a validated snapshot. Only active records may be used as an
// ingress UID binding; pending, retiring and disabled records confer no access.
func (m *AccountManager) Inventory() (AccountInventory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadInventory()
}

func (m *AccountManager) reconcileAccount(ctx context.Context, inventory *AccountInventory, index int) (err error) {
	if err := m.checkGroup(ctx, *inventory); err != nil {
		return err
	}
	if _, exists, err := m.system.LookupGroup(ctx, m.options.IngressGroup); err != nil {
		return err
	} else if !exists {
		if err := m.system.CreateGroup(ctx, AccountGroup{Name: m.options.IngressGroup, GID: m.options.IngressGID}); err != nil {
			return err
		}
	}
	if err := m.checkGroup(ctx, *inventory); err != nil {
		return err
	}
	if err := m.ensureServerIngress(ctx); err != nil {
		return err
	}
	if m.native {
		if err := m.prepareNativeIngressDirectories(); err != nil {
			return err
		}
	}
	account := &inventory.Accounts[index]
	desired := m.desired(*account)
	if err := m.prepareHome(desired.Home); err != nil {
		return err
	}
	if _, exists, err := m.observe(ctx, *account); err != nil {
		return err
	} else if !exists {
		if err := m.system.CreateUser(ctx, desired); err != nil {
			return err
		}
	}
	if _, exists, err := m.observe(ctx, *account); err != nil || !exists {
		return errors.Join(ErrAccountConflict, err)
	}
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			err = errors.Join(err, m.writeKey(*account, ""), m.system.DisableUser(cleanupCtx, desired))
		}
	}()
	// Both steps are attempted even if one fails; the retained state is pending.
	if err := errors.Join(m.writeKey(*account, ""), m.system.DisableUser(ctx, desired)); err != nil {
		return err
	}
	if err := m.system.ConfigureUser(ctx, desired); err != nil {
		return err
	}
	if err := m.verifyUser(ctx, *account); err != nil {
		return err
	}
	if err := m.writePolicy(); err != nil {
		return err
	}
	if err := m.writeKey(*account, m.authorizedKey(*account)); err != nil {
		return err
	}
	if err := m.system.ActivateSSH(ctx, m.sshPolicy(account.Username)); err != nil {
		return err
	}
	if err := m.system.EnableUser(ctx, desired); err != nil {
		return err
	}
	account.State = AccountActive
	if err := m.saveInventory(inventory); err != nil {
		account.State = AccountPending
		return err
	}
	return nil
}

func (m *AccountManager) retireAccount(ctx context.Context, inventory *AccountInventory, index int) error {
	account := &inventory.Accounts[index]
	var actual AccountUser
	var exists bool
	var err error
	if account.UID == 0 {
		_, exists, err = m.system.LookupUser(ctx, account.Username)
		if exists {
			err = errors.Join(err, ErrAccountConflict)
		}
	} else {
		actual, exists, err = m.observe(ctx, *account)
	}
	if err != nil {
		return errors.Join(err, m.writeKey(*account, ""))
	}
	if account.State == AccountDisabled && (!exists || actual.Shell == "/usr/sbin/nologin" && actual.GID == m.options.IngressGID && slices.Equal(actual.Groups, []uint32{m.options.IngressGID})) {
		data, err := accountReadPublic(filepath.Join(m.options.PolicyDir, "authorized_keys", account.Username))
		if err == nil && len(data) == 0 {
			return nil
		}
	}
	keyErr := m.writeKey(*account, "")
	if exists {
		err = m.system.DisableUser(ctx, m.desired(*account))
	}
	if err := errors.Join(keyErr, err); err != nil {
		return err
	}
	if account.State != AccountDisabled {
		account.State = AccountDisabled
		return m.saveInventory(inventory)
	}
	return nil
}

func (m *AccountManager) verifyUser(ctx context.Context, account ManagedAccount) error {
	actual, exists, err := m.observe(ctx, account)
	desired := m.desired(account)
	if err != nil || !exists || actual.GID != desired.GID || actual.Home != desired.Home || actual.Shell != desired.Shell ||
		len(actual.Groups) != 1 || actual.Groups[0] != desired.GID {
		return errors.Join(ErrAccountConflict, err)
	}
	return nil
}

func cloneAccount(account ManagedAccount) ManagedAccount {
	account.PreviousFingerprints = slices.Clone(account.PreviousFingerprints)
	return account
}

func (p AccountPlan) String() string {
	return fmt.Sprintf("managed account %s uid=%d for %s (%s)", p.Username, p.UID, p.AgentID, p.Fingerprint)
}
