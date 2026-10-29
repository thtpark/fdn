package identityservice

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

// ContainerStorage pins an owner-only app directory inside the iOS sandbox.
// Apple-managed ancestors may be group-writable; no native CLI selects this
// boundary, and paths below the pinned directory retain the usual checks.
type ContainerStorage struct {
	root string
	info os.FileInfo
}

func NewContainerStorage(root string) (*ContainerStorage, error) {
	if runtime.GOOS != "ios" {
		return nil, domain.Fail(domain.Denied, "app-container storage is available only to the iOS embedding")
	}
	return newContainerStorage(root)
}

func newContainerStorage(root string) (*ContainerStorage, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || os.Geteuid() == 0 || os.Geteuid() != os.Getuid() {
		return nil, domain.Fail(domain.Denied, "app container needs the current non-root account and a canonical private path")
	}
	if err := secure.Dir(root); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, domain.Fail(domain.Denied, "app-container root must already be canonical")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	return &ContainerStorage{root: root, info: info}, nil
}

func (c *ContainerStorage) checkAncestors(path string) error {
	if c == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || !inside(c.root, path) || path == c.root {
		return domain.Fail(domain.Denied, "identity path is outside the pinned app container")
	}
	current, err := os.Lstat(c.root)
	if err != nil {
		return err
	}
	if !os.SameFile(c.info, current) {
		return domain.Fail(domain.Denied, "the pinned app-container directory was replaced")
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		if !inside(c.root, parent) {
			return domain.Fail(domain.Denied, "identity path escaped its app container")
		}
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || secure.CheckOwner(info) != nil {
			return domain.Fail(domain.Denied, "app identity ancestors must be real, current-user controlled directories without group/other writes")
		}
		if parent == c.root {
			if info.Mode().Perm()&0077 != 0 {
				return domain.Fail(domain.Denied, "pinned app-container root is not owner-only")
			}
			return nil
		}
	}
}

func (o Options) lockState(dir string) (*secure.Lock, error) {
	if o.Container == nil {
		return stateLock(dir)
	}
	if err := o.Container.checkAncestors(dir); err != nil {
		return nil, err
	}
	return secure.Acquire(filepath.Join(dir, "identity.lock"))
}

func (o Options) checkOutput(path string) error {
	if o.Container == nil {
		return checkAncestors(path)
	}
	return o.Container.checkAncestors(path)
}
