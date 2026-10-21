package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func NewNativeIdentityServiceInstaller(o IdentityServiceOptions, runner process.Runner) (*IdentityServiceInstaller, error) {
	if runner == nil {
		runner = process.Native{}
	}
	host := &nativeIdentityServiceEnvironment{runner: runner, os: o.OS}
	installer, err := NewIdentityServiceInstaller(o, host, runner)
	if err != nil {
		return nil, err
	}
	installer.native = true
	return installer, nil
}

func nativeIdentityInstallGuard(target string) error {
	if target != runtime.GOOS {
		return domain.Fail(domain.Prerequisite, "native identity service installation/status must run on the specified OS")
	}
	if os.Geteuid() != 0 || os.Getuid() != 0 {
		return domain.Fail(domain.Denied, "identity service system installation/status requires explicit administrator execution; prepare/connect/serve remain unprivileged")
	}
	return nil
}

type nativeIdentityServiceEnvironment struct {
	runner process.Runner
	os     string
}

func (h *nativeIdentityServiceEnvironment) query(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, diagnostic := process.NewBuffer(16<<10), process.NewBuffer(8<<10)
	err := h.runner.Run(ctx, process.Spec{Path: path, Args: args, Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}, Stdout: output, Stderr: diagnostic})
	data, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	if errors.Join(err, outputErr, diagnosticErr) != nil {
		return nil, domain.Fail(domain.Prerequisite, "native account lookup failed or the required dedicated account/group is missing; follow service plan without adopting another account")
	}
	return data, nil
}

func (h *nativeIdentityServiceEnvironment) property(ctx context.Context, record, property string) (string, error) {
	data, err := h.query(ctx, "/usr/bin/dscl", ".", "-read", record, property)
	if err != nil {
		return "", err
	}
	value, ok := strings.CutPrefix(string(data), property+":")
	value = strings.TrimSpace(value)
	if !ok || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", domain.Fail(domain.Prerequisite, "native directory account property is missing or ambiguous")
	}
	return value, nil
}

func (h *nativeIdentityServiceEnvironment) LookupUser(ctx context.Context, name string) (AccountUser, bool, error) {
	if !username.MatchString(name) {
		return AccountUser{}, false, domain.Fail(domain.Invalid, "invalid dedicated account name")
	}
	if h.os == "linux" {
		return (&nativeAccountSystem{runner: h.runner}).LookupUser(ctx, name)
	}
	properties := map[string]string{}
	for _, key := range []string{"UniqueID", "PrimaryGroupID", "NFSHomeDirectory", "UserShell", "RealName"} {
		value, err := h.property(ctx, "/Users/"+name, key)
		if err != nil {
			return AccountUser{}, false, err
		}
		properties[key] = value
	}
	uid, uidErr := strconv.ParseUint(properties["UniqueID"], 10, 32)
	gid, gidErr := strconv.ParseUint(properties["PrimaryGroupID"], 10, 32)
	if errors.Join(uidErr, gidErr) != nil {
		return AccountUser{}, false, domain.Fail(domain.Prerequisite, "native directory returned invalid account IDs")
	}
	groups, err := h.query(ctx, "/usr/bin/id", "-G", name)
	if err != nil {
		return AccountUser{}, false, err
	}
	account := AccountUser{Username: name, UID: uint32(uid), GID: uint32(gid), Home: properties["NFSHomeDirectory"], Shell: properties["UserShell"], Marker: properties["RealName"]}
	for _, group := range strings.Fields(string(groups)) {
		id, err := strconv.ParseUint(group, 10, 32)
		if err != nil {
			return AccountUser{}, false, domain.Fail(domain.Prerequisite, "native directory returned invalid supplementary groups")
		}
		account.Groups = append(account.Groups, uint32(id))
	}
	return account, true, nil
}

func (h *nativeIdentityServiceEnvironment) search(ctx context.Context, kind, property string, id uint32) (string, bool, error) {
	data, err := h.query(ctx, "/usr/bin/dscl", ".", "-search", "/"+kind, property, strconv.FormatUint(uint64(id), 10))
	if err != nil {
		return "", false, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", false, nil
	}
	if len(fields) != 2 || !username.MatchString(fields[0]) || fields[1] != strconv.FormatUint(uint64(id), 10) {
		return "", false, domain.Fail(domain.Conflict, "native UID/GID lookup is ambiguous; unrelated accounts are never adopted")
	}
	return fields[0], true, nil
}

func (h *nativeIdentityServiceEnvironment) LookupUID(ctx context.Context, uid uint32) (AccountUser, bool, error) {
	if h.os == "linux" {
		return (&nativeAccountSystem{runner: h.runner}).LookupUID(ctx, uid)
	}
	name, found, err := h.search(ctx, "Users", "UniqueID", uid)
	if err != nil || !found {
		return AccountUser{}, found, err
	}
	return h.LookupUser(ctx, name)
}

