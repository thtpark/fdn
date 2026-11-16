package software

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func validMode(mode uint32) bool {
	return mode != 0 && mode&^0777 == 0 && mode&0022 == 0 && mode&0400 != 0 &&
		(mode&0111 == 0 || mode&0100 != 0)
}

func relativePath(value string) error {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || path.Clean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") ||
		strings.ContainsAny(value, "\\:\x00\r\n\t") || len(strings.Split(value, "/")) > 16 ||
		len(path.Base(value)) > 100 || len(path.Dir(value)) > 155 {
		return domain.Fail(domain.Invalid, "software archive paths must be bounded clean relative POSIX file paths")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return domain.Fail(domain.Invalid, "software archive paths cannot contain control characters")
		}
	}
	return nil
}

type names struct {
	files   map[string]bool
	parents map[string]bool
}

func newNames() *names { return &names{files: make(map[string]bool), parents: make(map[string]bool)} }

func (n *names) add(value string) error {
	if err := relativePath(value); err != nil {
		return err
	}
	if n.files[value] || n.parents[value] {
		return domain.Fail(domain.Invalid, "software archive contains a duplicate or file/directory collision")
	}
	for parent := path.Dir(value); parent != "."; parent = path.Dir(parent) {
		if n.files[parent] {
			return domain.Fail(domain.Invalid, "software archive file cannot be another entry's parent")
		}
		n.parents[parent] = true
	}
	n.files[value] = true
	return nil
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	if err := relativePath(name); err != nil {
		return nil, err
	}
	for current := name; current != "."; current = path.Dir(current) {
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || current != name && !info.IsDir() {
			return nil, domain.Fail(domain.Denied, "software files require real paths without symbolic links")
		}
	}
	file, err := root.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
			err = domain.Fail(domain.Denied, "software payloads must be regular files without hard links")
		}
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func unchanged(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() &&
		before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

type byteBudget struct {
	reader io.Reader
	limit  int64
	read   int64
}

type outputBudget struct {
	writer io.Writer
	limit  int64
	wrote  int64
}

func (b *outputBudget) Write(data []byte) (int, error) {
	if int64(len(data)) > b.limit-b.wrote {
		return 0, domain.Fail(domain.Invalid, "software archive exceeds its compressed byte budget")
	}
	n, err := b.writer.Write(data)
	b.wrote += int64(n)
	return n, err
}

func (b *byteBudget) Read(p []byte) (int, error) {
	if b.read >= b.limit {
		var probe [1]byte
		n, err := b.reader.Read(probe[:])
		if n != 0 {
			return 0, domain.Fail(domain.Invalid, "software archive exceeds its byte budget")
		}
		return 0, err
	}
	if int64(len(p)) > b.limit-b.read {
		p = p[:b.limit-b.read]
	}
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}

func bundleLimit(limit int64) (int64, error) {
	if limit == 0 {
		return domain.MaxSoftwareBundleBytes, nil
	}
	if limit < 0 || limit > domain.MaxSoftwareBundleBytes {
		return 0, domain.Fail(domain.Invalid, "software archive byte limit is outside the supported bounds")
	}
	return limit, nil
}

// CreateBundle emits a deterministic gzip/USTAR archive. Only listed regular
// files are read, modes are explicit, and the caller retains ownership of writer.
func CreateBundle(writer io.Writer, rootPath string, outputs []domain.BuildOutput, epoch int64) (payloads []domain.SoftwarePayload, err error) {
	if epoch < 0 || epoch > 1<<32-1 || len(outputs) == 0 || len(outputs) > MaxPayloads {
		return nil, domain.Fail(domain.Invalid, "software bundle requires a bounded epoch and nonempty output inventory")
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, domain.Fail(domain.Denied, "software build root must be an owned real directory without foreign write access")
	}
	if err := secure.CheckOwner(info); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	opened, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, domain.Fail(domain.Conflict, "software build root changed before it could be pinned")
	}
	outputs = slices.Clone(outputs)
	slices.SortFunc(outputs, func(a, b domain.BuildOutput) int { return strings.Compare(a.Path, b.Path) })
	inventory := newNames()
	var total int64
	for _, output := range outputs {
		if err := inventory.add(output.Path); err != nil {
			return nil, err
		}
		if !validMode(output.Mode) {
			return nil, domain.Fail(domain.Invalid, "software outputs require explicit safe modes")
		}
	}
	gz, err := gzip.NewWriterLevel(&outputBudget{writer: writer, limit: domain.MaxSoftwareBundleBytes}, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gz.Header.ModTime, gz.Header.OS = time.Unix(epoch, 0).UTC(), 255
	tw := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, tw.Close(), gz.Close()) }()
	for _, output := range outputs {
		file, openErr := openRegular(root, output.Path)
		if openErr != nil {
			return nil, openErr
		}
		before, statErr := file.Stat()
		if statErr != nil {
			file.Close()
			return nil, statErr
		}
		if before.Size() > domain.MaxSoftwareBundleBytes-total {
			file.Close()
			return nil, domain.Fail(domain.Invalid, "software outputs exceed their expanded byte limit")
		}
		header := &tar.Header{Name: output.Path, Mode: int64(output.Mode), Size: before.Size(), Typeflag: tar.TypeReg,
			ModTime: time.Unix(epoch, 0).UTC(), Format: tar.FormatUSTAR}
		hash := sha256.New()
		writeErr := tw.WriteHeader(header)
		if writeErr == nil {
			_, writeErr = io.CopyN(io.MultiWriter(tw, hash), file, before.Size())
		}
		var probe [1]byte
		n, probeErr := file.Read(probe[:])
		after, afterErr := file.Stat()
		closeErr := file.Close()
		if writeErr != nil || afterErr != nil || closeErr != nil {
			return nil, errors.Join(writeErr, afterErr, closeErr)
		}
		if n != 0 || probeErr != io.EOF || !unchanged(before, after) {
			return nil, domain.Fail(domain.Conflict, "software build output changed while bundling")
		}
		payloads = append(payloads, domain.SoftwarePayload{Path: output.Path, Mode: output.Mode, Size: before.Size(),
			SHA256: hex.EncodeToString(hash.Sum(nil))})
		total += before.Size()
	}
	retained, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(opened, retained) {
		return nil, domain.Fail(domain.Conflict, "software build root was replaced while bundling; outputs were not adopted")
	}
	return payloads, nil
}

