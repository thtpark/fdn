package software

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func checkDPKGHooks(policy Policy) error {
	architecture := strings.TrimPrefix(policy.Platform, "linux-")
	for _, name := range []string{policy.Package, policy.Package + ":" + architecture} {
		for _, extension := range []string{"preinst", "postinst", "prerm", "postrm", "config", "triggers"} {
			filename := filepath.Join("/var/lib/dpkg/info", name+"."+extension)
			if _, err := os.Lstat(filename); err == nil {
				return domain.Fail(domain.Denied, "Debian software cannot execute incumbent package maintainer scripts or triggers")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, directory := range []string{"/etc/dpkg", "/etc/dpkg/dpkg.cfg.d"} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) > 64 {
			return domain.Fail(domain.Prerequisite, "Debian software requires its protected native dpkg configuration")
		}
		for _, entry := range entries {
			if entry.IsDir() || directory == "/etc/dpkg" && entry.Name() != "dpkg.cfg" {
				continue
			}
			filename := filepath.Join(directory, entry.Name())
			if err := protectedPath(filename, 0, false); err != nil {
				return err
			}
			data, err := secure.ReadPublic(filename, 64<<10)
			if err != nil {
				return domain.Fail(domain.Prerequisite, "dpkg configuration is not bounded and readable")
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "#") || line == "" {
					continue
				}
				if strings.Contains(line, "pre-invoke") || strings.Contains(line, "post-invoke") ||
					strings.HasPrefix(line, "instdir") || strings.HasPrefix(line, "admindir") ||
					strings.HasPrefix(line, "root") || strings.HasPrefix(line, "force") {
					return domain.Fail(domain.Denied, "Debian software policy cannot inherit dpkg hooks, force options or alternate roots")
				}
			}
		}
	}
	return nil
}

type packageState struct {
	Present bool   `json:"present"`
	Version string `json:"version,omitempty"`
}

func (n *Native) readPackage(ctx context.Context, policy Policy) (packageState, error) {
	data, err := n.readCommand(ctx, policy, "/usr/bin/dpkg-query",
		[]string{"--show", "--showformat=${Package}\t${Version}\t${Architecture}\t${Status}\n", policy.Package})
	if err != nil {
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(bytes.TrimSpace(data)) == 0 {
			return packageState{}, nil
		}
		return packageState{}, domain.Fail(domain.Prerequisite, "the exact Debian package cannot be completely observed")
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "\t")
	if len(fields) != 4 || fields[0] != policy.Package || fields[2] != strings.TrimPrefix(policy.Platform, "linux-") ||
		fields[3] != "install ok installed" || !versionPattern.MatchString(fields[1]) {
		return packageState{}, domain.Fail(domain.Conflict, "Debian package is foreign, partially configured or on the wrong native architecture")
	}
	list, err := n.readCommand(ctx, policy, "/usr/bin/dpkg-query", []string{"--listfiles", policy.Package})
	if err != nil {
		return packageState{}, domain.Fail(domain.Prerequisite, "Debian package ownership list is unavailable")
	}
	for _, name := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		if name == "/." {
			continue
		}
		if !absolutePath(name) || !inside(policy.PackageRoot, name) && !inside(name, policy.PackageRoot) {
			return packageState{}, domain.Fail(domain.Denied, "incumbent Debian package owns files outside its exact isolated package prefix")
		}
		if !inside(policy.PackageRoot, name) {
			info, err := os.Lstat(name)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return packageState{}, domain.Fail(domain.Denied, "incumbent Debian package has a non-directory ancestor outside its isolated prefix")
			}
		}
	}
	return packageState{Present: true, Version: fields[1]}, nil
}

func debianPath(value string, directory bool) (string, error) {
	value = strings.TrimPrefix(value, "./")
	if directory {
		value = strings.TrimSuffix(value, "/")
		if value == "" || value == "." {
			return ".", nil
		}
	}
	if err := relativePath(value); err != nil {
		return "", err
	}
	return value, nil
}

