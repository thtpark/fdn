package upgrade

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func fixtureBundle(t *testing.T, platform string, modify func(map[string][]byte)) (domain.UpgradeRelease, []byte) {
	t.Helper()
	version := "0.3.4"
	files := map[string][]byte{"fdn-" + version + "-" + platform + ".tar.gz": []byte("synthetic archive")}
	if strings.HasPrefix(platform, "linux-") {
		architecture := strings.TrimPrefix(platform, "linux-")
		files["fdn-agent_"+version+"_"+architecture+".deb"] = []byte("synthetic agent package")
		files["fdn-foundation_"+version+"_"+architecture+".deb"] = []byte("synthetic foundation package")
	}
	artifacts := []string{}
	for name := range files {
		artifacts = append(artifacts, name)
	}
	files["release.json"], _ = json.Marshal(map[string]any{"format_version": 1, "version": version, "commit": strings.Repeat("a", 40),
		"platform": platform, "reproducible_binary_builds": true, "binaries": []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}, "artifacts": artifacts})
	if modify != nil {
		modify(files)
	}
	var manifest strings.Builder
	for name, data := range files {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&manifest, "%x  %s\n", sum, name)
	}
	files["SHA256SUMS"] = []byte(manifest.String())
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, data := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := output.Bytes()
	sum := sha256.Sum256(data)
	release := domain.UpgradeRelease{Version: version, Commit: strings.Repeat("a", 40), Platform: platform,
		BundleURL:    "https://github.com/" + Repository + "/releases/download/v" + version + "/fdn-" + version + "-" + platform + ".zip",
		BundleSHA256: hex.EncodeToString(sum[:]), BundleBytes: int64(len(data))}
	return release, data
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCatalog(body []byte) Catalog {
	catalog := NewCatalog()
	catalog.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	return catalog
}

func TestReleaseCatalogChecksStableVersionAndIndependentGitHubDigest(t *testing.T) {
	release, _ := fixtureBundle(t, "darwin-arm64", nil)
	body, _ := json.Marshal(map[string]any{
		"tag_name": "v" + release.Version, "target_commitish": release.Commit, "draft": false, "prerelease": false,
		"assets": []map[string]any{{"name": "fdn-" + release.Version + "-darwin-arm64.zip",
			"browser_download_url": release.BundleURL, "digest": "sha256:" + release.BundleSHA256,
			"size": release.BundleBytes, "state": "uploaded"}},
	})
	got, err := testCatalog(body).Latest(context.Background(), "darwin-arm64")
	if err != nil || got != release {
		t.Fatal(got, err)
	}
	for _, bad := range []string{
		strings.Replace(string(body), "sha256:", "", 1),
		strings.Replace(string(body), `"draft":false`, `"draft":true`, 1),
		strings.ReplaceAll(string(body), "https://github.com/", "https://untrusted.invalid/"),
	} {
		if _, err := testCatalog([]byte(bad)).Latest(context.Background(), "darwin-arm64"); err == nil {
			t.Fatal("catalog accepted an unpublished, untrusted or unchecked release")
		}
	}
}

func TestReleaseDownloadChecksExactDigestAndNeverOverwrites(t *testing.T) {
	release, bundle := fixtureBundle(t, "linux-amd64", nil)
	path := filepath.Join(t.TempDir(), "bundle.zip")
	if err := testCatalog(bundle).Download(context.Background(), release, path); err != nil {
		t.Fatal(err)
	}

	if err := testCatalog(bundle).Download(context.Background(), release, path); err == nil {
		t.Fatal("release download overwrote immutable retained bytes")
	}
	release.BundleSHA256 = strings.Repeat("0", 64)
	if err := testCatalog(bundle).Download(context.Background(), release, filepath.Join(t.TempDir(), "bad.zip")); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("mismatched release digest was accepted", err)
	}
}

func TestReleasePublishedSelectsExactServerVersionNotLatest(t *testing.T) {
	release, _ := fixtureBundle(t, "darwin-arm64", nil)
	body, err := json.Marshal(map[string]any{
		"tag_name": "v" + release.Version, "target_commitish": release.Commit,
		"assets": []map[string]any{{"name": "fdn-" + release.Version + "-darwin-arm64.zip",
			"browser_download_url": release.BundleURL, "digest": "sha256:" + release.BundleSHA256,
			"size": release.BundleBytes, "state": "uploaded"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := testCatalog(body)
	catalog.HTTP.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/repos/"+Repository+"/releases/tags/v"+release.Version {
			t.Error("published selection consulted a different release", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	actual, err := catalog.Published(context.Background(), release.Version, release.Platform)
	if err != nil || actual != release {
		t.Fatal("exact published selection failed", actual, err)
	}
	if _, err := testCatalog(body).Published(context.Background(), "0.3.5", release.Platform); err == nil {
		t.Fatal("server-compatible selection accepted a different returned version")
	}
}

func TestNativeBundleExactShapeAndTraversalChecks(t *testing.T) {
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		t.Run(platform, func(t *testing.T) {
			release, bundle := fixtureBundle(t, platform, nil)
			root := t.TempDir()
			file := filepath.Join(root, "bundle.zip")
			if err := os.WriteFile(file, bundle, 0600); err != nil {
				t.Fatal(err)
			}
			sums, err := ExtractBundle(file, filepath.Join(root, "out"), release)
			if err != nil || len(sums) != map[bool]int{true: 2, false: 4}[platform == "darwin-arm64"] {
				t.Fatal(sums, err)
			}
		})
	}
	for _, corrupt := range []func(map[string][]byte){
		func(files map[string][]byte) {
			files["../escape"] = files["release.json"]
			delete(files, "release.json")
		},
		func(files map[string][]byte) {
			files["release.json"] = bytes.ReplaceAll(files["release.json"], []byte(strings.Repeat("a", 40)), []byte(strings.Repeat("b", 40)))
		},
		func(files map[string][]byte) {
			files["wrong.deb"] = files["fdn-agent_0.3.4_amd64.deb"]
			delete(files, "fdn-agent_0.3.4_amd64.deb")
		},
	} {
		release, bundle := fixtureBundle(t, "linux-amd64", corrupt)
		root := t.TempDir()
		file := filepath.Join(root, "bundle.zip")
		if err := os.WriteFile(file, bundle, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ExtractBundle(file, filepath.Join(root, "out"), release); err == nil {
			t.Fatal("unsafe/inconsistent release bundle was accepted")
		}
	}
}

func TestReleaseComparisonAndPinnedDownloadHosts(t *testing.T) {
	for _, pair := range []struct {
		a, b string
		want int
	}{{"0.3.2", "0.3.3", -1}, {"0.9.9", "1.0.0", -1}, {"2.0.0", "1.9.9", 1}, {"1.2.3", "1.2.3", 0}} {
		got, err := CompareVersion(pair.a, pair.b)
		if err != nil || got != pair.want {
			t.Fatal(pair, got, err)
		}
	}
	for _, invalid := range []string{"dev", "1.2.3-dev", "01.2.3", "1.2", "999999999999999999999.0.0"} {
		if _, err := CompareVersion(invalid, "1.2.3"); err == nil {
			t.Fatal("unknown development/invalid version compared as a release", invalid)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, "https://attacker.invalid/asset", nil)
	if err := NewCatalog().HTTP.CheckRedirect(request, nil); err == nil {
		t.Fatal("release redirect escaped trusted hosts")
	}
}
