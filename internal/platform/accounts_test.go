package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/process"
	"golang.org/x/crypto/ssh"
)

const accountTestAgent = "agent_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const accountTestOther = "agent_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func platformTestRoot(t *testing.T, prefix string) string {
	t.Helper()
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(account.HomeDir) {
		t.Fatal("OS account home must be absolute")
	}
	home, err := filepath.EvalSymlinks(account.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := accountAncestors(home, uint32(os.Geteuid()), false); err != nil {
		t.Fatal("OS account home cannot hold protected test fixtures", err)
	}
	root, err := os.MkdirTemp(home, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func accountTestRoot(t *testing.T) string {
	t.Helper()
	return platformTestRoot(t, ".a-")
}

func TestPlatformFixtureRootsUseAccountHome(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(account.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(accountTestRoot(t), "unsafe")
	checkout := filepath.Join(unsafe, "checkout", "internal", "platform")
	if err := os.MkdirAll(checkout, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	t.Chdir(checkout)
	t.Setenv("HOME", unsafe)
	for _, fixture := range []struct {
		name string
		root func(*testing.T) string
	}{
		{"accounts", accountTestRoot},
		{"identity-install", identityInstallTestDir},
	} {
		var root string
		t.Run(fixture.name, func(t *testing.T) {
			root = fixture.root(t)
			if filepath.Dir(root) != home {
				t.Error("fixture root must use the OS account home, not HOME or the checkout")
			}
			info, err := os.Lstat(root)
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("incorrect fixture root permissions", info, err)
			}
			if err := accountAncestors(root, uint32(os.Geteuid()), false); err != nil {
				t.Error("fixture inherited unsafe ancestors", err)
			}
		})
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fixture root was not cleaned up", err)
		}
	}
}

func accountTestKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(key))
}

type recordingAccounts struct {
	mu         sync.Mutex
	users      map[string]AccountUser
	groups     map[string]AccountGroup
	disabled   map[string]bool
	mutations  []string
	lookups    int
	fail       string
	lostCreate bool
	before     func(string) error
}

func (r *recordingAccounts) LookupUser(_ context.Context, name string) (AccountUser, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	user, ok := r.users[name]
	user.Groups = slices.Clone(user.Groups)
	return user, ok, nil
}
func (r *recordingAccounts) LookupUID(_ context.Context, uid uint32) (AccountUser, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	for _, user := range r.users {
		if user.UID == uid {
			user.Groups = slices.Clone(user.Groups)
			return user, true, nil
		}
	}
	return AccountUser{}, false, nil
}
func (r *recordingAccounts) LookupGroup(_ context.Context, name string) (AccountGroup, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	group, ok := r.groups[name]
	return group, ok, nil
}
func (r *recordingAccounts) LookupGID(_ context.Context, gid uint32) (AccountGroup, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	for _, group := range r.groups {
		if group.GID == gid {
			return group, true, nil
		}
	}
	return AccountGroup{}, false, nil
}
func (r *recordingAccounts) record(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mutations = append(r.mutations, operation)
	if r.before != nil {
		if err := r.before(operation); err != nil {
			return err
		}
	}
	if r.fail == operation {
		return fmt.Errorf("recorded %s failure", operation)
	}
	return nil
}
func (r *recordingAccounts) CreateGroup(ctx context.Context, group AccountGroup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "create-group"); err != nil {
		return err
	}
	r.groups[group.Name] = group
	return nil
}
func (r *recordingAccounts) GrantServerIngress(ctx context.Context, user AccountUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "grant-server-ingress"); err != nil {
		return err
	}
	user.Groups = append(user.Groups, 210000)
	r.users[user.Username] = user
	group := r.groups["fdn_ingress"]
	group.Members = append(group.Members, user.Username)
	r.groups[group.Name] = group
	return nil
}

func (r *recordingAccounts) CreateUser(ctx context.Context, user AccountUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "create-user"); err != nil {
		return err
	}
	r.users[user.Username] = user
	r.disabled[user.Username] = true
	if r.lostCreate {
		return errors.New("recorded lost create acknowledgement")
	}
	return nil
}
func (r *recordingAccounts) ConfigureUser(ctx context.Context, user AccountUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "configure-user"); err != nil {
		return err
	}
	r.users[user.Username] = user
	return nil
}
func (r *recordingAccounts) DisableUser(ctx context.Context, user AccountUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "disable-user"); err != nil {
		return err
	}
	actual := r.users[user.Username]
	actual.Shell = "/usr/sbin/nologin"
	actual.Groups = []uint32{actual.GID}
	r.users[user.Username] = actual
	r.disabled[user.Username] = true
	return nil
}
func (r *recordingAccounts) EnableUser(ctx context.Context, user AccountUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.record(ctx, "enable-user"); err != nil {
		return err
	}
	r.users[user.Username] = user
	r.disabled[user.Username] = false
	return nil
}
func (r *recordingAccounts) ActivateSSH(ctx context.Context, _ AccountSSHPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.record(ctx, "activate-ssh")
}