func (h *nativeIdentityServiceEnvironment) LookupGroup(ctx context.Context, name string) (AccountGroup, bool, error) {
	if !username.MatchString(name) {
		return AccountGroup{}, false, domain.Fail(domain.Invalid, "invalid dedicated group name")
	}
	if h.os == "linux" {
		return (&nativeAccountSystem{runner: h.runner}).LookupGroup(ctx, name)
	}
	value, err := h.property(ctx, "/Groups/"+name, "PrimaryGroupID")
	if err != nil {
		return AccountGroup{}, false, err
	}
	gid, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return AccountGroup{}, false, domain.Fail(domain.Prerequisite, "native directory returned an invalid group ID")
	}
	return AccountGroup{Name: name, GID: uint32(gid)}, true, nil
}

func (h *nativeIdentityServiceEnvironment) LookupGID(ctx context.Context, gid uint32) (AccountGroup, bool, error) {
	if h.os == "linux" {
		return (&nativeAccountSystem{runner: h.runner}).LookupGID(ctx, gid)
	}
	name, found, err := h.search(ctx, "Groups", "PrimaryGroupID", gid)
	if err != nil || !found {
		return AccountGroup{}, found, err
	}
	return h.LookupGroup(ctx, name)
}

func identityInstallAncestors(path string, owner uint32, binary bool) error {
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return domain.Fail(domain.Prerequisite, "required protected native parent directory is missing")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != owner) {
			return domain.Fail(domain.Denied, "native service paths need protected parent ownership without group/other writes")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !binary || stat.Uid != 0 {
				return domain.Fail(domain.Denied, "native service paths must not traverse untrusted symlinks")
			}
			real, err := filepath.EvalSymlinks(current)
			if err != nil || real == current {
				return domain.Fail(domain.Denied, "immutable binary link cannot be resolved safely")
			}
			if err := identityInstallAncestors(filepath.Join(real, "placeholder"), 0, false); err != nil {
				return err
			}
		} else if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return domain.Fail(domain.Denied, "native service parent must be a real directory without group/other writes")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

