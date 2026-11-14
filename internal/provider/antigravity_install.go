package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

const antigravityRepository = "google-antigravity/antigravity-cli"
const maxAntigravityArchive int64 = 256 << 20
const maxAntigravityExecutable int64 = 512 << 20

type antigravityRelease struct {
	Version    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

func antigravityReleaseMetadata(ctx context.Context, client *http.Client, selector string) (antigravityRelease, error) {
	var release antigravityRelease
	address := "https://api.github.com/repos/" + antigravityRepository + "/releases/" + selector
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return release, err
	}
	response, err := client.Do(request)
	if err != nil {
		return release, domain.Fail(domain.Unavailable, "official Antigravity CLI release metadata is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return release, domain.Fail(domain.Unavailable, "official Antigravity CLI release channel rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxBody+1))
	if err != nil {
		return release, err
	}
	if len(data) > domain.MaxBody || json.Unmarshal(data, &release) != nil ||
		release.Draft || release.Prerelease || release.Version == "" || versionPattern.FindString(release.Version) != release.Version {
		return release, domain.Fail(domain.Provider, "official Antigravity release metadata is malformed or unsupported")
	}
	return release, nil
}

func antigravityAssetName() (string, error) {
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "mac"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	if (osName != "linux" && osName != "mac") || (arch != "x64" && arch != "arm64") {
		return "", domain.Fail(domain.Prerequisite, "Antigravity CLI managed installation supports native Linux/macOS amd64 and arm64 only")
	}
	return "agy_cli_" + osName + "_" + arch + ".tar.gz", nil
}

func antigravityDownloadClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		u := request.URL
		if len(via) > 5 || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") ||
			(u.Hostname() != "github.com" && u.Hostname() != "api.github.com" &&
				u.Hostname() != "release-assets.githubusercontent.com" && u.Hostname() != "objects.githubusercontent.com") {
			return domain.Fail(domain.Denied, "Antigravity release redirected outside its pinned distribution hosts")
		}
		return nil
	}}
}

func hashProviderExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractAntigravityArchive(archive, executable string) (err error) {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	header, err := reader.Next()
	if err != nil {
		return err
	}
	if header.Name != "antigravity" || header.Typeflag != tar.TypeReg ||
		header.Size <= 0 || header.Size > maxAntigravityExecutable {
		return domain.Fail(domain.Provider, "Antigravity archive is not the exact bounded standalone CLI payload")
	}
	output, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(output, reader, header.Size)
	if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
		return err
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return domain.Fail(domain.Provider, "Antigravity archive contains unexpected additional entries")
	}
	return nil
}

func (m *Manager) installAntigravity(ctx context.Context, version string, client *http.Client) (installed Installed, err error) {
	if version == "" || versionPattern.FindString(version) != version || strings.ContainsAny(version, "/\\") {
		return installed, domain.Fail(domain.Invalid, "Antigravity requires an exact semantic CLI release version")
	}
	name, err := antigravityAssetName()
	if err != nil {
		return installed, err
	}
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return installed, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	release, err := antigravityReleaseMetadata(ctx, client, "tags/"+url.PathEscape(version))
	if err != nil || release.Version != version {
		return installed, errors.Join(err, domain.Fail(domain.Provider, "official Antigravity release differs from the requested source"))
	}
	address, digest, size := "", "", int64(0)
	for _, asset := range release.Assets {
		if asset.Name == name {
			if address != "" {
				return installed, domain.Fail(domain.Provider, "official Antigravity asset is ambiguous")
			}
			if !strings.HasPrefix(asset.Digest, "sha256:") {
				return installed, domain.Fail(domain.Prerequisite, "official Antigravity CLI asset has no independently supplied SHA256 digest")
			}
			address, digest, size = asset.URL, strings.TrimPrefix(asset.Digest, "sha256:"), asset.Size
		}
	}
	expected := "https://github.com/" + antigravityRepository + "/releases/download/" + version + "/" + name
	if address != expected || !domain.ValidHash(digest) || size <= 0 || size > maxAntigravityArchive {
		return installed, domain.Fail(domain.Prerequisite, "official Antigravity CLI asset lacks its independently supplied digest/platform")
	}
	root := filepath.Join(m.Config.StateDir, "providers", "antigravity", "versions")
	if err := secure.Dir(root); err != nil {
		return installed, err
	}
	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return installed, err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, os.RemoveAll(stage))
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return installed, err
	}
	response, err := client.Do(request)
	if err != nil {
		return installed, domain.Fail(domain.Unavailable, "official Antigravity CLI asset download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return installed, domain.Fail(domain.Unavailable, "official Antigravity CLI asset download was rejected")
	}
	archive := filepath.Join(stage, "release.tar.gz")
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return installed, err
	}
	hash := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, size+1))
	if err := errors.Join(copyErr, file.Sync(), file.Close()); err != nil {
		return installed, err
	}
	if count != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return installed, domain.Fail(domain.Provider, "Antigravity asset failed independent size/digest verification")
	}
	path := filepath.Join(stage, "antigravity")
	if err := extractAntigravityArchive(archive, path); err != nil {
		return installed, err
	}
	if err := m.antigravityCompatibility(ctx, path); err != nil {
		return installed, err
	}
	output, err := m.capture(ctx, path, "", "--version")
	if err != nil || versionPattern.FindString(string(output)) != version {
		return installed, domain.Fail(domain.Provider, "Antigravity staged CLI source/version probe failed")
	}
	executableHash, err := hashProviderExecutable(path)
	if err != nil {
		return installed, err
	}
	final := filepath.Join(root, version+"-"+domain.NewID("build"))
	if err := os.Rename(stage, final); err != nil {
		return installed, err
	}
	published = true
	installed = Installed{Provider: "antigravity", Version: version, Path: filepath.Join(final, "antigravity"),
		ArtifactSHA256: digest, ExecutableSHA256: executableHash, UpdatedAt: time.Now().UTC().Unix()}
	return installed, config.Write(filepath.Join(filepath.Dir(root), "active.json"), installed)
}

func (m *Manager) antigravityCompatibility(ctx context.Context, path string) error {
	for _, probe := range []struct {
		args     []string
		required []string
	}{
		{[]string{"--help"}, []string{"--json-schema", "--output-format", "--agent", "--settings", "--disable-slash-commands"}},
		{[]string{"acp", "--help"}, []string{"--stdio", "--include-default-customizations", "--gemini-dir", "--dangerously-skip-permissions", "--disable-telemetry", "--login"}},
	} {
		output := process.NewBuffer(256 << 10)
		if err := m.Runner.Run(ctx, process.Spec{Path: path, Args: probe.args, Env: process.Environment(), Stdout: output, Stderr: output}); err != nil {
			return domain.Fail(domain.Prerequisite, "Antigravity CLI help/capability probe failed")
		}
		data, err := output.Bytes()
		if err != nil {
			return err
		}
		for _, flag := range probe.required {
			if !bytes.Contains(data, []byte(flag)) {
				return domain.Fail(domain.Prerequisite, "Antigravity executable lacks the supported standalone CLI/ACP isolation capabilities; the IDE launcher is not a provider")
			}
		}
	}
	return nil
}