func accountFixture(t *testing.T) (*AccountManager, *recordingAccounts) {
	t.Helper()
	root := filepath.Join(accountTestRoot(t), "root")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	options := AccountOptions{StateDir: filepath.Join(root, "s"), PolicyDir: filepath.Join(root, "p"), GatewayBinary: "/opt/fdn/fdn-service",
		APISocket: filepath.Join(root, "api"), JoinSocket: filepath.Join(root, "join"), ServerUID: 1200, ServerGroup: "fdn", ServerGID: 1200,
		IngressGroup: "fdn_ingress", IngressGID: 210000, UIDMin: 220000, UIDMax: 220010}
	system := &recordingAccounts{users: map[string]AccountUser{"fdn": {Username: "fdn", UID: 1200, GID: 1200, Groups: []uint32{1200}}}, groups: map[string]AccountGroup{"fdn": {Name: "fdn", GID: 1200}}, disabled: map[string]bool{}}
	manager, err := newAccountManager(options, system, root)
	if err != nil {
		t.Fatal(err)
	}
	return manager, system
}

func applyTestAccount(t *testing.T, manager *AccountManager, agent, key string) ManagedAccount {
	t.Helper()
	generation := int64(1)
	inventory, inventoryErr := manager.Inventory()
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	_, fingerprint, _ := CanonicalAccountKey(key)
	for _, account := range inventory.Accounts {
		if account.AgentID == agent {
			generation = account.Generation
			if account.Fingerprint != fingerprint {
				generation++
			}
		}
	}
	plan, err := manager.Plan(context.Background(), AccountRequest{AgentID: agent, PublicKey: key, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	account, err := manager.Apply(context.Background(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestManagedAccountPlanApplyAndRetainedReplay(t *testing.T) {
	manager, system := accountFixture(t)
	key := accountTestKey(t)
	plan, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: strings.TrimSpace(key) + " comment\n", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Username != "fdna_aaaaaaaaaaaaaaaaaaaa" || plan.UID != manager.options.UIDMin || plan.PublicKey != key {
		t.Fatal(plan)
	}
	if _, err := os.Lstat(manager.options.StateDir); !errors.Is(err, os.ErrNotExist) || len(system.mutations) != 0 {
		t.Fatal("plan changed state", err)
	}
	if _, err := manager.Apply(context.Background(), plan, false); !errors.Is(err, ErrAccountApplyRequired) {
		t.Fatal(err)
	}
	system.before = func(operation string) error {
		inventory, err := manager.loadInventory()
		if err != nil || len(inventory.Accounts) != 1 || inventory.Accounts[0].State != AccountPending {
			return fmt.Errorf("OS %s preceded retained pending intent: %v", operation, err)
		}
		return nil
	}
	account, err := manager.Apply(context.Background(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != AccountActive || account.UID != plan.UID || system.disabled[account.Username] {
		t.Fatal(account)
	}
	user := system.users[account.Username]
	if user.GID == manager.options.ServerGID || !slices.Equal(user.Groups, []uint32{manager.options.IngressGID}) {
		t.Fatal("account joined DB group", user)
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{manager.options.StateDir, 0700}, {filepath.Join(manager.options.StateDir, "inventory.json"), 0600},
		{manager.options.PolicyDir, 0755}, {user.Home, 0555}, {filepath.Join(manager.options.PolicyDir, "authorized_keys", account.Username), 0644},
	} {
		info, err := os.Lstat(item.path)
		if err != nil || info.Mode().Perm() != item.mode {
			t.Fatal("protected managed path", item.path, info, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(manager.options.PolicyDir, "authorized_keys", account.Username))
	if err != nil || !strings.Contains(string(data), "restrict,no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding,command=\"") || !strings.HasSuffix(string(data), key) {
		t.Fatal("unsafe authorized key", err)
	}
	if slices.Index(system.mutations, "activate-ssh") > slices.Index(system.mutations, "enable-user") {
		t.Fatal("account enabled before SSH policy", system.mutations)
	}
	count := len(system.mutations)
	system.before = nil
	reopened, err := newAccountManager(manager.options, system, manager.filesystemRoot)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.Apply(context.Background(), plan, true)
	if err != nil || replay.UID != account.UID || len(system.mutations) != count {
		t.Fatal("exact completed replay was not idempotent", replay, err)
	}
}

func TestManagedAccountRetirementReservationsAndExplicitRecovery(t *testing.T) {
	manager, system := accountFixture(t)
	key := accountTestKey(t)
	account := applyTestAccount(t, manager, accountTestAgent, key)
	disabled, err := manager.Disable(context.Background(), AccountDisableRequest{AgentID: accountTestAgent, Generation: account.Generation + 1}, true)
	if err != nil || disabled.State != AccountDisabled || !system.disabled[account.Username] {
		t.Fatal(disabled, err)
	}
	data, err := os.ReadFile(filepath.Join(manager.options.PolicyDir, "authorized_keys", account.Username))
	if err != nil || len(data) != 0 {
		t.Fatal("retirement left authorized keys", err)
	}
	if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: disabled.Generation + 1}); !errors.Is(err, ErrAccountRetired) {
		t.Fatal("implicit resurrection", err)
	}
	delete(system.users, account.Username)
	if _, err := manager.Reconcile(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, exists := system.users[account.Username]; exists {
		t.Fatal("recreated retired account")
	}
	if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestOther, PublicKey: key, Generation: 1}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("retired key reused", err)
	}
	other := applyTestAccount(t, manager, accountTestOther, accountTestKey(t))
	if other.UID <= account.UID {
		t.Fatal("retired UID recycled", other)
	}
	collidingID := "agent_" + strings.Repeat("a", 20) + strings.Repeat("b", 12)
	if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: collidingID, PublicKey: accountTestKey(t), Generation: 1}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("username collision adopted", err)
	}
	recovery, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: disabled.Generation + 1, Reactivate: true})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Apply(context.Background(), recovery, true)
	if err != nil || recovered.UID != account.UID || recovered.Username != account.Username || recovered.State != AccountActive {
		t.Fatal("same-identity recovery changed reservation", recovered, err)
	}
	if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: key, Generation: recovered.Generation + 1}); !errors.Is(err, ErrAccountRetired) {
		t.Fatal("compromised old key restored", err)
	}
}

