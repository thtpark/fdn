package upgrade

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

const Repository = "thtpark/fdn"
const MaxBundleBytes int64 = 256 << 20

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var sourceCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var assetName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]*$`)

func CompareVersion(a, b string) (int, error) {
	if len(a) > 128 || len(b) > 128 {
		return 0, domain.Fail(domain.Invalid, "release version is oversized")
	}
	left, right := stableVersion.FindStringSubmatch(a), stableVersion.FindStringSubmatch(b)
	if left == nil || right == nil {
		return 0, domain.Fail(domain.Invalid, "upgrades require stable MAJOR.MINOR.PATCH versions; development binaries are not release installations")
	}
	for i := 1; i <= 3; i++ {
		x, err := strconv.ParseUint(left[i], 10, 64)
		if err != nil {
			return 0, domain.Fail(domain.Invalid, "release version component is too large")
		}
		y, err := strconv.ParseUint(right[i], 10, 64)
		if err != nil {
			return 0, domain.Fail(domain.Invalid, "release version component is too large")
		}
		if x < y {
			return -1, nil
		}
		if x > y {
			return 1, nil
		}
	}
	return 0, nil
}

func ValidateRelease(release domain.UpgradeRelease) error {
	if _, err := CompareVersion(release.Version, release.Version); err != nil {
		return err
	}
	if !sourceCommit.MatchString(release.Commit) || !domain.ValidHash(release.BundleSHA256) ||
		release.BundleBytes <= 0 || release.BundleBytes > MaxBundleBytes ||
		(release.Platform != "linux-amd64" && release.Platform != "linux-arm64" && release.Platform != "darwin-arm64") {
		return domain.Fail(domain.Invalid, "invalid bounded release source/platform/checksum")
	}
	expected := "https://github.com/" + Repository + "/releases/download/v" + release.Version +
		"/fdn-" + release.Version + "-" + release.Platform + ".zip"
	if release.BundleURL != expected {
		return domain.Fail(domain.Denied, "upgrade URL must be the exact versioned asset in the pinned Foundation repository")
	}
	return nil
}

type Catalog struct{ HTTP *http.Client }

func NewCatalog() Catalog {
	return Catalog{HTTP: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !releaseHost(req.URL) {
			return domain.Fail(domain.Denied, "release download redirected outside the pinned GitHub distribution hosts")
		}
		return nil
	}}}
}

func releaseHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

func (c Catalog) get(ctx context.Context, address string, limit int64, destination io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	if !releaseHost(request.URL) {
		return domain.Fail(domain.Denied, "untrusted release host")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Foundation-release-upgrader")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return domain.Fail(domain.Unavailable, "release service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return domain.Fail(domain.Prerequisite, "no published Foundation release exists for this channel; Actions artifacts are not an upgrade channel")
	}
	if response.StatusCode != http.StatusOK {
		return domain.Fail(domain.Unavailable, "release service rejected the request; check GitHub availability and rate limits")
	}
	count, err := io.Copy(destination, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if count > limit {
		return domain.Fail(domain.Invalid, "release response exceeds its bounded size")
	}
	return nil
}

func (c Catalog) Latest(ctx context.Context, platform string) (domain.UpgradeRelease, error) {
	return c.release(ctx, "latest", platform)
}

// Published selects an exact server-compatible release, never a newer client.
func (c Catalog) Published(ctx context.Context, version, platform string) (domain.UpgradeRelease, error) {
	if _, err := CompareVersion(version, version); err != nil {
		return domain.UpgradeRelease{}, err
	}
	release, err := c.release(ctx, "tags/v"+version, platform)
	if err == nil && release.Version != version {
		return release, domain.Fail(domain.Denied, "published release differs from the requested server version")
	}
	return release, err
}

func (c Catalog) release(ctx context.Context, selector, platform string) (domain.UpgradeRelease, error) {
	var release domain.UpgradeRelease
	var buffer strings.Builder
	if err := c.get(ctx, "https://api.github.com/repos/"+Repository+"/releases/"+selector, 1<<20, &buffer); err != nil {
		return release, err
	}
	var response struct {
		Tag        string `json:"tag_name"`
		Commit     string `json:"target_commitish"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
			State  string `json:"state"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(buffer.String()), &response); err != nil {
		return release, domain.Fail(domain.Unavailable, "invalid GitHub release metadata")
	}
	if response.Draft || response.Prerelease || !strings.HasPrefix(response.Tag, "v") {
		return release, domain.Fail(domain.Denied, "only published stable Foundation releases are eligible")
	}
	release.Version, release.Commit, release.Platform = strings.TrimPrefix(response.Tag, "v"), response.Commit, platform
	name := "fdn-" + release.Version + "-" + platform + ".zip"
	found := false
	for _, asset := range response.Assets {
		if asset.Name == name {
			if found || asset.State != "uploaded" || !strings.HasPrefix(asset.Digest, "sha256:") {
				return release, domain.Fail(domain.Denied, "release asset is ambiguous, incomplete or has no independent GitHub checksum")
			}
			found = true
			release.BundleURL, release.BundleBytes = asset.URL, asset.Size
			release.BundleSHA256 = strings.TrimPrefix(asset.Digest, "sha256:")
		}
	}
	if !found {
		return release, domain.Fail(domain.Prerequisite, "published release has no native bundle for the target platform")
	}
	return release, ValidateRelease(release)
}

func (c Catalog) Download(ctx context.Context, release domain.UpgradeRelease, destination string) error {
	if err := ValidateRelease(release); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	err = c.get(ctx, release.BundleURL, release.BundleBytes, io.MultiWriter(file, hash))
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != release.BundleSHA256 {
		return domain.Fail(domain.Denied, "release ZIP checksum does not match the independently obtained GitHub asset digest")
	}
	return nil
}

func ExtractBundle(filename, destination string, release domain.UpgradeRelease) (map[string]string, error) {
	if err := ValidateRelease(release); err != nil {
		return nil, err
	}
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	want := 5
	if release.Platform == "darwin-arm64" {
		want = 3
	}
	if len(archive.File) != want {
		return nil, domain.Fail(domain.Denied, "unexpected native release bundle shape")
	}
	entries := map[string]*zip.File{}
	for _, file := range archive.File {
		if !assetName.MatchString(file.Name) || entries[file.Name] != nil || !file.Mode().IsRegular() ||
			file.UncompressedSize64 > uint64(MaxBundleBytes) {
			return nil, domain.Fail(domain.Denied, "unsafe or duplicate release bundle entry")
		}
		entries[file.Name] = file
	}
	read := func(name string) ([]byte, error) {
		file := entries[name]
		if file == nil {
			return nil, domain.Fail(domain.Denied, "missing release bundle metadata")
		}
		if file.UncompressedSize64 > 1<<20 {
			return nil, domain.Fail(domain.Invalid, "release metadata is oversized")
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		return io.ReadAll(io.LimitReader(stream, (1<<20)+1))
	}
	manifest, err := read("SHA256SUMS")
	if err != nil {
		return nil, err
	}
	checksums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(manifest), "\n"), "\n") {
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || !domain.ValidHash(hash) || !assetName.MatchString(name) || name == "SHA256SUMS" ||
			entries[name] == nil || checksums[name] != "" {
			return nil, domain.Fail(domain.Denied, "invalid release checksum manifest")
		}
		checksums[name] = hash
	}
	if len(checksums) != want-1 {
		return nil, domain.Fail(domain.Denied, "release manifest does not cover every payload")
	}
	metadata, err := read("release.json")
	if err != nil {
		return nil, err
	}
	var info struct {
		FormatVersion int      `json:"format_version"`
		Version       string   `json:"version"`
		Commit        string   `json:"commit"`
		Platform      string   `json:"platform"`
		Reproducible  bool     `json:"reproducible_binary_builds"`
		Binaries      []string `json:"binaries"`
		Artifacts     []string `json:"artifacts"`
	}
	if err := json.Unmarshal(metadata, &info); err != nil {
		return nil, err
	}
	if info.FormatVersion != 1 || info.Version != release.Version || info.Commit != release.Commit || info.Platform != release.Platform ||
		!info.Reproducible || len(info.Binaries) != 4 {
		return nil, domain.Fail(domain.Denied, "native release metadata differs from the authorized source/version/platform")
	}
	binaries := map[string]bool{}
	for _, binary := range info.Binaries {
		binaries[binary] = true
	}
	for _, name := range []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"} {
		if !binaries[name] {
			return nil, domain.Fail(domain.Denied, "release does not contain every required native binary")
		}
	}
	expected := map[string]bool{
		"SHA256SUMS": true, "release.json": true,
		"fdn-" + release.Version + "-" + release.Platform + ".tar.gz": true,
	}
	if strings.HasPrefix(release.Platform, "linux-") {
		architecture := strings.TrimPrefix(release.Platform, "linux-")
		expected["fdn-agent_"+release.Version+"_"+architecture+".deb"] = true
		expected["fdn-foundation_"+release.Version+"_"+architecture+".deb"] = true
	}
	for name := range entries {
		if !expected[name] {
			return nil, domain.Fail(domain.Denied, "release payload names do not match the authorized version/platform")
		}
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	for name, file := range entries {
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		target, err := os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			source.Close()
			return nil, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, MaxBundleBytes+1))
		err = errors.Join(copyErr, source.Close(), target.Close())
		if err != nil {
			return nil, err
		}
		if name != "SHA256SUMS" && hex.EncodeToString(hash.Sum(nil)) != checksums[name] {
			return nil, domain.Fail(domain.Denied, fmt.Sprintf("native release payload checksum mismatch: %s", name))
		}
	}
	return checksums, nil
}