func scanBundle(reader io.Reader, limit int64, sink func(domain.SoftwarePayload) (io.WriteCloser, error)) ([]domain.SoftwarePayload, error) {
	limit, err := bundleLimit(limit)
	if err != nil {
		return nil, err
	}
	compressed := bufio.NewReader(&byteBudget{reader: reader, limit: limit})
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, domain.Fail(domain.Invalid, "software bundle is not a valid gzip stream")
	}
	defer gz.Close()
	gz.Multistream(false)
	if gz.Name != "" || gz.Comment != "" || len(gz.Extra) != 0 {
		return nil, domain.Fail(domain.Invalid, "software bundle cannot carry opaque gzip metadata")
	}
	expanded := &byteBudget{reader: gz, limit: domain.MaxSoftwareBundleBytes + int64(MaxPayloads)*1024 + 1024}
	tr := tar.NewReader(expanded)
	inventory := newNames()
	payloads := []domain.SoftwarePayload{}
	var total int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, domain.Fail(domain.Invalid, "software bundle has malformed or truncated tar contents")
		}
		if len(payloads) >= MaxPayloads || header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Linkname != "" ||
			header.Mode < 0 || header.Mode > 0777 || !validMode(uint32(header.Mode)) ||
			header.Size < 0 || header.Size > domain.MaxSoftwareBundleBytes-total {
			return nil, domain.Fail(domain.Invalid, "software bundle permits only bounded USTAR regular files with explicit safe modes")
		}
		if err := inventory.add(header.Name); err != nil {
			return nil, err
		}
		payload := domain.SoftwarePayload{Path: header.Name, Mode: uint32(header.Mode), Size: header.Size}
		hash := sha256.New()
		var output io.WriteCloser
		if sink != nil {
			output, err = sink(payload)
			if err != nil {
				return nil, err
			}
		}
		writer := io.Writer(hash)
		if output != nil {
			writer = io.MultiWriter(output, hash)
		}
		_, copyErr := io.CopyN(writer, tr, header.Size)
		var closeErr error
		if output != nil {
			closeErr = output.Close()
		}
		if copyErr != nil || closeErr != nil {
			return nil, errors.Join(domain.Fail(domain.Invalid, "software payload is truncated or could not be persisted"), copyErr, closeErr)
		}
		payload.SHA256 = hex.EncodeToString(hash.Sum(nil))
		payloads = append(payloads, payload)
		total += header.Size
	}
	var probe [1]byte
	n, tailErr := expanded.Read(probe[:])
	if n != 0 || tailErr != io.EOF {
		return nil, domain.Fail(domain.Invalid, "software bundle has trailing tar data or a corrupt gzip checksum")
	}
	if _, err := compressed.ReadByte(); err != io.EOF {
		return nil, domain.Fail(domain.Invalid, "software bundle contains concatenated or trailing compressed data")
	}
	if len(payloads) == 0 {
		return nil, domain.Fail(domain.Invalid, "software bundle cannot be empty")
	}
	return sortedPayloads(payloads), nil
}