func TestManagedAccountInterruptedProvisionAndFailedPolicyStayClosed(t *testing.T) {
	for _, failure := range []string{"lost-create", "activate-ssh", "enable-user"} {
		t.Run(failure, func(t *testing.T) {
			manager, system := accountFixture(t)
			if failure == "lost-create" {
				system.lostCreate = true
			} else {
				system.fail = failure
			}
			plan, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Apply(context.Background(), plan, true); err == nil {
				t.Fatal("fixture failure disappeared")
			}
			inventory, err := manager.Inventory()
			if err != nil || len(inventory.Accounts) != 1 || inventory.Accounts[0].State != AccountPending || !system.disabled[plan.Username] {
				t.Fatal("partial apply was active", inventory, err)
			}
			if failure != "lost-create" {
				data, err := os.ReadFile(filepath.Join(manager.options.PolicyDir, "authorized_keys", plan.Username))
				if err != nil || len(data) != 0 {
					t.Fatal("failed policy left live key", err)
				}
			}
			system.fail = ""
			system.lostCreate = false
			reopened, err := newAccountManager(manager.options, system, manager.filesystemRoot)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err = reopened.Reconcile(context.Background(), true)
			if err != nil || inventory.Accounts[0].State != AccountActive || inventory.Accounts[0].UID != plan.UID {
				t.Fatal("retained recovery failed", inventory, err)
			}
			if count := len(slices.DeleteFunc(slices.Clone(system.mutations), func(s string) bool { return s != "create-user" })); count != 1 {
				t.Fatal("recovery duplicated user creation", system.mutations)
			}
		})
	}
}

