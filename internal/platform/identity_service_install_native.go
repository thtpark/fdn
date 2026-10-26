package platform

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func NewNativeIdentityServiceInstaller(o IdentityServiceOptions, runner process.Runner) (*IdentityServiceInstaller, error) {
	var err error
	o, err = ResolveIdentityServiceUser(o)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		runner = process.Native{}
	}
	installer, err := NewIdentityServiceInstaller(o, &nativeIdentityServiceEnvironment{}, runner)
	if err != nil {
		return nil, err
	}
	installer.native = true
	return installer, nil
}

func nativeIdentityInstallGuard(target string) error {
	if target != runtime.GOOS {
		return domain.Fail(domain.Prerequisite, "native worker service installation/status must run on the specified OS")
	}
	if os.Geteuid() == 0 || os.Getuid() != os.Geteuid() {
		return domain.Fail(domain.Denied, "run worker service installation/status as the existing installing user, without sudo or a privileged UID")
	}
	return nil
}

func currentIdentityServiceUser() (AccountUser, error) {
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return AccountUser{}, domain.Fail(domain.Prerequisite, "cannot resolve the installing user's OS account and HOME")
	}
	if account.Username == "" || !filepath.IsAbs(account.HomeDir) || filepath.Clean(account.HomeDir) != account.HomeDir {
		return AccountUser{}, domain.Fail(domain.Prerequisite, "installing user's OS account needs its existing canonical HOME")
	}
	return AccountUser{Username: account.Username, UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()), Home: account.HomeDir}, nil
}

type nativeIdentityServiceEnvironment struct{}

func (*nativeIdentityServiceEnvironment) CurrentUser(context.Context) (AccountUser, error) {
	return currentIdentityServiceUser()
}

func identityInstallAncestors(path string, owner uint32, binary bool) error {
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return domain.Fail(domain.Prerequisite, "required protected native parent directory is missing")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != owner) {
			return domain.Fail(domain.Denied, "native service paths need protected installing-user or root ownership")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !binary {
				return domain.Fail(domain.Denied, "native service paths must not traverse symlinks")
			}
			real, err := filepath.EvalSymlinks(current)
			if err != nil || real == current {
				return domain.Fail(domain.Denied, "binary activation link cannot be resolved safely")
			}
			if err := identityInstallAncestors(filepath.Join(real, "placeholder"), owner, false); err != nil {
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
		return domain.Fail(domain.Prerequisite, "required private worker directory is missing; prepare it as the installing user")
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return domain.Fail(domain.Denied, "existing native directory ownership/mode does not match the installing user; it will not be adopted or chmodded")
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
			if !ok || (stat.Uid != 0 && stat.Uid != uid) {
				return nil, domain.Fail(domain.Denied, "binary activation links must be owned by root or the installing user")
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil || real == path {
				return nil, domain.Fail(domain.Denied, "binary activation link cannot be resolved")
			}
			return identityInstallRead(real, uid, false, true)
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.Fail(domain.Prerequisite, "required native configuration/binary is missing; prepare it as the installing user")
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
	if !ok || !info.Mode().IsRegular() || (stat.Uid != uid && !(binary && stat.Uid == 0)) || info.Mode().Perm()&0022 != 0 ||
		private && info.Mode().Perm()&0077 != 0 || binary && (info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0) {
		return nil, domain.Fail(domain.Denied, "native files require installing-user ownership, private configuration and protected executable permissions")
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

func (*nativeIdentityServiceEnvironment) ValidateLayout(o IdentityServiceOptions) error {
	if err := identityInstallAncestors(filepath.Join(o.Home, "placeholder"), uint32(o.UID), false); err != nil {
		return err
	}
	for _, path := range []string{o.StateDir, o.WorkerStateDir} {
		if err := identityInstallDirectory(path, o.UID, o.GID, 0700, false); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(o.SocketDir); err == nil {
		if err := identityInstallDirectory(o.SocketDir, o.UID, o.GID, 0700, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := identityInstallAncestors(o.SocketDir, uint32(o.UID), false); err != nil {
		return err
	}
	for _, binary := range []string{o.IdentityBinary, o.WorkerBinary} {
		if _, err := identityInstallRead(binary, uint32(o.UID), false, true); err != nil {
			return err
		}
	}
	data, err := identityInstallRead(o.IdentityConfig, uint32(o.UID), true, false)
	if err != nil {
		return err
	}
	var identity config.IdentityService
	if err := config.Decode(data, &identity); err != nil {
		return err
	}
	data, err = identityInstallRead(o.WorkerConfig, uint32(o.UID), true, false)
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
		len(identity.Socket) > 103 || strings.ContainsAny(identity.Socket, "\x00\r\n") ||
		(identity.WorkerUID != 0 && identity.WorkerUID != o.UID) || (identity.WorkerGID != 0 && identity.WorkerGID != o.GID) {
		return domain.Fail(domain.Conflict, "identity configuration must match the installing user's state/IPC; legacy foreign worker UID/GID requires explicit migration")
	}
	if worker.StateDir != o.WorkerStateDir || worker.IdentitySocket != identity.Socket || worker.SSH != nil ||
		!identityPathWithin(o.WorkerStateDir, worker.CredentialFile) || !identityPathWithin(o.Home, worker.CAFile) ||
		identityPathWithin(o.StateDir, worker.CredentialFile) || identityPathWithin(o.StateDir, worker.CAFile) {
		return domain.Fail(domain.Denied, "runner configuration must retain its public IPC binding and distinct runner files, not mix identity-private files into credential/CA paths")
	}
	return worker.ValidateForOwner(uint32(o.UID))
}

func identityInstallUnitParents(base, target string, uid, gid int) error {
	if !identityPathWithin(base, target) {
		return domain.Fail(domain.Denied, "native definition is outside the installing user's HOME")
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
			return domain.Fail(domain.Denied, "native manager definition parent is not owned/protected by the installing user")
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
			return domain.Fail(domain.Conflict, "existing service definition differs from this exact plan; retain it for explicit user review")
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

func (*nativeIdentityServiceEnvironment) WriteDefinitions(o IdentityServiceOptions, plan IdentityServicePlan) error {
	if err := identityInstallDirectory(o.SocketDir, o.UID, o.GID, 0700, true); err != nil {
		return err
	}
	for _, definition := range []Definition{plan.Identity, plan.Worker} {
		if err := identityInstallUnitParents(o.Home, filepath.Dir(definition.Path), o.UID, o.GID); err != nil {
			return err
		}
		if err := identityInstallDefinition(definition, o.UID, o.GID); err != nil {
			return err
		}
	}
	return nil
}

func (*nativeIdentityServiceEnvironment) RunnerDomain(o IdentityServiceOptions) error {
	if o.OS != "linux" {
		return nil
	}
	directory := "/run/user/" + strconv.Itoa(o.UID)
	if err := identityInstallDirectory(directory, o.UID, o.GID, 0700, false); err != nil {
		return domain.Fail(domain.Prerequisite, "installing user's systemd manager/runtime directory is unavailable; log in as that existing user")
	}
	info, err := os.Lstat(filepath.Join(directory, "bus"))
	if err != nil {
		return domain.Fail(domain.Prerequisite, "installing user's bus is unavailable; prepare the existing user manager before installation")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || int(stat.Uid) != o.UID {
		return domain.Fail(domain.Denied, "user bus must be a real socket owned by the installing user")
	}
	return nil
}
