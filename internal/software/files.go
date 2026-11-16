package software

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

const maxRecordBytes = 4 * MaxManifestBytes

func readRecord(filename string, value any) error {
	if err := protectedPath(filename, uint32(os.Geteuid()), false); err != nil {
		return err
	}
	data, err := secure.Read(filename, maxRecordBytes)
	if err != nil {
		return err
	}
	return decodeJSON(data, value)
}

func writeRecord(filename string, value any, exclusive bool) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return domain.Fail(domain.Invalid, "native software evidence exceeds its record byte limit")
	}
	if err := protectedPath(filepath.Dir(filename), uint32(os.Geteuid()), false); err != nil {
		return err
	}
	if exclusive {
		return secure.Create(filename, data)
	}
	return secure.Write(filename, data)
}

func digestFile(filename string, limit int64, private bool) (string, int64, error) {
	if err := protectedPath(filename, uint32(os.Geteuid()), false); err != nil {
		return "", 0, err
	}
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || stat.Nlink != 1 || before.Size() > limit ||
		private && (before.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid())) {
		return "", 0, domain.Fail(domain.Denied, "native software source/evidence must be a bounded protected regular file without links")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if size > limit || size != before.Size() || !unchanged(before, after) {
		return "", 0, domain.Fail(domain.Conflict, "native software bytes changed during verification")
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func verifyTree(directory string, payloads []domain.SoftwarePayload, sealed bool) error {
	if err := validatePayloads(payloads); err != nil {
		return err
	}
	if err := protectedPath(directory, uint32(os.Geteuid()), false); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	expected := make(map[string]domain.SoftwarePayload, len(payloads))
	parents := make(map[string]bool)
	for _, payload := range payloads {
		expected[payload.Path] = payload
		for parent := path.Dir(payload.Path); parent != "."; parent = path.Dir(parent) {
			parents[parent] = true
		}
	}
	found := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := root.Lstat(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		stat, statOK := info.Sys().(*syscall.Stat_t)
		if !statOK || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) ||
			sealed && stat.Uid != uint32(os.Geteuid()) ||
			info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return domain.Fail(domain.Denied, "managed software tree contains a link or writable untrusted node")
		}
		if entry.IsDir() {
			if name != "." && !parents[name] {
				return domain.Fail(domain.Conflict, "managed software tree contains an unmanifested directory")
			}
			if sealed && info.Mode().Perm() != 0555 {
				return domain.Fail(domain.Conflict, "managed software version directory is not sealed")
			}
			return nil
		}
		payload, ok := expected[name]
		mode := os.FileMode(payload.Mode)
		if sealed {
			mode &^= 0222
		}
		if !ok || !info.Mode().IsRegular() || info.Size() != payload.Size || info.Mode().Perm() != mode {
			return domain.Fail(domain.Conflict, "managed software files differ from the exact signed inventory")
		}
		file, err := openRegular(root, name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, io.LimitReader(file, payload.Size+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || closeErr != nil {
			return errors.Join(copyErr, statErr, closeErr)
		}
		if !unchanged(info, after) || hex.EncodeToString(hash.Sum(nil)) != payload.SHA256 {
			return domain.Fail(domain.Conflict, "managed software payload is corrupted or changed during verification")
		}
		found++
		return nil
	})
	if err != nil {
		return err
	}
	if found != len(payloads) {
		return domain.Fail(domain.Conflict, "managed software inventory is incomplete")
	}
	return nil
}

func copyTree(source, destination string, payloads []domain.SoftwarePayload) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	if err := protectedPath(destination, uint32(os.Geteuid()), false); err != nil {
		return err
	}
	input, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer output.Close()
	for _, payload := range sortedPayloads(payloads) {
		if err := output.MkdirAll(filepath.FromSlash(path.Dir(payload.Path)), 0700); err != nil {
			return err
		}
		from, err := openRegular(input, payload.Path)
		if err != nil {
			return err
		}
		to, err := output.OpenFile(filepath.FromSlash(payload.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			from.Close()
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(to, hash), io.LimitReader(from, payload.Size+1))
		modeErr := to.Chmod(os.FileMode(payload.Mode) &^ 0222)
		syncErr := to.Sync()
		closeErr := errors.Join(to.Close(), from.Close())
		if err := errors.Join(copyErr, modeErr, syncErr, closeErr); err != nil {
			return err
		}
		if size != payload.Size || hex.EncodeToString(hash.Sum(nil)) != payload.SHA256 {
			return domain.Fail(domain.Conflict, "staged software changed before immutable activation")
		}
	}
	directories := []string{}
	err = fs.WalkDir(output.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slices.Reverse(directories)
	for _, directory := range directories {
		file, err := output.Open(directory)
		if err != nil {
			return err
		}
		err = errors.Join(file.Chmod(0555), file.Sync(), file.Close())
		if err != nil {
			return err
		}
	}
	return verifyTree(destination, payloads, true)
}

func ensurePrivate(directory string) error {
	if err := protectedPath(directory, uint32(os.Geteuid()), true); err != nil {
		return err
	}
	if err := secure.Dir(directory); err != nil {
		return err
	}
	return secure.SyncDir(filepath.Dir(directory))
}