func TestManagedAccountUnmanagedCollisionsAndPlanTampering(t *testing.T) {
	for _, collision := range []string{"name", "group-name", "group-gid", "server-gid"} {
		t.Run(collision, func(t *testing.T) {
			manager, system := accountFixture(t)
			name, _ := AccountUsername(accountTestAgent)
			switch collision {
			case "name":
				system.users[name] = AccountUser{Username: name, UID: 55555}
			case "group-name":
				system.groups[manager.options.IngressGroup] = AccountGroup{Name: manager.options.IngressGroup, GID: 55555}
			case "group-gid":
				system.groups["unmanaged"] = AccountGroup{Name: "unmanaged", GID: manager.options.IngressGID}
			default:
				system.groups[manager.options.ServerGroup] = AccountGroup{Name: manager.options.ServerGroup, GID: 55555}
			}
			if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: 1}); err == nil || len(system.mutations) != 0 {
				t.Fatal("adopted unmanaged identity", err)
			}
		})
	}
	manager, system := accountFixture(t)
	system.users["existing"] = AccountUser{Username: "existing", UID: manager.options.UIDMin}
	plan, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: 1})
	if err != nil || plan.UID != manager.options.UIDMin+1 {
		t.Fatal("occupied UID not skipped", plan, err)
	}
	for _, modify := range []func(*AccountPlan){func(p *AccountPlan) { p.UID++ }, func(p *AccountPlan) { p.Username = "root" }, func(p *AccountPlan) { p.Fingerprint = "SHA256:invalid" }, func(p *AccountPlan) { p.Revision++ }, func(p *AccountPlan) { p.PublicKey = accountTestKey(t) }} {
		invalid := plan
		modify(&invalid)
		if _, err := manager.Apply(context.Background(), invalid, true); err == nil || len(system.mutations) != 0 {
			t.Fatal("tampered plan reached OS", err)
		}
	}
	system.users["racer"] = AccountUser{Username: "racer", UID: plan.UID}
	if _, err := manager.Apply(context.Background(), plan, true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("allocation race not fenced", err)
	}
}

func TestManagedAccountKeyRotationAndGroupDriftReconciliation(t *testing.T) {
	manager, system := accountFixture(t)
	oldKey := accountTestKey(t)
	account := applyTestAccount(t, manager, accountTestAgent, oldKey)
	rotated := applyTestAccount(t, manager, accountTestAgent, accountTestKey(t))
	if rotated.UID != account.UID || !slices.Contains(rotated.PreviousFingerprints, account.Fingerprint) {
		t.Fatal("rotation lost reservation", rotated)
	}
	if _, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestOther, PublicKey: oldKey, Generation: 1}); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("rotated key reassigned", err)
	}
	user := system.users[account.Username]
	user.Groups = append(user.Groups, manager.options.ServerGID)
	user.Shell = "/bin/bash"
	system.users[user.Username] = user
	if _, err := manager.Reconcile(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(system.users[user.Username].Groups, []uint32{manager.options.IngressGID}) || system.users[user.Username].Shell != "/bin/sh" {
		t.Fatal("unsafe group/shell drift retained")
	}
	group := system.groups[manager.options.IngressGroup]
	group.Members = []string{"unmanaged"}
	system.groups[group.Name] = group
	if _, err := manager.Reconcile(context.Background(), true); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("unmanaged group member accepted", err)
	}
}

