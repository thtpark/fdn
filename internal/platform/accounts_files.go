package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/secure"
)

func (m *AccountManager) guard(ctx context.Context, apply bool) error {
	if !apply {
		return ErrAccountApplyRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.native && (runtime.GOOS != "linux" || os.Geteuid() != 0) {
		return errors.New("native managed account apply requires explicit Linux root execution")
	}
	if m.native {
		if err := accountTrustedFile(m.options.GatewayBinary, 0, true); err != nil {
			return fmt.Errorf("gateway binary is not root-owned and protected: %w", err)
		}
	}
	return nil
}

func (m *AccountManager) accountDirectory(path string, mode os.FileMode, create, traversable bool) error {
	if err := m.accountAncestors(filepath.Dir(path), uint32(os.Geteuid()), traversable); err != nil {
		return err
	}
	if create {
		if err := os.Mkdir(path, mode); err == nil {
			if err := os.Chmod(path, mode); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("managed account directory must have its exact protected owner and permissions")
	}
	return nil
}

func accountRelativePath(root, path string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("managed account filesystem paths must be absolute and canonical")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("managed account path is outside its logical filesystem root")
	}
	return relative, nil
}

func (m *AccountManager) accountAncestors(path string, owner uint32, traversable bool) error {
	root := m.filesystemRoot
	if m.native || root == "" || root == "/" {
		return accountAncestors(path, owner, traversable)
	}
	if _, err := accountRelativePath(root, path); err != nil {
		return err
	}
	// Keep ownership, symlink and write protection anchored at / even for fixtures.
	if err := accountAncestors(path, owner, false); err != nil {
		return err
	}
	if traversable {
		for current := path; ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode().Perm()&0001 == 0 {
				return errors.New("managed account path has a non-traversable public ancestor")
			}
			if current == root {
				break
			}
		}
	}
	return nil
}

func accountAncestors(path string, owner uint32, traversable bool) error {
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
		if !ok || !info.IsDir() || info.Mode().Perm()&0022 != 0 || (stat.Uid != 0 && stat.Uid != owner) ||
			traversable && info.Mode().Perm()&0001 == 0 {
			return errors.New("managed account path has an unprotected, symlinked or non-traversable ancestor")
		}
	}
	return nil
}

