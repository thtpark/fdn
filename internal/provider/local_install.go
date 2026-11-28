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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

const (
	ollamaRepository   = "ollama/ollama"
	maxOllamaArchive   = int64(3) << 30
	maxOllamaExtracted = int64(8) << 30
)

type ollamaRelease struct {
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

func localDownloadClient() *http.Client {
	return &http.Client{
		Timeout: 25 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			u := request.URL
			if len(via) > 5 {
				return domain.Fail(domain.Denied, "local runtime download exceeded redirect limit")
			}
			if (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") && u.Scheme == "http" {
				return nil
			}
			if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") ||
				(u.Hostname() != "github.com" && u.Hostname() != "api.github.com" &&
					u.Hostname() != "release-assets.githubusercontent.com" && u.Hostname() != "objects.githubusercontent.com") {
				return domain.Fail(domain.Denied, "local runtime release redirected outside pinned distribution hosts")
			}
			return nil
		},
	}
}

func ollamaAssetCandidates() ([]string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64", "darwin/amd64":
		return []string{"ollama-darwin.tgz"}, nil
	case "linux/amd64":
		return []string{"ollama-linux-amd64.tar.zst", "ollama-linux-amd64.tgz"}, nil
	case "linux/arm64":
		return []string{"ollama-linux-arm64.tar.zst", "ollama-linux-arm64.tgz"}, nil
	default:
		return nil, domain.Fail(domain.Prerequisite, "managed local model runtime supports native Linux/macOS amd64 and arm64 only")
	}
}

func ollamaNormalizedVersion(tag string) (string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if trimmed == "" || versionPattern.FindString(trimmed) != trimmed {
		return "", false
	}
	return trimmed, true
}

func ollamaReleaseMetadata(ctx context.Context, baseURL string, client *http.Client, selector string) (ollamaRelease, string, error) {
	var release ollamaRelease
	if client == nil {
		client = localDownloadClient()
	}
	apiRoot := "https://api.github.com/repos/" + ollamaRepository + "/releases"
	if baseURL != "" {
		apiRoot = strings.TrimRight(baseURL, "/") + "/releases"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiRoot+"/"+selector, nil)
	if err != nil {
		return release, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return release, "", domain.Fail(domain.Unavailable, "official local runtime release metadata is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return release, "", domain.Fail(domain.Unavailable, "official local runtime release channel rejected the metadata request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxBody+1))
	if err != nil {
		return release, "", err
	}
	if len(data) > domain.MaxBody || json.Unmarshal(data, &release) != nil || release.Draft || release.Prerelease {
		return release, "", domain.Fail(domain.Provider, "official local runtime release metadata is malformed or unsupported")
	}
	version, ok := ollamaNormalizedVersion(release.Version)
	if !ok {
		return release, "", domain.Fail(domain.Provider, "official local runtime release tag is malformed")
	}
	return release, version, nil
}

func (m *Manager) installLocalRuntime(ctx context.Context, version string, client *http.Client) (installed Installed, err error) {
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return installed, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	return m.installLocalRuntimeLocked(ctx, version, client)
}

func (m *Manager) installLocalRuntimeLocked(ctx context.Context, version string, client *http.Client) (installed Installed, err error) {
	normalized, ok := ollamaNormalizedVersion(version)
	if !ok || strings.ContainsAny(version, "/\\") {
		return installed, domain.Fail(domain.Invalid, "local runtime requires an exact semantic release version")
	}
	candidates, err := ollamaAssetCandidates()
	if err != nil {
		return installed, err
	}
	if client == nil {
		client = localDownloadClient()
	}
	release, releaseVer, err := ollamaReleaseMetadata(ctx, m.LocalReleaseBaseURL, client, "tags/v"+url.PathEscape(normalized))
	if err != nil || releaseVer != normalized {
		return installed, errors.Join(err, domain.Fail(domain.Provider, "official local runtime release differs from the requested version"))
	}
	var assetName, address, digest string
	var size int64
	for _, want := range candidates {
		for _, asset := range release.Assets {
			if asset.Name == want {
				if address != "" {
					return installed, domain.Fail(domain.Provider, "official local runtime asset is ambiguous")
				}
				if !strings.HasPrefix(asset.Digest, "sha256:") {
					return installed, domain.Fail(domain.Prerequisite, "official local runtime asset lacks an independent SHA256 digest")
				}
				assetName = asset.Name
				address = asset.URL
				digest = strings.TrimPrefix(asset.Digest, "sha256:")
				size = asset.Size
			}
		}
		if address != "" {
			break
		}
	}
	downloadPrefix := "https://github.com/" + ollamaRepository + "/releases/download/" + release.Version + "/"
	if m.LocalReleaseBaseURL != "" {
		downloadPrefix = strings.TrimRight(m.LocalReleaseBaseURL, "/") + "/download/" + release.Version + "/"
	}
	if assetName == "" || address != downloadPrefix+assetName || !domain.ValidHash(digest) || size <= 0 || size > maxOllamaArchive {
		return installed, domain.Fail(domain.Prerequisite, "official local runtime asset failed source/digest/platform checks")
	}

	root := filepath.Join(m.Config.StateDir, "providers", "local", "versions")
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
		return installed, domain.Fail(domain.Unavailable, "official local runtime download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return installed, domain.Fail(domain.Unavailable, "official local runtime download was rejected")
	}
	archive := filepath.Join(stage, assetName)
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
		return installed, domain.Fail(domain.Provider, "local runtime archive failed independent size/SHA256 verification")
	}

	distDir := filepath.Join(stage, "dist")
	if err := secure.Dir(distDir); err != nil {
		return installed, err
	}
	executableRel, err := extractOllamaArchive(ctx, archive, distDir)
	if err != nil {
		return installed, err
	}
	stagedExec := filepath.Join(distDir, executableRel)
	if err := m.localCompatibility(ctx, stagedExec); err != nil {
		return installed, err
	}
	versionOut, err := m.capture(ctx, stagedExec, "", "--version")
	if err != nil || versionPattern.FindString(string(versionOut)) != normalized {
		return installed, domain.Fail(domain.Provider, "staged local runtime version probe did not match the verified release")
	}
	exeHash, err := hashProviderExecutable(stagedExec)
	if err != nil {
		return installed, err
	}
	if err := os.Remove(archive); err != nil {
		return installed, err
	}
	final := filepath.Join(root, normalized+"-"+domain.NewID("build"))
	if err := os.Rename(stage, final); err != nil {
		return installed, err
	}
	published = true
	installed = Installed{
		Provider:         "local",
		Version:          normalized,
		Path:             filepath.Join(final, "dist", executableRel),
		ArtifactSHA256:   digest,
		ExecutableSHA256: exeHash,
		UpdatedAt:        time.Now().UTC().Unix(),
	}
	return installed, config.Write(filepath.Join(filepath.Dir(root), "active.json"), installed)
}

