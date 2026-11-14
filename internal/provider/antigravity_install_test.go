package provider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type antigravityReleaseTransport struct {
	archive   []byte
	badDigest bool
}

func (r antigravityReleaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var data []byte
	if request.URL.Host == "api.github.com" {
		name, err := antigravityAssetName()
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(r.archive)
		digest := hex.EncodeToString(hash[:])
		if r.badDigest {
			digest = strings.Repeat("0", 64)
		}
		data, err = json.Marshal(map[string]any{
			"tag_name": "1.5.8", "draft": false, "prerelease": false,
			"assets": []map[string]any{{"name": name, "size": len(r.archive), "digest": "sha256:" + digest,
				"browser_download_url": "https://github.com/" + antigravityRepository + "/releases/download/1.5.8/" + name}},
		})
		if err != nil {
			return nil, err
		}
	} else {
		data = r.archive
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
}

func antigravityArchive(t *testing.T, name string) []byte {
	t.Helper()
	var data bytes.Buffer
	zip := gzip.NewWriter(&data)
	writer := tar.NewWriter(zip)
	payload := []byte("qualified fixture executable, not a real provider")
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeReg, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestAntigravityInstallUsesOfficialIndependentDigestAndRetainsOldActivation(t *testing.T) {
	manager, _ := antigravityManager(t, true)
	manager.Config.ProviderPaths = map[string]string{}
	archive := antigravityArchive(t, "antigravity")
	client := &http.Client{Transport: antigravityReleaseTransport{archive: archive}}
	installed, err := manager.installAntigravity(context.Background(), "1.5.8", client)
	if err != nil || installed.SignaturesVerified || !domain.ValidHash(installed.ArtifactSHA256) || !domain.ValidHash(installed.ExecutableSHA256) {
		t.Fatal("official native integrity verification was lost or called code signing", installed, err)
	}
	resolved, err := manager.Resolve("antigravity")
	if err != nil || resolved != installed.Path {
		t.Fatal("verified native provider did not resolve", resolved, err)
	}
	client.Transport = antigravityReleaseTransport{archive: archive, badDigest: true}
	if _, err := manager.installAntigravity(context.Background(), "1.5.8", client); err == nil {
		t.Fatal("invalid independent digest was accepted")
	}
	var current Installed
	if err := config.Read(filepath.Join(manager.Config.StateDir, "providers", "antigravity", "active.json"), &current); err != nil || current != installed {
		t.Fatal("failed install changed existing native activation", current, err)
	}
	if err := os.WriteFile(installed.Path, []byte("changed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve("antigravity"); err == nil {
		t.Fatal("an externally replaced/self-updated executable was adopted")
	}
}

func TestAntigravityArchiveRejectsUnexpectedPathsWithoutExecution(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "archive.tar.gz")
	if err := os.WriteFile(archive, antigravityArchive(t, "../outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractAntigravityArchive(archive, filepath.Join(root, "agy")); err == nil || domain.AsFault(err).Code != domain.Provider {
		t.Fatal("untrusted native archive layout was accepted", err)
	}
}