func accountTrustedFile(path string, owner uint32, executable bool) error {
	if err := accountAncestors(filepath.Dir(path), owner, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != owner || info.Mode().Perm()&0022 != 0 ||
		executable && (info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0) {
		return errors.New("managed account file is not an owner-protected regular file")
	}
	return nil
}

func (m *AccountManager) lockInventory() (*secure.Lock, error) {
	if err := m.accountDirectory(m.options.StateDir, 0700, true, false); err != nil {
		return nil, err
	}
	return secure.Acquire(filepath.Join(m.options.StateDir, "inventory.lock"))
}

func (m *AccountManager) loadInventory() (AccountInventory, error) {
	initial := AccountInventory{Version: AccountInventoryVersion, OptionsHash: m.optionsHash(), NextUID: m.options.UIDMin}
	if err := m.accountDirectory(m.options.StateDir, 0700, false, false); errors.Is(err, os.ErrNotExist) {
		return m.emptyInventory(initial)
	} else if err != nil {
		return AccountInventory{}, err
	}
	entries, err := os.ReadDir(m.options.StateDir)
	if err != nil {
		return AccountInventory{}, err
	}
	if len(entries) > 4096 {
		return AccountInventory{}, errors.New("managed account state directory exceeded its bounded inventory file count")
	}
	for _, entry := range entries {
		if entry.Name() == "inventory.json" || entry.Name() == "inventory.lock" {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".fdn-write-") {
			path := filepath.Join(m.options.StateDir, entry.Name())
			if err := accountTrustedFile(path, uint32(os.Geteuid()), false); err != nil {
				return AccountInventory{}, err
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode().Perm()&0077 != 0 || info.Size() > 8<<20 {
				return AccountInventory{}, errors.New("unprotected interrupted inventory write")
			}
			continue
		}
		return AccountInventory{}, errors.New("managed account state directory contains unmanaged files")
	}
	data, err := secure.Read(filepath.Join(m.options.StateDir, "inventory.json"), 8<<20)
	if errors.Is(err, os.ErrNotExist) {
		return m.emptyInventory(initial)
	}
	if err != nil {
		return AccountInventory{}, err
	}
	var inventory AccountInventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return AccountInventory{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return AccountInventory{}, errors.New("managed account inventory has trailing data")
	}
	if err := m.validateInventory(inventory); err != nil {
		return AccountInventory{}, err
	}
	return inventory, nil
}

func (m *AccountManager) emptyInventory(initial AccountInventory) (AccountInventory, error) {
	if _, err := os.Lstat(filepath.Join(m.options.PolicyDir, ".foundation-accounts")); err == nil || !errors.Is(err, os.ErrNotExist) {
		return AccountInventory{}, errors.New("SSH policy exists without its retained provisioning inventory")
	}
	return initial, nil
}

func (m *AccountManager) validateInventory(inventory AccountInventory) error {
	if inventory.Version != AccountInventoryVersion || inventory.OptionsHash != m.optionsHash() ||
		inventory.NextUID < m.options.UIDMin || inventory.NextUID > m.options.UIDMax+1 ||
		len(inventory.Accounts) > 4096 || len(inventory.Accounts) > 0 && inventory.Revision == 0 {
		return errors.New("unsupported or mismatched managed account inventory")
	}
	names := map[string]bool{}
	agents := map[string]bool{}
	uids := map[uint32]bool{}
	fingerprints := map[string]string{}
	for _, account := range inventory.Accounts {
		name, err := accountUsername(account.AgentID, account.Username)
		if err != nil || account.Username == "" || names[name] || agents[account.AgentID] || account.Generation < 1 {
			return ErrAccountConflict
		}
		names[name] = true
		agents[account.AgentID] = true
		if account.UID == 0 {
			if (account.State != AccountDisabled && account.State != AccountRetiring) || account.GID != 0 || account.PublicKey != "" || account.Fingerprint != "" || len(account.PreviousFingerprints) != 0 {
				return ErrAccountConflict
			}
			continue
		}
		if !inventory.GroupReserved || uids[account.UID] || account.UID < m.options.UIDMin || account.UID >= inventory.NextUID || account.UID == m.options.ServerUID || account.GID != m.options.IngressGID {
			return ErrAccountConflict
		}
		uids[account.UID] = true
		canonical, fingerprint, err := CanonicalAccountKey(account.PublicKey)
		if err != nil || canonical != account.PublicKey || fingerprint != account.Fingerprint {
			return errors.New("noncanonical managed account key inventory")
		}
		switch account.State {
		case AccountPending, AccountActive, AccountRetiring, AccountDisabled:
		default:
			return errors.New("unknown managed account lifecycle state")
		}
		for _, fingerprint := range append(slices.Clone(account.PreviousFingerprints), account.Fingerprint) {
			if !validAccountFingerprint(fingerprint) || fingerprints[fingerprint] != "" && fingerprints[fingerprint] != account.AgentID {
				return ErrAccountConflict
			}
			fingerprints[fingerprint] = account.AgentID
		}
	}
	return nil
}

func (m *AccountManager) saveInventory(inventory *AccountInventory) error {
	inventory.Revision++
	if err := m.validateInventory(*inventory); err != nil {
		return err
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("managed account inventory exceeded its retained size limit")
	}
	return secure.Write(filepath.Join(m.options.StateDir, "inventory.json"), data)
}

func (m *AccountManager) preparePolicy() error {
	if err := m.accountDirectory(m.options.PolicyDir, 0755, true, true); err != nil {
		return err
	}
	marker := filepath.Join(m.options.PolicyDir, ".foundation-accounts")
	data, err := accountReadPublic(marker)
	if errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(m.options.PolicyDir)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("refusing to adopt an unmanaged SSH policy directory")
		}
		if err := accountWritePublic(marker, []byte(m.optionsHash()+"\n")); err != nil {
			return err
		}
	} else if err != nil || string(data) != m.optionsHash()+"\n" {
		return errors.New("SSH policy directory belongs to another installation")
	}
	for _, directory := range []string{"authorized_keys", "homes"} {
		if err := m.accountDirectory(filepath.Join(m.options.PolicyDir, directory), 0755, true, true); err != nil {
			return err
		}
	}
	return nil
}

func (m *AccountManager) prepareHome(path string) error {
	return m.accountDirectory(path, 0555, true, true)
}

func accountReadPublic(path string) ([]byte, error) {
	if err := accountTrustedFile(path, uint32(os.Geteuid()), false); err != nil {
		return nil, err
	}
	return secure.ReadPublic(path, 1<<20)
}

