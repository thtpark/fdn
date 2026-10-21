package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/distribution"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/platform"
)

var nativeBinaries = []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}
var workerBinaries = []string{"agent-cli", "agent-service", "agent-identity"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, domain.AsFault(err).Message)
		os.Exit(1)
	}
}

func command(args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	data, err := cmd.Output()
	return strings.TrimSpace(string(data)), err
}

func run() error {
	ctx := context.Background()
	commit, err := command("git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return domain.Fail(domain.Denied, "a merged release PR event file is required")
	}
	var event distribution.MergeEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if err := distribution.CheckReleaseEvent(os.Getenv("GITHUB_ACTIONS"), os.Getenv("GITHUB_EVENT_NAME"), event, commit); err != nil {
		return err
	}
	goos, arch := os.Getenv("TARGET_OS"), os.Getenv("TARGET_ARCH")
	if !((goos == "linux" && (arch == "amd64" || arch == "arm64")) || (goos == "darwin" && arch == "arm64")) {
		return domain.Fail(domain.Invalid, "unsupported release target")
	}
	versionBytes, err := os.ReadFile("VERSION")
	if err != nil {
		return err
	}
	version := strings.TrimSpace(string(versionBytes))
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`).MatchString(version) {
		return domain.Fail(domain.Invalid, "VERSION must contain an exact semantic version")
	}
	domain.Version, domain.Commit = version, commit
	epochText, err := command("git", "show", "-s", "--format=%ct", commit)
	if err != nil {
		return err
	}
	epoch, err := strconv.ParseInt(epochText, 10, 64)
	if err != nil {
		return err
	}
	target := goos + "-" + arch
	out := filepath.Join("dist", target)
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "fdn-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	names := nativeBinaries
	ldflags := "-s -w -buildid= -X github.com/thtpark/fdn/internal/domain.Version=" + version + " -X github.com/thtpark/fdn/internal/domain.Commit=" + commit
	for _, name := range names {
		for _, pass := range []string{"first", "second"} {
			directory := filepath.Join(stage, pass)
			if err := os.MkdirAll(directory, 0700); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", filepath.Join(directory, name), "./cmd/"+name)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch, "GOTOOLCHAIN=local", "SOURCE_DATE_EPOCH="+epochText)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return err
			}
		}
		first, err := distribution.SHA256(filepath.Join(stage, "first", name))
		if err != nil {
			return err
		}
		second, err := distribution.SHA256(filepath.Join(stage, "second", name))
		if err != nil {
			return err
		}
		if first != second {
			return domain.Fail(domain.Internal, "independent binary builds were not byte-for-byte reproducible")
		}
	}
	notices, err := thirdPartyNotices()
	if err != nil {
		return err
	}
	native := []distribution.Entry{{Name: "THIRD_PARTY_NOTICES", Data: notices, Mode: 0644}}
	guideEntries, err := distribution.GuideEntries("guide")
	if err != nil {
		return err
	}
	native = append(native, guideEntries...)
	native = append(native, binaryEntries(filepath.Join(stage, "first"), "bin", names)...)
	artifacts := []string{}
	archiveName := "fdn-" + version + "-" + target + ".tar.gz"
	if err := write(filepath.Join(out, archiveName), func(w io.Writer) error { return distribution.TarGzip(w, native, epoch) }); err != nil {
		return err
	}
	artifacts = append(artifacts, archiveName)
	if goos == "linux" {
		for _, role := range []string{"foundation", "agent"} {
			pkg := "fdn-" + role
			control := "Package: " + pkg + "\nVersion: " + version + "\nArchitecture: " + arch + "\nMaintainer: Foundation Project\nSection: admin\nPriority: optional\nDescription: Native Go Foundation " + role + "\n"
			data := []distribution.Entry{{Name: "usr/share/doc/" + pkg + "/copyright", Data: notices, Mode: 0644}}
			guidePrefix := "usr/share/doc/" + pkg + "/guide"
			guideEntries, err := distribution.GuideEntries(guidePrefix)
			if err != nil {
				return err
			}
			data = append(data, guideEntries...)
			controlEntries := []distribution.Entry{{Name: "control", Data: []byte(control), Mode: 0644}}
			if role == "foundation" {
				data = append(data, distribution.Entry{Name: "usr/bin/fdn-service", Path: filepath.Join(stage, "first", "fdn-service"), Mode: 0755})
				unit, err := foundationUnit()
				if err != nil {
					return err
				}
				data = append(data, distribution.Entry{Name: "lib/systemd/system/fdn-foundation.service", Data: []byte(unit.Content), Mode: 0644})
				postinst := `#!/bin/sh
set -eu
if ! getent passwd fdn >/dev/null; then
  adduser --system --group --home /var/lib/fdn --no-create-home --shell /usr/sbin/nologin fdn
else
  getent passwd fdn | awk -F: '$6 == "/var/lib/fdn" && $7 == "/usr/sbin/nologin" {ok=1} END {exit !ok}'
fi
if [ -e /var/lib/fdn ] || [ -L /var/lib/fdn ]; then
  test -d /var/lib/fdn && test ! -L /var/lib/fdn
  test "$(stat -c %u /var/lib/fdn)" = "$(id -u fdn)"
  test "$(stat -c %a /var/lib/fdn)" = 700
else
  install -d -m 0700 -o fdn -g fdn /var/lib/fdn
fi
if command -v systemctl >/dev/null; then systemctl daemon-reload; fi
runuser -u fdn -- env HOME=/var/lib/fdn /usr/bin/fdn-service guide install --home /var/lib/fdn
printf '%s\n' 'State and identities are preserved. Follow DEPLOYMENT.md for signed Founder setup, isolated accounts/ingress, and explicit service startup.'
`
				controlEntries = append(controlEntries, distribution.Entry{Name: "postinst", Data: []byte(postinst), Mode: 0755})
			} else {
				data = append(data, binaryEntries(filepath.Join(stage, "first"), "usr/bin", workerBinaries)...)
			}
			filename := pkg + "_" + version + "_" + arch + ".deb"
			if err := write(filepath.Join(out, filename), func(w io.Writer) error { return distribution.Debian(w, controlEntries, data, epoch) }); err != nil {
				return err
			}
			check := exec.Command("dpkg-deb", "--info", filepath.Join(out, filename))
			check.Stdout, check.Stderr = os.Stdout, os.Stderr
			if err := check.Run(); err != nil {
				return err
			}
			extract := filepath.Join(stage, "extract-"+role)
			check = exec.Command("dpkg-deb", "--extract", filepath.Join(out, filename), extract)
			check.Stdout, check.Stderr = os.Stdout, os.Stderr
			if err := check.Run(); err != nil {
				return err
			}
			extractedNotices, err := distribution.SHA256(filepath.Join(extract, "usr", "share", "doc", pkg, "copyright"))
			if err != nil {
				return err
			}
			expectedNotices := sha256.Sum256(notices)
			if extractedNotices != hex.EncodeToString(expectedNotices[:]) {
				return domain.Fail(domain.Internal, "Debian extraction did not preserve required license notices")
			}
			for _, entry := range guideEntries {
				digest, err := distribution.SHA256(filepath.Join(extract, filepath.FromSlash(entry.Name)))
				if err != nil {
					return err
				}
				expected := sha256.Sum256(entry.Data)
				if digest != hex.EncodeToString(expected[:]) {
					return domain.Fail(domain.Internal, "Debian extraction did not preserve the complete version-matched guide")
				}
			}
			for _, entry := range data {
				if entry.Path == "" {
					continue
				}
				installed := filepath.Join(extract, filepath.FromSlash(entry.Name))
				if err := verifyBinary(entry.Path, installed); err != nil {
					return err
				}
			}
			artifacts = append(artifacts, filename)
		}
	}
	metadata := map[string]any{"format_version": 1, "version": version, "commit": commit, "platform": target, "source_date_epoch": epoch,
		"go_version": runtime.Version(), "reproducible_binary_builds": true, "binaries": names, "signed": false, "attested": false, "artifacts": artifacts}
	catalog, err := guidance.Catalog()
	if err != nil {
		return err
	}
	metadata["guide_content_sha256"], metadata["guide_files"], metadata["guide_skills"] = catalog.ContentSHA256, len(catalog.Files), catalog.Skills
	if err := write(filepath.Join(out, "release.json"), func(w io.Writer) error { return json.NewEncoder(w).Encode(metadata) }); err != nil {
		return err
	}
	artifacts = append(artifacts, "release.json")
	sort.Strings(artifacts)
	if err := write(filepath.Join(out, "SHA256SUMS"), func(w io.Writer) error {
		for _, name := range artifacts {
			digest, err := distribution.SHA256(filepath.Join(out, name))
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s  %s\n", digest, name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	digest, err := distribution.SHA256(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		return err
	}
	fmt.Printf("%s SHA256SUMS digest: %s\n", target, digest)
	if summary := os.Getenv("GITHUB_STEP_SUMMARY"); summary != "" {
		file, err := os.OpenFile(summary, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintf(file, "### %s\n\nCommit: `%s`\n\nSHA256SUMS SHA-256: `%s`\n\nUnsigned/unattested; obtain this digest through a trusted channel before installation.\n", target, commit, digest)
		return errors.Join(writeErr, file.Close())
	}
	return nil
}

func foundationUnit() (platform.Definition, error) {
	return platform.Render(platform.Options{
		OS: "linux", Role: "foundation", Binary: "/usr/bin/fdn-service",
		ConfigFile: "/var/lib/fdn/server.json", StateDir: "/var/lib/fdn", Home: "/var/lib/fdn",
		User: "fdn", IngressGroup: "fdn_ingress", IngressDirs: []string{"/run/fdn-ingress"},
		Definition: "/lib/systemd/system/fdn-foundation.service",
	})
}

func binaryEntries(directory, prefix string, names []string) []distribution.Entry {
	entries := make([]distribution.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, distribution.Entry{Name: prefix + "/" + name, Path: filepath.Join(directory, name), Mode: 0755})
	}
	return entries
}

func verifyBinary(source, installed string) error {
	info, err := os.Lstat(installed)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0755 {
		return domain.Fail(domain.Internal, "Debian extraction did not preserve a regular executable binary")
	}
	expected, err := distribution.SHA256(source)
	if err != nil {
		return err
	}
	actual, err := distribution.SHA256(installed)
	if err != nil {
		return err
	}
	if actual != expected {
		return domain.Fail(domain.Internal, "Debian extraction did not preserve binary contents")
	}
	return nil
}

func write(path string, fn func(io.Writer) error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	return errors.Join(fn(file), file.Sync(), file.Close())
}
