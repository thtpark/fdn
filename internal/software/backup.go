package software

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

const (
	MaxBackupBytes          int64 = 1 << 30
	MaxBackupEncryptedBytes int64 = MaxBackupBytes + 64<<20
	MaxBackupFiles                = 32768
)

type backupSource struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type backupEntry struct {
	Source   string
	Name     string
	Info     os.FileInfo
	Root     string
	Relative string
}

type backupManifest struct {
	FormatVersion int             `json:"format_version"`
	PolicySHA256  string          `json:"policy_sha256"`
	PermitSHA256  string          `json:"permit_sha256"`
	Recipient     string          `json:"recipient"`
	Current       snapshot        `json:"current"`
	Sources       []backupSource  `json:"sources"`
	Directories   []string        `json:"directories"`
	CurrentRecord json.RawMessage `json:"current_record,omitempty"`
	ReceiptRecord json.RawMessage `json:"receipt_record,omitempty"`
	CapturedAt    int64           `json:"captured_at"`
}

func (n *Native) backupSources(config nativeConfig, current snapshot) []backupSource {
	sources := []backupSource{}
	seen := make(map[string]bool)
	add := func(kind, value string) {
		if value != "" && !seen[value] {
			sources = append(sources, backupSource{Kind: kind, Path: value})
			seen[value] = true
		}
	}
	if current.State == "managed" {
		add("prefix", config.Policy.Prefix)
	} else if current.State == "adoptable" {
		add("baseline", current.Root)
	}
	if config.Policy.Method == "debian" && current.Package.Present {
		add("package", config.Policy.PackageRoot)
	}
	if config.Policy.Service != "" && current.State != "absent" {
		add("service", n.serviceFilename(config.Policy))
	}
	for index, value := range config.Policy.BackupPaths {
		add("data-"+leftPad(index), value)
	}
	return sources
}

func leftPad(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

func (n *Native) backupInventory(sources []backupSource, current snapshot, owner uint32) ([]backupEntry, []string, error) {
	files := []backupEntry{}
	directories := []string{}
	var bytes int64
	for _, source := range sources {
		if err := protectedPath(source.Path, owner, false); err != nil {
			return nil, nil, domain.Fail(domain.Prerequisite, "an explicitly pinned software cold-backup source is missing or unprotected")
		}
		info, err := os.Lstat(source.Path)
		if err != nil {
			return nil, nil, err
		}
		add := func(full, relative, name string, info os.FileInfo) error {
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
				return domain.Fail(domain.Denied, "software backup cannot omit or follow links or untrusted writable files")
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 && stat.Uid != owner {
				return domain.Fail(domain.Denied, "software backup source belongs to another OS account")
			}
			if err := relativePath(name); err != nil {
				return err
			}
			if len(files)+len(directories) >= MaxBackupFiles {
				return domain.Fail(domain.Prerequisite, "software cold-backup inventory exceeds its bounded file count")
			}
			if info.IsDir() {
				directories = append(directories, name)
				return nil
			}
			if !info.Mode().IsRegular() || stat.Nlink != 1 || info.Size() > MaxBackupBytes-bytes {
				return domain.Fail(domain.Denied, "software cold backups require bounded regular bytes without hard links")
			}
			files = append(files, backupEntry{Source: full, Name: name, Info: info, Root: source.Path, Relative: relative})
			bytes += info.Size()
			return nil
		}
		if !info.IsDir() {
			if err := add(source.Path, "", source.Kind+"/file", info); err != nil {
				return nil, nil, err
			}
			continue
		}
		root, err := os.OpenRoot(source.Path)
		if err != nil {
			return nil, nil, err
		}
		err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if source.Kind == "prefix" && relative == "current" {
				actual, err := os.Readlink(filepath.Join(source.Path, "current"))
				expected, expectedErr := currentLink(Policy{Prefix: source.Path}, current.Root)
				if err != nil || expectedErr != nil || actual != expected {
					return domain.Fail(domain.Conflict, "software cold backup activation link changed after approval")
				}
				return nil
			}
			info, err := root.Lstat(filepath.FromSlash(relative))
			if err != nil {
				return err
			}
			name := source.Kind
			if relative != "." {
				name = path.Join(name, relative)
			}
			return add(filepath.Join(source.Path, filepath.FromSlash(relative)), relative, name, info)
		})
		closeErr := root.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return nil, nil, err
		}
	}
	return files, directories, nil
}