func (n *Native) packageTar(ctx context.Context, policy Policy, option, filename string, limit int64, inspect func(io.Reader) error) error {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	result := make(chan error, 1)
	go func() {
		err := n.run(commandCtx, policy, "/usr/bin/dpkg-deb", []string{option, filename}, writer)
		_ = writer.CloseWithError(err)
		result <- err
	}()
	inspectErr := inspect(&byteBudget{reader: reader, limit: limit})
	_ = reader.CloseWithError(inspectErr)
	cancel()
	runErr := <-result
	if inspectErr != nil || runErr != nil {
		return errors.Join(domain.Fail(domain.Invalid, "Debian package inspection failed without installation"), inspectErr, runErr)
	}
	return nil
}

func strictTarTail(reader io.Reader) error {
	buffer := make([]byte, 512)
	for {
		n, err := reader.Read(buffer)
		for _, value := range buffer[:n] {
			if value != 0 {
				return domain.Fail(domain.Invalid, "Debian archive has unrecognized trailing contents")
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func parseControl(data []byte, policy Policy, release domain.SoftwareRelease) error {
	fields := make(map[string]string)
	last := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if last != "Description" {
				return domain.Fail(domain.Invalid, "Debian control has opaque folded identity or dependency fields")
			}
			continue
		}
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || fields[key] != "" {
			return domain.Fail(domain.Invalid, "Debian control is malformed or duplicates a field")
		}
		fields[key], last = strings.TrimSpace(value), key
	}
	if fields["Package"] != policy.Package || fields["Version"] != release.Manifest.Version ||
		fields["Architecture"] != strings.TrimPrefix(policy.Platform, "linux-") {
		return domain.Fail(domain.Denied, "Debian package identity/version/architecture differs from the signed native product")
	}
	for _, forbidden := range []string{"Depends", "Pre-Depends", "Conflicts", "Breaks", "Replaces", "Provides", "Essential", "Multi-Arch"} {
		if fields[forbidden] != "" {
			return domain.Fail(domain.Denied, "Debian native installation cannot alter other packages or infer dependency authority")
		}
	}
	return nil
}

func (n *Native) inspectPackage(ctx context.Context, policy Policy, filename string, release domain.SoftwareRelease, destination string) ([]domain.SoftwarePayload, error) {
	controlFound := false
	err := n.packageTar(ctx, policy, "--ctrl-tarfile", filename, 4<<20, func(reader io.Reader) error {
		tr := tar.NewReader(reader)
		seen := make(map[string]bool)
		for count := 0; ; count++ {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			directory := header.Typeflag == tar.TypeDir
			name, err := debianPath(header.Name, directory)
			if err != nil || count >= 64 || seen[name] || header.Linkname != "" || len(header.PAXRecords) != 0 ||
				header.Mode < 0 || header.Mode > 0777 || header.Mode&0022 != 0 || header.Uid != 0 || header.Gid != 0 ||
				(header.Uname != "" && header.Uname != "root") || (header.Gname != "" && header.Gname != "root") {
				return domain.Fail(domain.Invalid, "Debian control archive contains unsafe or duplicate entries")
			}
			seen[name] = true
			if directory && name == "." {
				continue
			}
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA ||
				(name != "control" && name != "md5sums") || header.Size < 0 || header.Size > 1<<20 {
				return domain.Fail(domain.Denied, "downloaded Debian maintainer scripts, triggers and hooks are forbidden")
			}
			data, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
			if err != nil || int64(len(data)) != header.Size {
				return domain.Fail(domain.Invalid, "Debian control entry is truncated")
			}
			if name == "control" {
				if err := parseControl(data, policy, release); err != nil {
					return err
				}
				controlFound = true
			}
		}
		return strictTarTail(reader)
	})
	if err != nil {
		return nil, err
	}
	if !controlFound {
		return nil, domain.Fail(domain.Invalid, "Debian package lacks its exact control identity")
	}
	var root *os.Root
	if destination != "" {
		root, err = emptyRoot(destination)
		if err != nil {
			return nil, err
		}
		defer root.Close()
	}
	payloads := []domain.SoftwarePayload{}
	prefix := filepath.ToSlash(strings.TrimPrefix(policy.PackageRoot, "/"))
	err = n.packageTar(ctx, policy, "--fsys-tarfile", filename,
		domain.MaxSoftwareBundleBytes+int64(MaxPayloads)*1024+1024, func(reader io.Reader) error {
			tr := tar.NewReader(reader)
			seen := make(map[string]bool)
			files := newNames()
			var total int64
			for count := 0; ; count++ {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				directory := header.Typeflag == tar.TypeDir
				name, err := debianPath(header.Name, directory)
				if err != nil || count >= MaxPayloads*16 || seen[name] || header.Linkname != "" ||
					len(header.PAXRecords) != 0 || header.Mode < 0 || header.Mode > 0777 || header.Mode&0022 != 0 ||
					header.Uid != 0 || header.Gid != 0 || (header.Uname != "" && header.Uname != "root") ||
					(header.Gname != "" && header.Gname != "root") {
					return domain.Fail(domain.Invalid, "Debian data archive contains unsafe or duplicate entries")
				}
				seen[name] = true
				if directory {
					if header.Size != 0 || header.Mode&0500 != 0500 {
						return domain.Fail(domain.Invalid, "Debian package directories require safe readable root-owned modes")
					}
					if name != "." && name != prefix && !strings.HasPrefix(prefix, name+"/") && !strings.HasPrefix(name, prefix+"/") {
						return domain.Fail(domain.Denied, "Debian package directories escape the exact isolated prefix")
					}
					continue
				}
				if (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || !strings.HasPrefix(name, prefix+"/") ||
					!validMode(uint32(header.Mode)) || header.Size < 0 || header.Size > domain.MaxSoftwareBundleBytes-total ||
					len(payloads) >= MaxPayloads {
					return domain.Fail(domain.Denied, "Debian payload permits only bounded regular files inside its exact isolated prefix")
				}
				relative := strings.TrimPrefix(name, prefix+"/")
				if err := files.add(relative); err != nil {
					return err
				}
				payload := domain.SoftwarePayload{Path: relative, Mode: uint32(header.Mode), Size: header.Size}
				hash := sha256.New()
				var file *os.File
				var sink io.Writer = hash
				if root != nil {
					if err := root.MkdirAll(filepath.FromSlash(path.Dir(relative)), 0700); err != nil {
						return err
					}
					file, err = root.OpenFile(filepath.FromSlash(relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
					if err != nil {
						return err
					}
					sink = io.MultiWriter(file, hash)
				}
				_, copyErr := io.CopyN(sink, tr, header.Size)
				var closeErr error
				if file != nil {
					closeErr = errors.Join(file.Chmod(os.FileMode(payload.Mode)), file.Sync(), file.Close())
				}
				if err := errors.Join(copyErr, closeErr); err != nil {
					return err
				}
				payload.SHA256 = hex.EncodeToString(hash.Sum(nil))
				payloads = append(payloads, payload)
				total += header.Size
			}
			return strictTarTail(reader)
		})
	if err != nil {
		return nil, err
	}
	if err := validatePayloads(payloads); err != nil {
		return nil, err
	}
	return sortedPayloads(payloads), nil
}

func (n *Native) installPackage(ctx context.Context, policy Policy, filename string, release domain.SoftwareRelease, payloads []domain.SoftwarePayload) error {
	if err := n.run(ctx, policy, "/usr/bin/dpkg", []string{"--no-triggers", "--install", filename}, nil); err != nil {
		return domain.Fail(domain.Unavailable, "native Debian installation is uncertain; no package-manager retry is permitted")
	}
	state, err := n.readPackage(ctx, policy)
	if err != nil {
		return err
	}
	if !state.Present || state.Version != release.Manifest.Version {
		return domain.Fail(domain.Conflict, "native Debian installation did not register the exact signed package")
	}
	if err := verifyTree(policy.PackageRoot, payloads, false); err != nil {
		return err
	}
	return nil
}

func packagePayload(policy Policy, release domain.SoftwareRelease) (domain.SoftwarePayload, error) {
	platform, err := platformFor(release, policy.Platform)
	if err != nil {
		return domain.SoftwarePayload{}, err
	}
	if len(platform.Payloads) != 1 || platform.Payloads[0].Path != policy.PackageFile {
		return domain.SoftwarePayload{}, domain.Fail(domain.Denied, "Debian source bundle must contain only the policy's exact signed package file")
	}
	return platform.Payloads[0], nil
}

func immutableName(release domain.SoftwareRelease) (string, error) {
	data, err := CanonicalManifest(release.Manifest)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(release.Manifest.Revision, 10) + "-" + domain.Hash(string(data)), nil
}