func extractOllamaArchive(ctx context.Context, archive, dest string) (string, error) {
	if strings.HasSuffix(archive, ".tar.zst") {
		cmd := exec.CommandContext(ctx, "zstd", "-dc", archive)
		cmd.Env = process.Environment()
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		stderr := process.NewBuffer(16 << 10)
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return "", domain.Fail(domain.Prerequisite, "zstd is required to unpack the Linux local model runtime archive")
		}
		rel, tarErr := unpackBoundedTar(stdout, dest)
		waitErr := cmd.Wait()
		if err := errors.Join(tarErr, waitErr); err != nil {
			return "", domain.Fail(domain.Provider, "local runtime zstd archive extraction failed")
		}
		return rel, nil
	}
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", domain.Fail(domain.Provider, "local runtime gzip archive is malformed")
	}
	defer gz.Close()
	return unpackBoundedTar(gz, dest)
}

func unpackBoundedTar(r io.Reader, dest string) (string, error) {
	destClean := filepath.Clean(dest)
	tr := tar.NewReader(r)
	var total int64
	foundBin := ""
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", domain.Fail(domain.Provider, "local runtime tar stream is corrupted")
		}
		clean := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if clean == "." || clean == "" {
			continue
		}
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsRune(clean, 0) {
			return "", domain.Fail(domain.Denied, "local runtime archive contains an unsafe path")
		}
		target := filepath.Join(destClean, clean)
		if !strings.HasPrefix(target, destClean+string(filepath.Separator)) {
			return "", domain.Fail(domain.Denied, "local runtime archive entry escaped staging directory")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || total+hdr.Size > maxOllamaExtracted {
				return "", domain.Fail(domain.Provider, "local runtime archive exceeds extraction size bound")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return "", err
			}
			mode := os.FileMode(0600)
			if hdr.Mode&0111 != 0 || clean == "ollama" || clean == filepath.Join("bin", "ollama") || strings.Contains(clean, ".so") || strings.HasSuffix(clean, ".dylib") {
				mode = 0700
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return "", err
			}
			n, copyErr := io.CopyN(out, tr, hdr.Size)
			total += n
			if err := errors.Join(copyErr, out.Sync(), out.Close()); err != nil {
				return "", err
			}
			if clean == filepath.Join("bin", "ollama") || (clean == "ollama" && foundBin == "") {
				foundBin = clean
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) || strings.ContainsRune(hdr.Linkname, 0) {
				return "", domain.Fail(domain.Denied, "local runtime archive contains an absolute symlink")
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), hdr.Linkname))
			if resolved != destClean && !strings.HasPrefix(resolved, destClean+string(filepath.Separator)) {
				return "", domain.Fail(domain.Denied, "local runtime archive symlink escapes staging directory")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return "", err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return "", err
			}
		default:
			return "", domain.Fail(domain.Denied, "local runtime archive contains an unsupported entry type")
		}
	}
	if foundBin == "" {
		return "", domain.Fail(domain.Provider, "local runtime archive did not contain the expected ollama executable")
	}
	return foundBin, nil
}

func (m *Manager) localCompatibility(ctx context.Context, path string) error {
	output := process.NewBuffer(256 << 10)
	if err := m.Runner.Run(ctx, process.Spec{Path: path, Args: []string{"--help"}, Env: process.Environment(), Stdout: output, Stderr: output}); err != nil {
		return domain.Fail(domain.Prerequisite, "local runtime capability probe failed")
	}
	data, err := output.Bytes()
	if err != nil {
		return err
	}
	for _, required := range []string{"serve", "pull"} {
		if !bytes.Contains(data, []byte(required)) {
			return domain.Fail(domain.Prerequisite, "local runtime executable lacks required serve/pull capabilities")
		}
	}
	return nil
}
