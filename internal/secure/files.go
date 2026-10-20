package secure

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
)

func CheckOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return domain.Fail(domain.Denied, "private state must be owned by the current OS account")
	}
	return nil
}

func Dir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return domain.Fail(domain.Denied, "state directory must be a real, owner-only directory (0700)")
	}
	return CheckOwner(info)
}

func Read(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}

	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, domain.Fail(domain.Denied, "private file must be regular, owner-only (0600), and within its size limit")
	}
	if err := CheckOwner(info); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(data) > int(limit) {
		return nil, domain.Fail(domain.Invalid, "private file exceeds its size limit")
	}
	return data, err
}

func ReadPublic(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return nil, domain.Fail(domain.Denied, "trust pins must be bounded regular files owned by this account or root, without group/other write permission")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, domain.Fail(domain.Invalid, "trust bundle exceeds its size limit")
	}
	return data, nil
}

func Write(path string, data []byte) (err error) {
	if err := Dir(filepath.Dir(path)); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return domain.Fail(domain.Denied, "refusing to replace an insecure private file")
		}
		if err := CheckOwner(info); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".fdn-write-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(temp.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

func Create(path string, data []byte) error {
	if err := Dir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr, SyncDir(filepath.Dir(path)))
}

func SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type Lock struct{ file *os.File }

func Acquire(path string) (*Lock, error) {
	if err := Dir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, domain.Fail(domain.Denied, "insecure lock file")
	}
	if err := CheckOwner(info); err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, domain.Fail(domain.Conflict, "another local worker holds the slot")
		}
		return nil, err
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	return errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
}
