package upgrade

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

var archiveBinaries = []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}

func archiveRoot(prefix string) string { return filepath.Join(prefix, "lib", "fdn") }

func managedLink(path, expected string) error {
	if err := protected(filepath.Dir(path), uint32(os.Geteuid())); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) || info.Mode()&os.ModeSymlink == 0 {
		return domain.Fail(domain.Denied, "archive activation requires existing owner-controlled managed links")
	}
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if target != expected {
		return domain.Fail(domain.Conflict, "archive managed link differs from its exact canonical target")
	}
	return nil
}

func managedArchive(prefix string) (string, error) {
	root := archiveRoot(prefix)
	current := filepath.Join(root, "current")
	target, err := os.Readlink(current)
	if err != nil {
		return "", err
	}
	if filepath.Clean(target) != target || filepath.Dir(target) != root || target == current {
		return "", domain.Fail(domain.Denied, "archive baseline is outside its canonical immutable release directory")
	}
	if err := managedLink(current, target); err != nil {
		return "", err
	}
	if err := protected(target, uint32(os.Geteuid())); err != nil {
		return "", err
	}
	for _, name := range archiveBinaries {
		if err := managedLink(filepath.Join(prefix, "bin", name), filepath.Join(current, "bin", name)); err != nil {
			return "", err
		}
		if err := protected(filepath.Join(target, "bin", name), uint32(os.Geteuid())); err != nil {
			return "", err
		}
	}
	return target, nil
}

func verifyArchivePayload(root string, platform domain.SoftwarePlatform) error {
	found := map[string]bool{}
	for _, entry := range platform.Payloads {
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		if !inside(root, path) || filepath.ToSlash(filepath.Clean(entry.Path)) != entry.Path {
			return domain.Fail(domain.Denied, "archive payload path is not canonical")
		}
		if err := protected(path, uint32(os.Geteuid())); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != entry.Mode || info.Size() != entry.Size {
			return domain.Fail(domain.Conflict, "archive payload metadata differs from its signed release")
		}
		digest, err := fileHash(path)
		if err != nil {
			return err
		}
		if digest != entry.SHA256 || found[entry.Path] {
			return domain.Fail(domain.Conflict, "archive payload bytes differ from its signed release")
		}
		found[entry.Path] = true
	}
	for _, name := range archiveBinaries {
		if !found["bin/"+name] {
			return domain.Fail(domain.Denied, "archive release omits a managed executable")
		}
	}
	return nil
}

func activateArchive(prefix, payload, id string) error {
	if _, err := managedArchive(prefix); err != nil {
		return err
	}
	root := archiveRoot(prefix)
	if filepath.Dir(payload) != root || filepath.Base(payload) == "current" {
		return domain.Fail(domain.Denied, "archive activation requires its canonical immutable staged release")
	}
	if err := protected(payload, uint32(os.Geteuid())); err != nil {
		return err
	}
	temporary := filepath.Join(root, ".current-"+id)
	if err := os.Symlink(payload, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(root, "current")); err != nil {
		return err
	}
	return secure.SyncDir(root)
}
