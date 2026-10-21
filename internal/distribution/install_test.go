package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func fixtureBundle(t *testing.T, root string, symlink bool, modify ...func([]Entry) []Entry) (string, string) {
	t.Helper()
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	name := "fdn-0.0.0-test-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	archive, err := os.Create(filepath.Join(bundle, name))
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{Name: "bin/agent-cli", Data: []byte("fixture-cli"), Mode: 0755}, {Name: "bin/agent-service", Data: []byte("fixture-agent"), Mode: 0755}, {Name: "bin/fdn-service", Data: []byte("fixture-foundation"), Mode: 0755}, {Name: "THIRD_PARTY_NOTICES", Data: []byte("fixture notices"), Mode: 0644}}
	guide, err := GuideEntries("guide")
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, guide...)
	for _, change := range modify {
		entries = change(entries)
	}
	if symlink {
		compressed := gzip.NewWriter(archive)
		writer := tar.NewWriter(compressed)
		for i, entry := range entries {
			header := &tar.Header{Name: entry.Name, Mode: 0755, Size: int64(len(entry.Data)), Typeflag: tar.TypeReg}
			if i == 0 {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/outside"
				header.Size = 0
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if i != 0 {
				if _, err := writer.Write(entry.Data); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	} else if err := TarGzip(archive, entries, 123); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := SHA256(filepath.Join(bundle, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", digest, name)), 0600); err != nil {
		t.Fatal(err)
	}
	trusted, err := SHA256(filepath.Join(bundle, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	return bundle, trusted
}

func installer(t *testing.T, root, bundle, trusted string, extra ...string) ([]byte, error) {
	t.Helper()
	temp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(temp, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{filepath.Join("..", "..", "scripts", "install.sh"), "--bundle", bundle, "--manifest-sha256", trusted, "--prefix", filepath.Join(root, "prefix"), "--acknowledge-stopped"}
	args = append(args, extra...)
	cmd := exec.Command("sh", args...)
	cmd.Env = append(os.Environ(), "TMPDIR="+temp)
	output, err := cmd.CombinedOutput()
	entries, readErr := os.ReadDir(temp)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("installer leaked verification scratch state", readErr)
	}
	return output, err
}

func TestVerifiedInstallerIsIdempotentAndPreservesState(t *testing.T) {
	root := t.TempDir()
	bundle, trusted := fixtureBundle(t, root, false)
	output, err := installer(t, root, bundle, trusted)
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		if err == nil {
			t.Fatal("unsupported Mac architecture accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(string(output), err)
	}
	installed, err := os.ReadFile(filepath.Join(root, "prefix", "bin", "agent-cli"))
	if err != nil || string(installed) != "fixture-cli" {
		t.Fatal(string(installed), err)
	}
	guide, err := os.ReadFile(filepath.Join(root, "prefix", "lib", "fdn", "current", "guide", ".agents", "skills", "foundation-work", "SKILL.md"))
	if err != nil || !bytes.Contains(guide, []byte("name: foundation-work")) {
		t.Fatal("installed release is missing its operating skill", err)
	}
	marker := filepath.Join(root, "identity-marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := installer(t, root, bundle, trusted); err != nil {
		t.Fatal(string(output), err)
	}
	bytes, err := os.ReadFile(marker)
	if err != nil || string(bytes) != "preserve" {
		t.Fatal("identity/state modified")
	}
}

func TestInstallerRejectsTamperSymlinksAndUnlistedArchives(t *testing.T) {
	for _, kind := range []string{"digest", "content", "symlink", "unlisted"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			bundle, trusted := fixtureBundle(t, root, kind == "symlink")
			switch kind {
			case "digest":
				trusted = strings.Repeat("0", 64)
			case "content":
				matches, _ := filepath.Glob(filepath.Join(bundle, "*.tar.gz"))
				if err := os.WriteFile(matches[0], []byte("tamper"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unlisted":
				safe := filepath.Join(bundle, "release.json")
				if err := os.WriteFile(safe, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				digest, err := SHA256(safe)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bundle, "SHA256SUMS"), []byte(digest+"  release.json\n"), 0600); err != nil {
					t.Fatal(err)
				}
				trusted, err = SHA256(filepath.Join(bundle, "SHA256SUMS"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if output, err := installer(t, root, bundle, trusted); err == nil {
				t.Fatal("unsafe installation accepted", string(output))
			}
			if _, err := os.Stat(filepath.Join(root, "prefix")); !os.IsNotExist(err) {
				t.Fatal("rejected bundle modified install prefix")
			}
		})
	}
}

func TestArchiveHeaderDoesNotDependOnInputOrder(t *testing.T) {
	var one, two bytes.Buffer
	a := Entry{Name: "a", Data: []byte("A"), Mode: 0644}
	b := Entry{Name: "b", Data: []byte("B"), Mode: 0644}
	if err := TarGzip(&one, []Entry{a, b}, 0); err != nil {
		t.Fatal(err)
	}

	if err := TarGzip(&two, []Entry{b, a}, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one.Bytes(), two.Bytes()) {
		t.Fatal("non-canonical archive order")
	}
}

func TestInstallerRejectsMissingGuideBeforeChangingPrefix(t *testing.T) {
	for _, missing := range []string{"guide/AGENTS.md", "guide/MANIFEST.json", "guide/.agents/skills/foundation-work/SKILL.md"} {
		t.Run(missing, func(t *testing.T) {
			root := t.TempDir()
			bundle, trusted := fixtureBundle(t, root, false, func(entries []Entry) []Entry {
				return slices.DeleteFunc(entries, func(entry Entry) bool { return entry.Name == missing })
			})
			if output, err := installer(t, root, bundle, trusted); err == nil {
				t.Fatal("installed an incomplete operating guide", string(output))
			}
			if _, err := os.Stat(filepath.Join(root, "prefix")); !os.IsNotExist(err) {
				t.Fatal("incomplete guide modified the installation prefix")
			}
		})
	}
}
