package platform

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// The root helper prepares only the explicitly configured ingress directories.
// setgid inheritance lets the unprivileged service create group-bound sockets
// without adding any agent to its DB group or granting it chown privileges.
func (m *AccountManager) prepareNativeIngressDirectories() error {
	seen := map[string]bool{}
	for _, socket := range []string{m.options.APISocket, m.options.JoinSocket} {
		path := filepath.Dir(socket)
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := accountAncestors(filepath.Dir(path), 0, true); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0700); err == nil {
			if err := os.Chown(path, int(m.options.ServerUID), int(m.options.IngressGID)); err != nil {
				return err
			}
			if err := os.Chmod(path, os.ModeSetgid|0710); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != m.options.ServerUID || stat.Gid != m.options.IngressGID ||
			info.Mode()&os.ModeSetgid == 0 || (info.Mode().Perm() != 0710 && info.Mode().Perm() != 0750) {
			return errors.New("native ingress directory must be server-owned, ingress-group-only and setgid")
		}
	}
	return nil
}
