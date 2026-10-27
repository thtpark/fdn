// Command publish bundles already verified release artifacts; it never builds binaries.
package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thtpark/fdn/internal/distribution"
)

func bundle(input, output, version, commit string) error {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return errors.New("publication requires an exact stable version and merge commit")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		directory := filepath.Join(input, "fdn-"+platform)
		metadata, err := os.ReadFile(filepath.Join(directory, "release.json"))
		if err != nil {
			return err
		}
		var release struct {
			FormatVersion int      `json:"format_version"`
			Version       string   `json:"version"`
			Commit        string   `json:"commit"`
			Platform      string   `json:"platform"`
			Reproducible  bool     `json:"reproducible_binary_builds"`
			Artifacts     []string `json:"artifacts"`
			Binaries      []string `json:"binaries"`
		}
		if err := json.Unmarshal(metadata, &release); err != nil {
			return err
		}
		if release.FormatVersion != 1 || release.Version != version || release.Commit != commit || release.Platform != platform || !release.Reproducible {
			return errors.New("downloaded artifacts differ from the actual merged release source")
		}
		binaries := map[string]bool{}
		for _, name := range release.Binaries {
			binaries[name] = true
		}
		if len(binaries) != 4 || !binaries["agent-cli"] || !binaries["agent-service"] || !binaries["agent-identity"] || !binaries["fdn-service"] {
			return errors.New("publication requires all four native binaries")
		}
		manifest, err := os.ReadFile(filepath.Join(directory, "SHA256SUMS"))
		if err != nil {
			return err
		}
		names := []string{"SHA256SUMS"}
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSuffix(string(manifest), "\n"), "\n") {
			expected, name, ok := strings.Cut(line, "  ")
			if !ok || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(expected) ||
				!regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*$`).MatchString(name) || name == "SHA256SUMS" || seen[name] {
				return errors.New("invalid or duplicate publication checksum entry")
			}
			actual, err := distribution.SHA256(filepath.Join(directory, name))
			if err != nil {
				return err
			}
			if actual != expected {
				return errors.New("publication payload checksum mismatch")
			}
			seen[name] = true
			names = append(names, name)
		}
		if len(names) != len(release.Artifacts)+2 || !seen["release.json"] {
			return errors.New("publication manifest is incomplete")
		}
		for _, name := range release.Artifacts {
			if !seen[name] {
				return errors.New("publication is missing a declared artifact")
			}
		}
		file, err := os.OpenFile(filepath.Join(output, "fdn-"+version+"-"+platform+".zip"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		archive := zip.NewWriter(file)
		for _, name := range names {
			info, err := os.Lstat(filepath.Join(directory, name))
			if err != nil || !info.Mode().IsRegular() {
				file.Close()
				return errors.New("publication inputs must be regular files")
			}
			header := &zip.FileHeader{Name: name, Method: zip.Deflate}
			header.SetMode(0644)
			writer, err := archive.CreateHeader(header)
			if err != nil {
				file.Close()
				return err
			}
			source, err := os.Open(filepath.Join(directory, name))
			if err != nil {
				file.Close()
				return err
			}
			_, copyErr := io.Copy(writer, source)
			if err := errors.Join(copyErr, source.Close()); err != nil {
				file.Close()
				return err
			}
		}
		if err := errors.Join(archive.Close(), file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	raw, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var event distribution.MergeEvent
	err = json.Unmarshal(raw, &event)
	if err == nil {
		err = distribution.CheckReleaseEvent(os.Getenv("GITHUB_ACTIONS"), os.Getenv("GITHUB_EVENT_NAME"), event, os.Getenv("RELEASE_COMMIT"))
	}
	if err == nil {
		version, readErr := os.ReadFile("VERSION")
		err = readErr
		if err == nil {
			err = bundle("publish/input", "publish/output", strings.TrimSpace(string(version)), event.PullRequest.MergeCommitSHA)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
