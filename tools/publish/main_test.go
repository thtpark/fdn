package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/distribution"
)

func publicationFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	input, output, version, commit := filepath.Join(root, "input"), filepath.Join(root, "output"), "0.3.4", strings.Repeat("a", 40)
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		directory := filepath.Join(input, "fdn-"+platform)
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		names := []string{"fdn-" + version + "-" + platform + ".tar.gz"}
		if strings.HasPrefix(platform, "linux-") {
			architecture := strings.TrimPrefix(platform, "linux-")
			names = append(names, "fdn-agent_"+version+"_"+architecture+".deb", "fdn-foundation_"+version+"_"+architecture+".deb")
		}
		metadata, _ := json.Marshal(map[string]any{"format_version": 1, "version": version, "commit": commit, "platform": platform,
			"reproducible_binary_builds": true, "artifacts": names, "binaries": []string{"agent-cli", "agent-service", "agent-identity", "fdn-service"}})
		if err := os.WriteFile(filepath.Join(directory, "release.json"), metadata, 0600); err != nil {
			t.Fatal(err)
		}
		var manifest strings.Builder
		for _, name := range append(names, "release.json") {
			path := filepath.Join(directory, name)
			if name != "release.json" {
				if err := os.WriteFile(path, []byte("synthetic native payload"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			hash, err := distribution.SHA256(path)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&manifest, "%s  %s\n", hash, name)
		}
		if err := os.WriteFile(filepath.Join(directory, "SHA256SUMS"), []byte(manifest.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return input, output, version, commit
}

func TestPublicationBundlesAllPlatformsWithoutRebuildingOrOverwriting(t *testing.T) {
	input, output, version, commit := publicationFixture(t)
	if err := bundle(input, output, version, commit); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		archive, err := zip.OpenReader(filepath.Join(output, "fdn-"+version+"-"+platform+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		count := 5
		if platform == "darwin-arm64" {
			count = 3
		}
		if len(archive.File) != count {
			t.Fatal("durable native asset has a different shape", platform, len(archive.File))
		}
		archive.Close()
	}
	if err := bundle(input, output, version, commit); err == nil {
		t.Fatal("immutable publication outputs were overwritten")
	}
}

func TestPublicationRejectsWrongSourceAndDamagedPayloadBeforeRelease(t *testing.T) {
	for _, corruption := range []string{"source", "payload"} {
		t.Run(corruption, func(t *testing.T) {
			input, output, version, commit := publicationFixture(t)
			if corruption == "source" {
				commit = strings.Repeat("b", 40)
			} else if err := os.WriteFile(filepath.Join(input, "fdn-linux-amd64", "fdn-"+version+"-linux-amd64.tar.gz"), []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := bundle(input, output, version, commit); err == nil {
				t.Fatal("untrusted/different publication source was accepted")
			}
		})
	}
}