func accountWritePublic(path string, data []byte) (err error) {
	if err := accountAncestors(filepath.Dir(path), uint32(os.Geteuid()), false); err != nil {
		return err
	}
	if err := accountTrustedFile(path, uint32(os.Geteuid()), false); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fdna-write-")
	if err != nil {
		return err
	}
	defer func() {
		if e := os.Remove(file.Name()); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	_, writeErr := file.Write(data)
	err = errors.Join(writeErr, file.Chmod(0644), file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(path))
}

func (m *AccountManager) forcedCommand() string {
	return m.options.GatewayBinary + " gateway --input " + filepath.Join(m.options.PolicyDir, "gateway.json")
}

func (m *AccountManager) sshPolicy(name string) AccountSSHPolicy {
	return AccountSSHPolicy{Username: name, ForcedCommand: m.forcedCommand(),
		AuthorizedKeysFile: filepath.Join(m.options.PolicyDir, "authorized_keys", "%u"), MainConfig: m.options.SSHDConfig}
}

func (m *AccountManager) authorizedKey(account ManagedAccount) string {
	return `restrict,no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding,command="` + m.forcedCommand() + `" ` + account.PublicKey
}

func (m *AccountManager) renderSSH() string {
	return "# Foundation managed ingress; include from the main sshd configuration.\nPermitUserEnvironment no\nMatch Group " + m.options.IngressGroup + "\n" +
		"    AuthenticationMethods publickey\n    PubkeyAuthentication yes\n    PasswordAuthentication no\n    KbdInteractiveAuthentication no\n" +
		"    AuthorizedKeysFile " + m.sshPolicy("").AuthorizedKeysFile + "\n    AuthorizedKeysCommand none\n" +
		"    AuthorizedPrincipalsFile none\n    AuthorizedPrincipalsCommand none\n    TrustedUserCAKeys none\n" +
		"    ForceCommand " + m.forcedCommand() + "\n    DisableForwarding yes\n    AllowTcpForwarding no\n" +
		"    AllowStreamLocalForwarding no\n    AllowAgentForwarding no\n    X11Forwarding no\n    PermitTunnel no\n" +
		"    PermitTTY no\n    PermitUserRC no\n    PermitOpen none\n    PermitListen none\n" +
		"    MaxSessions 1\n    AcceptEnv FDN_GATEWAY_UNUSED\nMatch all\n"
}

func (m *AccountManager) gatewayConfig() ([]byte, error) {
	return json.Marshal(ingress.ProxyOptions{APISocket: m.options.APISocket, JoinSocket: m.options.JoinSocket, ServerUID: m.options.ServerUID})
}

func (m *AccountManager) writePolicy() error {
	gateway, err := m.gatewayConfig()
	if err != nil {
		return err
	}
	if err := accountWritePublic(filepath.Join(m.options.PolicyDir, "gateway.json"), gateway); err != nil {
		return err
	}
	return accountWritePublic(filepath.Join(m.options.PolicyDir, "sshd.conf"), []byte(m.renderSSH()))
}

func (m *AccountManager) writeKey(account ManagedAccount, key string) error {
	return accountWritePublic(filepath.Join(m.options.PolicyDir, "authorized_keys", account.Username), []byte(key))
}

func (m *AccountManager) verifyActive(ctx context.Context, account ManagedAccount) error {
	if err := m.verifyUser(ctx, account); err != nil {
		return err
	}
	if err := m.accountDirectory(m.desired(account).Home, 0555, false, true); err != nil {
		return err
	}
	gateway, err := m.gatewayConfig()
	if err != nil {
		return err
	}
	for path, expected := range map[string]string{
		filepath.Join(m.options.PolicyDir, "authorized_keys", account.Username): m.authorizedKey(account),
		filepath.Join(m.options.PolicyDir, "gateway.json"):                      string(gateway),
		filepath.Join(m.options.PolicyDir, "sshd.conf"):                         m.renderSSH(),
		filepath.Join(m.options.PolicyDir, ".foundation-accounts"):              m.optionsHash() + "\n",
	} {
		data, err := accountReadPublic(path)
		if err != nil || string(data) != expected {
			return errors.Join(ErrAccountConflict, err)
		}
	}
	return nil
}

func (m *AccountManager) String() string {
	return fmt.Sprintf("managed SSH ingress group %s gid=%d", m.options.IngressGroup, m.options.IngressGID)
}