// InspectBundle verifies complete backend-held bytes, including gzip CRC and
// absence of trailing/multistream data.
func InspectBundle(data []byte) ([]domain.SoftwarePayload, error) {
	if int64(len(data)) > domain.MaxSoftwareBundleBytes {
		return nil, domain.Fail(domain.Invalid, "software bundle exceeds its compressed byte limit")
	}
	return InspectBundleReader(bytes.NewReader(data), int64(len(data)))
}

// InspectBundleReader is the bounded streaming counterpart. Zero selects the
// maximum supported compressed size.
func InspectBundleReader(reader io.Reader, limit int64) ([]domain.SoftwarePayload, error) {
	return scanBundle(reader, limit, nil)
}

type syncedFile struct{ *os.File }

func (f syncedFile) Close() error { return errors.Join(f.Sync(), f.File.Close()) }

func emptyRoot(directory string) (*os.Root, error) {
	if err := protectedPath(directory, uint32(os.Geteuid()), false); err != nil {
		return nil, err
	}
	if err := secure.Dir(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil || len(entries) != 0 {
		root.Close()
		if err != nil {
			return nil, err
		}
		return nil, domain.Fail(domain.Conflict, "software extraction requires an empty protected real directory")
	}
	return root, nil
}

// ExtractBundle creates a new protected destination and verifies exact bytes.
// Existing destinations, including empty directories and links, are refused.
// Failure retains unactivated bytes for the caller's source-bound reconciliation.
func ExtractBundle(data []byte, directory string, expected []domain.SoftwarePayload) error {
	if err := validatePayloads(expected); err != nil {
		return err
	}
	if int64(len(data)) > domain.MaxSoftwareBundleBytes {
		return domain.Fail(domain.Invalid, "software bundle exceeds its compressed byte limit")
	}
	if err := protectedPath(directory, uint32(os.Geteuid()), true); err != nil {
		return err
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return domain.Fail(domain.Conflict, "software extraction destination already exists or cannot be proven absent")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
		return err
	}
	if err := protectedPath(filepath.Dir(directory), uint32(os.Geteuid()), false); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	return ExtractBundleReader(bytes.NewReader(data), directory, expected, int64(len(data)))
}

// ExtractBundleReader is the streaming native-staging counterpart. Its explicit
// destination must already be an empty, protected, owner-only real directory.
func ExtractBundleReader(reader io.Reader, directory string, expected []domain.SoftwarePayload, limit int64) error {
	if err := validatePayloads(expected); err != nil {
		return err
	}
	root, err := emptyRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	actual, err := scanBundle(reader, limit, func(payload domain.SoftwarePayload) (io.WriteCloser, error) {
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(payload.Path)), 0700); err != nil {
			return nil, err
		}
		file, err := root.OpenFile(filepath.FromSlash(payload.Path), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, err
		}
		if err := file.Chmod(os.FileMode(payload.Mode)); err != nil {
			file.Close()
			return nil, err
		}
		return syncedFile{file}, nil
	})
	if err != nil {
		return err
	}
	if !slices.Equal(actual, sortedPayloads(expected)) {
		return domain.Fail(domain.Conflict, "software bundle files, sizes, modes or digests differ from the signed platform inventory")
	}
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return secure.SyncDir(filepath.Join(directory, filepath.FromSlash(name)))
		}
		return nil
	})
}