func TestManagedAccountInventoryAndPolicyPathProtection(t *testing.T) {
	manager, system := accountFixture(t)
	account := applyTestAccount(t, manager, accountTestAgent, accountTestKey(t))
	path := filepath.Join(manager.options.StateDir, "inventory.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AccountInventory){func(i *AccountInventory) { i.Version++ }, func(i *AccountInventory) { i.Accounts[0].UID = i.NextUID }, func(i *AccountInventory) { i.Accounts[0].State = "unknown" }, func(i *AccountInventory) {
		i.Accounts[0].PublicKey = strings.TrimSpace(i.Accounts[0].PublicKey) + " comment\n"
	}} {
		var inventory AccountInventory
		if err := json.Unmarshal(original, &inventory); err != nil {
			t.Fatal(err)
		}
		change(&inventory)
		data, _ := json.Marshal(inventory)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Inventory(); err == nil {
			t.Fatal("accepted incompatible inventory")
		}
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(manager.options.PolicyDir, "protected")
	if err := os.WriteFile(protected, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(manager.options.PolicyDir, "authorized_keys", account.Username)
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, keyPath); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Plan(context.Background(), AccountRequest{AgentID: accountTestAgent, PublicKey: accountTestKey(t), Generation: account.Generation + 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), plan, true); err == nil {
		t.Fatal("replaced symlink authorized keys")
	}
	if data, err := os.ReadFile(protected); err != nil || string(data) != "preserve" || !system.disabled[account.Username] {
		t.Fatal("followed symlink or left account enabled", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inventory(); err == nil {
		t.Fatal("silently reset lost provisioning inventory")
	}
}

func TestManagedAccountCanonicalPublicKeys(t *testing.T) {
	key := accountTestKey(t)
	canonical, fingerprint, err := CanonicalAccountKey(strings.TrimSpace(key) + " comment\n")
	if err != nil || canonical != key || !validAccountFingerprint(fingerprint) {
		t.Fatal(canonical, fingerprint, err)
	}
	for _, invalid := range []string{"", "command=\"bad\" " + key, key + key, key + "\x00", key + "\r", strings.Repeat("x", (16<<10)+1)} {
		if _, _, err := CanonicalAccountKey(invalid); err == nil {
			t.Fatal("accepted invalid SSH key")
		}
	}
	if _, err := AccountUsername("founder"); err == nil {
		t.Fatal("display name became account identity")
	}
}

type accountRecordingRunner struct {
	calls []process.Spec
	run   func(process.Spec) error
}

func (r *accountRecordingRunner) Run(_ context.Context, s process.Spec) error {
	r.calls = append(r.calls, s)
	if r.run != nil {
		return r.run(s)
	}
	return nil
}

type accountExitCode int

func (e accountExitCode) Error() string { return "recorded exit" }
func (e accountExitCode) ExitCode() int { return int(e) }

func TestManagedAccountNativeCommandsAreNarrowAndNeverRunHostTools(t *testing.T) {
	manager, _ := accountFixture(t)
	runner := &accountRecordingRunner{}
	native := &nativeAccountSystem{options: manager.options, runner: runner}
	name := "agent1"
	user := manager.desired(ManagedAccount{AgentID: accountTestAgent, Username: name, UID: manager.options.UIDMin})
	for _, call := range []func() error{
		func() error {
			return native.CreateGroup(context.Background(), AccountGroup{Name: manager.options.IngressGroup, GID: manager.options.IngressGID})
		},
		func() error { return native.CreateUser(context.Background(), user) }, func() error { return native.DisableUser(context.Background(), user) },
		func() error { return native.ConfigureUser(context.Background(), user) }, func() error { return native.EnableUser(context.Background(), user) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range runner.calls {
		if !filepath.IsAbs(call.Path) || strings.Contains(strings.Join(call.Args, " "), "--non-unique") || !slices.Equal(call.Env, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}) || call.Stdin != nil {
			t.Fatal("unsafe native command", call.Path, call.Args)
		}
	}
	count := len(runner.calls)
	user.Username = "root"
	if err := native.EnableUser(context.Background(), user); !errors.Is(err, ErrAccountConflict) || len(runner.calls) != count {
		t.Fatal("arbitrary root API", err)
	}
	production, err := NewNativeAccountManager(manager.options, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := production.Apply(context.Background(), AccountPlan{}, false); !errors.Is(err, ErrAccountApplyRequired) {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		if _, err := production.Reconcile(context.Background(), true); err == nil || len(runner.calls) != count {
			t.Fatal("native root/OS guard missing", err)
		}
	}
	runner.run = func(s process.Spec) error {
		if s.Path == "/usr/bin/getent" {
			return accountExitCode(2)
		}
		return errors.New("unexpected command")
	}
	if _, exists, err := native.LookupUser(context.Background(), name); err != nil || exists {
		t.Fatal("not-found lookup", exists, err)
	}
}

func TestManagedAccountEffectiveSSHDPolicyAndNativeParser(t *testing.T) {
	manager, _ := accountFixture(t)
	policy := manager.sshPolicy("fdna_aaaaaaaaaaaaaaaaaaaa")
	var effective strings.Builder
	for _, line := range strings.Split(manager.renderSSH(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Match ") {
			continue
		}
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatal(line)
		}
		fmt.Fprintf(&effective, "%s %s\n", strings.ToLower(name), value)
	}
	if err := checkEffectiveAccountSSH([]byte(effective.String()), policy); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{strings.Replace(effective.String(), "permittty no", "permittty yes", 1), effective.String() + "acceptenv BASH_ENV\n", strings.Replace(effective.String(), "forcecommand "+policy.ForcedCommand, "forcecommand none", 1)} {
		if err := checkEffectiveAccountSSH([]byte(unsafe), policy); err == nil {
			t.Fatal("unsafe effective sshd policy accepted")
		}
	}
	parser := "/usr/sbin/sshd"
	if _, err := os.Stat(parser); err != nil {
		t.Skip("native sshd parser unavailable")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(manager.options.StateDir)
	keyPath := filepath.Join(root, "host-key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "sshd-config")
	content := "HostKey " + keyPath + "\n" + strings.Replace(manager.renderSSH(), "Match Group "+manager.options.IngressGroup, "Match User *", 1)
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(parser, "-T", "-f", configPath, "-C", "user="+policy.Username+",host=localhost,addr=127.0.0.1")
	output, err := command.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "Missing privilege separation directory") {
			t.Skip("native parser prerequisite absent")
		}
		t.Fatalf("native sshd parser rejected managed config: %v\n%s", err, output)
	}
	if err := checkEffectiveAccountSSH(output, policy); err != nil {
		t.Fatalf("native sshd effective policy differs: %v\n%s", err, output)
	}
}
