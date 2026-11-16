package main

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/distribution"
)

func sourcePublicationFixture(t *testing.T) (root, input, output, version, commit string) {
	t.Helper()
	root, version = t.TempDir(), "0.3.4"
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
			"-c", "user.name=Publication Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		cmd.Dir = root
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_DATE=2026-11-15T12:00:00Z", "GIT_COMMITTER_DATE=2026-11-15T12:00:00Z")
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated publication Git fixture: %v: %s", err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "--quiet", "--initial-branch=release")
	for name, content := range map[string]string{"VERSION": version + "\n", "go.mod": "module fixture.invalid/fdn\n\ngo 1.27\n",
		"go.sum": "", "main.go": "package main\nfunc main() {}\n", "README.md": "source guidance\n", "LICENSE": "fixture notices\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "--all")
	git("commit", "--quiet", "-m", "synthetic ancestor")
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "--all")
	git("commit", "--quiet", "-m", "synthetic release")
	commit = git("rev-parse", "HEAD")
	input, output, _, _ = publicationFixtureWithIdentity(t, version, commit)
	if _, err := distribution.CreateSourceAssets(context.Background(), root, filepath.Join(input, "fdn-source"), version, commit); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPublicationSourceCompanionsLeaveStrictNativeZIPsUnchanged(t *testing.T) {
	root, input, output, version, commit := sourcePublicationFixture(t)
	if err := publishAssets(context.Background(), root, input, output, version, commit); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 7 {
		t.Fatal("publication did not produce exactly three ZIPs and four source companions", entries, err)
	}
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
		archive, err := zip.OpenReader(filepath.Join(output, "fdn-"+version+"-"+platform+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		expected := 5
		if platform == "darwin-arm64" {
			expected = 3
		}
		if len(archive.File) != expected {
			t.Fatal("source assets changed the strict current native ZIP layout", platform)
		}
		for _, file := range archive.File {
			if strings.Contains(file.Name, "source") || strings.Contains(file.Name, "/") {
				t.Fatal("source companion was inserted into a native ZIP", file.Name)
			}
		}
		archive.Close()
	}
	digests := map[string]string{}
	for _, name := range distribution.SourceAssetNames(version) {
		expected, err := distribution.SHA256(filepath.Join(input, "fdn-source", name))
		actual, hashErr := distribution.SHA256(filepath.Join(output, name))
		if err != nil || hashErr != nil || expected != actual {
			t.Fatal("source publication changed an already verified companion", name, err, hashErr)
		}
		digests[name] = actual
	}
	if err := publishAssets(context.Background(), root, input, output, version, commit); err == nil {
		t.Fatal("publication overwrote immutable outputs")
	}
	for name, expected := range digests {
		actual, err := distribution.SHA256(filepath.Join(output, name))
		if err != nil || expected != actual {
			t.Fatal("refused repeat publication changed existing source assets", name, err)
		}
	}
}

func TestPublicationRequiresAllExactSourceCompanionsBeforeBundling(t *testing.T) {
	for _, corruption := range []string{"missing", "archive", "metadata", "commit", "dirty"} {
		t.Run(corruption, func(t *testing.T) {
			root, input, output, version, commit := sourcePublicationFixture(t)
			names := distribution.SourceAssetNames(version)
			switch corruption {
			case "missing":
				if err := os.Remove(filepath.Join(input, "fdn-source", names[3])); err != nil {
					t.Fatal(err)
				}
			case "archive", "metadata":
				index := 0
				if corruption == "metadata" {
					index = 2
				}
				if err := os.WriteFile(filepath.Join(input, "fdn-source", names[index]), []byte("tampered"), 0644); err != nil {
					t.Fatal(err)
				}
			case "commit":
				commit = strings.Repeat("b", 40)
			case "dirty":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("dirty source"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := publishAssets(context.Background(), root, input, output, version, commit); err == nil {
				t.Fatal("publication accepted missing or different source companions", corruption)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("source refusal left native outputs ready for publication", err)
			}
		})
	}
}

func TestPublicationSourceCopyRefusesSymlinksAndOverwrite(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("verified source asset"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := copySourceAsset(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := copySourceAsset(source, destination); err == nil {
		t.Fatal("source copy overwrote an existing file")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := copySourceAsset(link, filepath.Join(root, "unsafe")); err == nil {
		t.Fatal("source copy followed a symlink")
	}
}