func (n *Native) validateBackupSources(config nativeConfig, current snapshot) error {
	_, _, err := n.backupInventory(n.backupSources(config, current), current, uint32(config.Policy.UID))
	return err
}

func (n *Native) coldBackup(config nativeConfig, job nativeJob) (hash string, err error) {
	recipient, err := age.ParseX25519Recipient(job.Permit.Proposal.BackupRecipient)
	if err != nil || job.Permit.Proposal.BackupRecipient != config.Policy.BackupRecipient {
		return "", domain.Fail(domain.Denied, "native software cold backup recipient changed from the exact approved off-host key")
	}
	sources := n.backupSources(config, job.Snapshot)
	files, directories, err := n.backupInventory(sources, job.Snapshot, uint32(config.Policy.UID))
	if err != nil {
		return "", err
	}
	manifest := backupManifest{FormatVersion: 1, PolicySHA256: config.PolicySHA256,
		PermitSHA256: domain.Hash(job.Permit.Signature), Recipient: job.Permit.Proposal.BackupRecipient,
		Current: job.Snapshot, Sources: sources, Directories: directories, CapturedAt: n.clock().Unix()}
	if job.Snapshot.ReceiptID != "" {
		manifest.CurrentRecord, err = secure.Read(config.Policy.CurrentReceipt, maxRecordBytes)
		if err != nil {
			return "", err
		}
		manifest.ReceiptRecord, err = secure.Read(n.receiptFilename(job.Snapshot.ReceiptID), maxRecordBytes)
		if err != nil || domain.Hash(string(manifest.ReceiptRecord)) != job.Snapshot.ReceiptSHA {
			return "", domain.Fail(domain.Conflict, "native software cold backup lost its original current receipt")
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil || len(data) > maxRecordBytes {
		return "", domain.Fail(domain.Prerequisite, "software cold-backup metadata exceeds its bounded private record")
	}
	filename := n.backupFilename(job.Permit.Proposal.Challenge.ID)
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	encrypted, err := age.Encrypt(io.MultiWriter(file, digest), recipient)
	if err != nil {
		file.Close()
		return "", err
	}
	gz := gzip.NewWriter(encrypted)
	gz.Header.ModTime, gz.Header.OS = time.Unix(manifest.CapturedAt, 0).UTC(), 255
	tw := tar.NewWriter(gz)
	defer func() {
		err = errors.Join(err, tw.Close(), gz.Close(), encrypted.Close(), file.Sync(), file.Close(), secure.SyncDir(filepath.Dir(filename)))
		if err == nil {
			hash = hex.EncodeToString(digest.Sum(nil))
		}
	}()
	header := func(name string, mode int64, size int64) error {
		return tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: size, Typeflag: tar.TypeReg,
			ModTime: time.Unix(manifest.CapturedAt, 0).UTC(), Format: tar.FormatUSTAR})
	}
	if err := header("snapshot.json", 0600, int64(len(data))); err != nil {
		return "", err
	}
	if _, err := tw.Write(data); err != nil {
		return "", err
	}
	for _, entry := range files {
		if err := protectedPath(entry.Source, uint32(config.Policy.UID), false); err != nil {
			return "", err
		}
		input, err := os.OpenFile(entry.Source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return "", err
		}
		before, statErr := input.Stat()
		if statErr != nil || !unchanged(entry.Info, before) {
			input.Close()
			return "", domain.Fail(domain.Conflict, "software cold-backup source changed before capture")
		}
		if err := header(entry.Name, int64(entry.Info.Mode().Perm()), entry.Info.Size()); err != nil {
			input.Close()
			return "", err
		}
		_, copyErr := io.CopyN(tw, input, entry.Info.Size())
		after, statErr := input.Stat()
		closeErr := input.Close()
		if err := errors.Join(copyErr, statErr, closeErr); err != nil {
			return "", err
		}
		if !unchanged(before, after) {
			return "", domain.Fail(domain.Conflict, "software cold-backup source changed during capture")
		}
	}
	for _, entry := range files {
		info, err := os.Lstat(entry.Source)
		if err != nil || !unchanged(entry.Info, info) {
			return "", domain.Fail(domain.Conflict, "software cold-backup source changed before snapshot completion")
		}
	}
	return "", nil
}