func identityInstallDirectory(path string, uid, gid int, mode os.FileMode, create bool) error {
	if err := identityInstallAncestors(path, uint32(uid), false); err != nil {
		return err
	}
	if create {
		if err := os.Mkdir(path, mode); err == nil {
			if err := os.Chown(path, uid, gid); err != nil {
				return err
			}
			if err := os.Chmod(path, mode); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.Fail(domain.Prerequisite, "private identity/runner directory is missing; provision the exact service plan first")
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return domain.Fail(domain.Denied, "existing native directory ownership/mode does not match the plan; it will not be adopted or chmodded")
	}
	return nil
}

func identityInstallRead(path string, uid uint32, private, binary bool) ([]byte, error) {
	if err := identityInstallAncestors(path, uid, binary); err != nil {
		return nil, err
	}
	if binary {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || uid != 0 || stat.Uid != 0 {
				return nil, domain.Fail(domain.Denied, "binary activation links must be root-owned")
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil || real == path {
				return nil, domain.Fail(domain.Denied, "binary activation link cannot be resolved")
			}
			return identityInstallRead(real, 0, false, true)
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.Fail(domain.Prerequisite, "required native configuration/binary is missing; prepare it under its dedicated account first")
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || info.Mode().Perm()&0022 != 0 ||
		private && info.Mode().Perm()&0077 != 0 || binary && (info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0) {
		return nil, domain.Fail(domain.Denied, "native files require exact account ownership and protected regular-file permissions")
	}
	if binary {
		return nil, nil
	}
	if info.Size() > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native configuration exceeds its size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "native configuration exceeds its size limit")
	}
	return data, err
}

func (h *nativeIdentityServiceEnvironment) ValidateLayout(o IdentityServiceOptions) error {
	for _, directory := range []IdentityDirectoryPlan{{o.IdentityHome, o.IdentityUID, o.IdentityGID, 0700}, {o.WorkerHome, o.WorkerUID, o.WorkerGID, 0700},
		{o.StateDir, o.IdentityUID, o.IdentityGID, 0700}, {o.WorkerStateDir, o.WorkerUID, o.WorkerGID, 0700}} {
		if err := identityInstallDirectory(directory.Path, directory.UID, directory.GID, os.FileMode(directory.Mode), false); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(o.SocketDir); err == nil {
		if err := identityInstallDirectory(o.SocketDir, o.IdentityUID, o.WorkerGID, 0710, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := identityInstallAncestors(o.SocketDir, 0, false); err != nil {
		return err
	}
	for _, binary := range []string{o.IdentityBinary, o.WorkerBinary} {
		if _, err := identityInstallRead(binary, 0, false, true); err != nil {
			return err
		}
	}
	data, err := identityInstallRead(o.IdentityConfig, uint32(o.IdentityUID), true, false)
	if err != nil {
		return err
	}
	var identity config.IdentityService
	if err := config.Decode(data, &identity); err != nil {
		return err
	}
	data, err = identityInstallRead(o.WorkerConfig, uint32(o.WorkerUID), true, false)
	if err != nil {
		return err
	}
	var worker config.Worker
	if err := config.Decode(data, &worker); err != nil {
		return err
	}
	return validateIdentityServiceConfiguration(o, identity, worker)
}

func validateIdentityServiceConfiguration(o IdentityServiceOptions, identity config.IdentityService, worker config.Worker) error {
	if identity.StateDir != o.StateDir || filepath.Dir(identity.Socket) != o.SocketDir || !filepath.IsAbs(identity.Socket) || filepath.Clean(identity.Socket) != identity.Socket ||
		len(identity.Socket) > 103 || strings.ContainsAny(identity.Socket, "\x00\r\n") || identity.WorkerUID != o.WorkerUID || identity.WorkerGID != o.WorkerGID {
		return domain.Fail(domain.Conflict, "private identity configuration does not match planned state/IPC and runner UID/GID")
	}
	if worker.StateDir != o.WorkerStateDir || worker.IdentitySocket != identity.Socket || worker.SSH != nil ||
		!identityPathWithin(o.WorkerStateDir, worker.CredentialFile) || !identityPathWithin(o.WorkerHome, worker.CAFile) ||
		identityPathWithin(o.IdentityHome, worker.CredentialFile) || identityPathWithin(o.IdentityHome, worker.CAFile) {
		return domain.Fail(domain.Denied, "runner configuration must use only the planned public IPC endpoint, never identity-private state or SSH keys")
	}
	return worker.Validate()
}

func identityInstallUnitParents(base, target string, uid, gid int) error {
	if !identityPathWithin(base, target) {
		return domain.Fail(domain.Denied, "native definition is outside its managed directory")
	}
	rel, _ := filepath.Rel(base, target)
	current := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := identityInstallAncestors(current, uint32(uid), false); err != nil {
			return err
		}
		if err := os.Mkdir(current, 0755); err == nil {
			if err := os.Chown(current, uid, gid); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || int(stat.Uid) != uid || info.Mode().Perm()&0022 != 0 {
			return domain.Fail(domain.Denied, "native manager definition parent is not owned/protected by the planned account")
		}
	}
	return nil
}

func identityInstallDefinition(definition Definition, uid, gid int) (err error) {
	if err := identityInstallAncestors(definition.Path, uint32(uid), false); err != nil {
		return err
	}
	if _, err := os.Lstat(definition.Path); err == nil {
		old, err := identityInstallRead(definition.Path, uint32(uid), false, false)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, []byte(definition.Content)) {
			return domain.Fail(domain.Conflict, "existing service definition differs from this exact plan; it was retained for explicit administrator review")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(definition.Path), ".fdn-identity-unit-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := file.WriteString(definition.Content)
	ownerErr := file.Chown(uid, gid)
	modeErr := file.Chmod(0644)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, ownerErr, modeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), definition.Path); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(definition.Path))
}

func (h *nativeIdentityServiceEnvironment) WriteDefinitions(o IdentityServiceOptions, plan IdentityServicePlan) error {
	if err := identityInstallDirectory(o.SocketDir, o.IdentityUID, o.WorkerGID, 0710, true); err != nil {
		return err
	}
	if err := identityInstallAncestors(o.IdentityDefinition, 0, false); err != nil {
		return err
	}
	if err := identityInstallUnitParents(o.WorkerHome, filepath.Dir(o.WorkerDefinition), o.WorkerUID, o.WorkerGID); err != nil {
		return err
	}
	if err := identityInstallDefinition(plan.Identity, 0, 0); err != nil {
		return err
	}
	return identityInstallDefinition(plan.Worker, o.WorkerUID, o.WorkerGID)
}

func (h *nativeIdentityServiceEnvironment) RunnerDomain(o IdentityServiceOptions) error {
	if o.OS != "linux" {
		return nil
	}
	directory := "/run/user/" + strconv.Itoa(o.WorkerUID)
	if err := identityInstallDirectory(directory, o.WorkerUID, o.WorkerGID, 0700, false); err != nil {
		return domain.Fail(domain.Prerequisite, "runner user manager/runtime directory is unavailable or not owned by its UID")
	}
	info, err := os.Lstat(filepath.Join(directory, "bus"))
	if err != nil {
		return domain.Fail(domain.Prerequisite, "runner user bus is unavailable; persistent user manager setup remains pending")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || int(stat.Uid) != o.WorkerUID {
		return domain.Fail(domain.Denied, "runner user bus must be a real socket owned by the planned worker UID")
	}
	return nil
}
