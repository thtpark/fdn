package upgrade

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type Policy struct {
	Role           string   `json:"role"`
	Name           string   `json:"name"`
	InstanceID     string   `json:"instance_id"`
	TargetID       string   `json:"target_id"`
	CAFile         string   `json:"ca_file"`
	StateDir       string   `json:"state_dir"`
	Method         string   `json:"method"`
	Prefix         string   `json:"prefix,omitempty"`
	BinaryDir      string   `json:"binary_dir"`
	User           string   `json:"user"`
	Home           string   `json:"home"`
	ConfigFile     string   `json:"config_file"`
	IdentityConfig string   `json:"identity_config,omitempty"`
	ServiceOptions string   `json:"service_options,omitempty"`
	BackupPaths    []string `json:"backup_paths"`
}

var nativeName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func inside(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func protected(path string, owner uint32) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != owner) || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm()&0022 != 0 || (current != path && !info.IsDir()) {
			return domain.Fail(domain.Denied, "updater policy/configuration/binaries require protected real paths owned by the updater or root")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

func (p Policy) Validate() error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return domain.Fail(domain.Prerequisite, "native upgrading is implemented only for Linux and macOS")
	}
	if (p.Role != "foundation" && p.Role != "agent") || !nativeName.MatchString(p.Name) ||
		p.InstanceID == "" || p.TargetID == "" || p.User == "" || p.User == "root" ||
		(p.Method != "debian" && p.Method != "archive") || len(p.BackupPaths) == 0 || len(p.BackupPaths) > 16 {
		return domain.Fail(domain.Invalid, "updater policy requires an exact role, identity, existing service user, installation method and bounded backup paths")
	}
	if p.Role == "foundation" && (runtime.GOOS != "linux" || p.TargetID != p.InstanceID) ||
		p.Method == "debian" && (runtime.GOOS != "linux" || p.BinaryDir != "/usr/bin" || os.Geteuid() != 0) {
		return domain.Fail(domain.Denied, "Foundation/Debian upgrades require separately provisioned native Linux administrator policy")
	}
	if p.Role == "foundation" && os.Geteuid() != 0 || p.Role == "agent" && runtime.GOOS == "darwin" && os.Geteuid() == 0 {
		return domain.Fail(domain.Denied, "Foundation updater requires explicit root policy; macOS worker updater runs as the existing installing user")
	}
	if p.Method == "archive" && (p.Prefix == "" || p.BinaryDir != filepath.Join(p.Prefix, "bin")) {
		return domain.Fail(domain.Invalid, "archive upgrades require the exact managed prefix/bin activation path")
	}
	paths := []string{p.CAFile, p.StateDir, p.BinaryDir, p.Home, p.ConfigFile}
	if p.Role == "agent" {
		paths = append(paths, p.IdentityConfig, p.ServiceOptions)
	}
	for _, path := range append(paths, p.BackupPaths...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n\t") {
			return domain.Fail(domain.Invalid, "updater paths must be clean absolute single-line non-root paths")
		}
	}
	for _, path := range p.BackupPaths {
		if inside(path, p.StateDir) || inside(p.StateDir, path) {
			return domain.Fail(domain.Invalid, "updater evidence must be outside application backup paths")
		}
	}
	account, err := user.Lookup(p.User)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 || account.HomeDir != p.Home || os.Geteuid() != 0 && os.Geteuid() != uid {
		return domain.Fail(domain.Denied, "updater policy must bind the actual existing non-root service account and HOME")
	}
	return nil
}

func ReadPolicy(path string) (Policy, error) {
	var policy Policy
	if err := protected(path, uint32(os.Geteuid())); err != nil {
		return policy, err
	}
	if err := config.Read(path, &policy); err != nil {
		return policy, err
	}
	if err := policy.Validate(); err != nil {
		return policy, err
	}
	if err := protected(filepath.Dir(policy.StateDir), uint32(os.Geteuid())); err != nil {
		return policy, err
	}
	if policy.Method == "archive" {
		if err := protected(policy.Prefix, uint32(os.Geteuid())); err != nil {
			return policy, err
		}
	}
	for _, path := range []string{policy.CAFile, policy.ConfigFile, policy.IdentityConfig, policy.ServiceOptions} {
		if path != "" {
			uid, _, err := policy.account()
			if err != nil {
				return policy, err
			}
			if err := protected(path, uint32(uid)); err != nil {
				return policy, err
			}
		}
	}
	return policy, nil
}

func (p Policy) account() (int, int, error) {
	account, err := user.Lookup(p.User)
	if err != nil {
		return 0, 0, err
	}
	uid, uerr := strconv.Atoi(account.Uid)
	gid, gerr := strconv.Atoi(account.Gid)
	return uid, gid, errors.Join(uerr, gerr)
}
