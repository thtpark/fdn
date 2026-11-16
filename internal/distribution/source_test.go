package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sourceFixture(t *testing.T) (root, version, commit, parent string) {
	t.Helper()
	root, version = t.TempDir(), "0.3.45"
	sourceTestGit(t, root, "init", "--quiet", "--initial-branch=release")
	files := map[string]string{
		"VERSION": version + "\n", "go.mod": "module fixture.invalid/fdn\n\ngo 1.27\n", "go.sum": "",
		"main.go": "package main\nfunc main() {}\n", "main_test.go": "package main\n",
		"README.md": "tracked source guidance\n", "LICENSE": "fixture license\n", "THIRD_PARTY_NOTICES": "fixture notices\n",
		".gitignore":        ".local/\ndist/\nignored.txt\n",
		".gitattributes":    "guide/exported.md export-ignore\nguide/subst.md export-subst\n",
		"guide/exported.md": "must not be omitted\n", "guide/subst.md": "$Format:%H$\n",
		"guide/" + strings.Repeat("long-", 30) + ".md": "long portable PAX name\n",
		".agents/skills/fixture/SKILL.md":              "---\nname: fixture\n---\nsource guide\n",
		"scripts/build.sh":                             "#!/bin/sh\nexit 0\n", "ancestry.txt": "real retained ancestor\n",
	}
	for name, content := range files {
		sourceTestWrite(t, filepath.Join(root, filepath.FromSlash(name)), []byte(content))
	}
	if err := os.Chmod(filepath.Join(root, "scripts", "build.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../README.md", filepath.Join(root, "guide", "readme-link")); err != nil {
		t.Fatal(err)
	}
	sourceTestGit(t, root, "add", "--all")
	parent = sourceTestCommit(t, root)
	sourceTestGit(t, root, "rm", "--quiet", "ancestry.txt")
	sourceTestWrite(t, filepath.Join(root, "release.go"), []byte("package main\n"))
	sourceTestGit(t, root, "add", "--all")
	commit = sourceTestCommit(t, root)
	return
}

func sourceTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	data, err := sourceGit(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func sourceTestCommit(t *testing.T, root string) string {
	t.Helper()
	cmd := sourceCommand(context.Background(), root, "-c", "user.name=Source Fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "synthetic source fixture")
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE=2026-11-15T12:00:00Z", "GIT_COMMITTER_DATE=2026-11-15T12:00:00Z")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture commit: %v: %s", err, output)
	}
	return sourceTestGit(t, root, "rev-parse", "HEAD")
}

func sourceTestWrite(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCheckoutCanonicalAncestorAliasesRemainRootBound(t *testing.T) {
	root, version, commit, _ := sourceFixture(t)
	ancestorAlias := filepath.Join(t.TempDir(), "ancestor")
	if err := os.Symlink(filepath.Dir(root), ancestorAlias); err != nil {
		t.Fatal(err)
	}
	aliasedRoot := filepath.Join(ancestorAlias, filepath.Base(root))
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if aliasedRoot == canonicalRoot {
		t.Fatal("fixture did not produce an ancestor-path alias")
	}
	snapshot, err := readSourceSnapshot(context.Background(), aliasedRoot, version, commit)
	if err != nil || snapshot.root != canonicalRoot {
		t.Fatal("checkout ancestor alias was not resolved to the physical Git root", snapshot.root, err)
	}
	output := filepath.Join(t.TempDir(), "assets")
	release, err := CreateSourceAssets(context.Background(), aliasedRoot, output, version, commit)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifySourceAssets(context.Background(), canonicalRoot, output, version, commit)
	if err != nil || verified.Tree != release.Tree {
		t.Fatal("aliased checkout changed source companion verification", err)
	}
	for _, directory := range []string{filepath.Join(root, "guide"), filepath.Join(aliasedRoot, "guide")} {
		if _, err := CheckSourceCheckout(context.Background(), directory, version, commit); err == nil ||
			!strings.Contains(err.Error(), "source must be the Git checkout root") {
			t.Fatal("checkout subdirectory was not refused", err)
		}
	}
	directAlias := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, directAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckSourceCheckout(context.Background(), directAlias, version, commit); err == nil ||
		!strings.Contains(err.Error(), "source checkout must be a real directory") {
		t.Fatal("direct symlink checkout root was not refused", err)
	}
}

func TestSourceAssetsExactTreeArchiveBundleAndNoLocalLeakage(t *testing.T) {
	root, version, commit, parent := sourceFixture(t)
	secret := []byte("PRIVATE-UNTRACKED-CONTENT-NOT-FOR-PUBLICATION")
	for _, name := range []string{".local/identity/private.key", "untracked-secret.txt", "ignored.txt"} {
		sourceTestWrite(t, filepath.Join(root, filepath.FromSlash(name)), secret)
	}
	cmd := sourceCommand(context.Background(), root, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(secret)
	object, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "assets")
	release, err := CreateSourceAssets(context.Background(), root, output, version, commit)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifySourceAssets(context.Background(), root, output, version, commit)
	if err != nil || verified.Tree != release.Tree || verified.Commit != commit || verified.Version != version ||
		verified.FormatVersion != 1 || verified.OfflineDependencyLimitation == "" || verified.BundleRules == "" || verified.ArchiveGoVersion == "" {
		t.Fatal("source companion contract did not round-trip", verified, err)
	}
	names := SourceAssetNames(version)
	for i, artifact := range release.Artifacts {
		info, err := os.Stat(filepath.Join(output, artifact.Name))
		digest, hashErr := SHA256(filepath.Join(output, artifact.Name))
		if err != nil || hashErr != nil || artifact.Name != names[i] || artifact.Size != info.Size() || artifact.SHA256 != digest {
			t.Fatal("metadata did not bind actual source asset bytes", artifact, err, hashErr)
		}
	}
	extracted := sourceTestExtract(t, filepath.Join(output, names[0]), t.TempDir(), release.ArchivePrefix)
	sourceTestGit(t, extracted, "init", "--quiet")
	sourceTestGit(t, extracted, "add", "--all")
	if tree := sourceTestGit(t, extracted, "write-tree"); tree != release.Tree {
		t.Fatal("source archive did not reconstruct the exact tracked release tree", tree, release.Tree)
	}
	for _, name := range []string{".local", "ignored.txt", "untracked-secret.txt", "ancestry.txt"} {
		if _, err := os.Lstat(filepath.Join(extracted, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatal("archive leaked a local or historical-only path", name, err)
		}
	}
	for name, expected := range map[string]string{"guide/exported.md": "must not be omitted\n", "guide/subst.md": "$Format:%H$\n"} {
		data, err := os.ReadFile(filepath.Join(extracted, filepath.FromSlash(name)))
		if err != nil || string(data) != expected {
			t.Fatal("Git export attributes changed actual source inputs", name, err)
		}
	}
	clone := filepath.Join(t.TempDir(), "clone")
	sourceTestGit(t, root, "-c", "protocol.file.allow=always", "clone", "--quiet", filepath.Join(output, names[1]), clone)
	sourceTestGit(t, clone, "checkout", "--quiet", "--detach", commit)
	if tree := sourceTestGit(t, clone, "rev-parse", "HEAD^{tree}"); tree != release.Tree {
		t.Fatal("bundle clone changed the release tree", tree)
	}
	if got := sourceTestGit(t, clone, "show", parent+":ancestry.txt"); got != "real retained ancestor" {
		t.Fatal("bundle replaced real release ancestry", got)
	}
	if _, err := sourceGit(context.Background(), clone, "cat-file", "-e", strings.TrimSpace(string(object))); err == nil {
		t.Fatal("bundle leaked an unrelated local private Git object")
	}
	again, err := CreateSourceAssets(context.Background(), root, filepath.Join(t.TempDir(), "again"), version, commit)
	if err != nil || again.Artifacts[0].SHA256 != release.Artifacts[0].SHA256 {
		t.Fatal("source archive is not deterministic under its declared rules", err)
	}
	if _, err := CreateSourceAssets(context.Background(), root, output, version, commit); err == nil {
		t.Fatal("immutable source assets were overwritten")
	}
}

func sourceTestExtract(t *testing.T, archiveName, destination, prefix string) string {
	t.Helper()
	file, err := os.Open(archiveName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !strings.HasPrefix(header.Name, prefix) || strings.Contains(header.Name, ".git/") {
			t.Fatal("unexpected source archive path", header, err)
		}
		name := filepath.Join(destination, filepath.FromSlash(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(name, os.FileMode(header.Mode)); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			data, err := io.ReadAll(archive)
			if err != nil || bytes.Contains(data, []byte("PRIVATE-UNTRACKED-CONTENT-NOT-FOR-PUBLICATION")) {
				t.Fatal("source archive leaked local bytes", err)
			}
			if err := os.WriteFile(name, data, os.FileMode(header.Mode)); err != nil {
				t.Fatal(err)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, name); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("source archive contains unsupported file types", header)
		}
	}
	return filepath.Join(destination, strings.TrimSuffix(prefix, "/"))
}

func TestSourceCheckoutRefusesDirtyTrackedAndUnresolvedInputs(t *testing.T) {
	for _, corruption := range []string{"bytes", "index", "missing", "mode", "symlink", "parent-symlink", "assume-unchanged", "skip-worktree",
		"wrong-commit", "wrong-version", "shallow", "grafts", "submodule", "lfs", "tracked-private", "missing-module", "local-module", "untracked-link"} {
		t.Run(corruption, func(t *testing.T) {
			root, version, commit, parent := sourceFixture(t)
			switch corruption {
			case "bytes", "index", "assume-unchanged", "skip-worktree":
				if corruption == "assume-unchanged" || corruption == "skip-worktree" {
					sourceTestGit(t, root, "update-index", "--"+corruption, "main.go")
				}
				sourceTestWrite(t, filepath.Join(root, "main.go"), []byte("package main\nfunc nope() {}\n"))
				if corruption == "index" {
					sourceTestGit(t, root, "add", "main.go")
				}
			case "missing":
				if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(filepath.Join(root, "main.go"), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("README.md", filepath.Join(root, "main.go")); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				directory := t.TempDir()
				if err := os.Rename(filepath.Join(root, "guide"), filepath.Join(directory, "guide")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(directory, "guide"), filepath.Join(root, "guide")); err != nil {
					t.Fatal(err)
				}
			case "wrong-commit":
				commit = parent
			case "wrong-version":
				version = "0.3.46"
			case "shallow":
				sourceTestWrite(t, filepath.Join(root, ".git", "shallow"), []byte(commit+"\n"))
			case "grafts":
				sourceTestWrite(t, filepath.Join(root, ".git", "info", "grafts"), []byte(commit+"\n"))
			case "submodule":
				sourceTestGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+parent+",submodule")
				commit = sourceTestCommit(t, root)
			case "lfs":
				sourceTestWrite(t, filepath.Join(root, "asset.lfs"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 20\n"))
				sourceTestGit(t, root, "add", "asset.lfs")
				commit = sourceTestCommit(t, root)
			case "tracked-private":
				sourceTestWrite(t, filepath.Join(root, ".local", "private.key"), []byte("private state"))
				sourceTestGit(t, root, "add", "-f", ".local/private.key")
				commit = sourceTestCommit(t, root)
			case "missing-module":
				sourceTestGit(t, root, "rm", "--quiet", "go.sum")
				commit = sourceTestCommit(t, root)
			case "local-module":
				sourceTestWrite(t, filepath.Join(root, "go.mod"), []byte("module fixture.invalid/fdn\n\ngo 1.27\n\nreplace example.invalid/lib => ../private\n"))
				sourceTestGit(t, root, "add", "go.mod")
				commit = sourceTestCommit(t, root)
			case "untracked-link":
				if err := os.Symlink("untracked.go", filepath.Join(root, "link.go")); err != nil {
					t.Fatal(err)
				}
				sourceTestWrite(t, filepath.Join(root, "untracked.go"), []byte("package main\n"))
				sourceTestGit(t, root, "add", "link.go")
				commit = sourceTestCommit(t, root)
			}
			output := filepath.Join(t.TempDir(), "must-not-exist")
			if _, err := CreateSourceAssets(context.Background(), root, output, version, commit); err == nil {
				t.Fatal("uncertified source checkout was accepted", corruption)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("failed source validation left publishable output", err)
			}
		})
	}
}

func TestSourceBuildInputsRefuseUntrackedAndIgnoredNativeInputs(t *testing.T) {
	root, _, _, _ := sourceFixture(t)
	sourceTestWrite(t, filepath.Join(root, ".local", "private.key"), []byte("private state"))
	if err := CheckTrackedBuildInputs(context.Background(), root); err != nil {
		t.Fatal("private test state was treated as a build input", err)
	}
	sourceTestWrite(t, filepath.Join(root, "ignored.txt"), []byte("untracked embedded data"))
	if err := CheckTrackedBuildInputs(context.Background(), root); err == nil {
		t.Fatal("ignored extra build inputs were accepted")
	}
	if err := os.Remove(filepath.Join(root, "ignored.txt")); err != nil {
		t.Fatal(err)
	}
	sourceTestWrite(t, filepath.Join(root, "extra.go"), []byte("package main\n"))
	if err := CheckTrackedBuildInputs(context.Background(), root); err == nil {
		t.Fatal("untracked Go build input was accepted")
	}
}

func TestSourceAssetsRefuseTamperingEvenWithRehashedManifest(t *testing.T) {
	for _, corruption := range []string{"version", "commit", "tree", "format", "offline", "size", "hash", "name",
		"archive", "bundle", "checksums", "missing", "extra", "symlink", "rehashed-archive", "rehashed-thin-bundle", "oversized-metadata", "oversized-checksums"} {
		t.Run(corruption, func(t *testing.T) {
			root, version, commit, parent := sourceFixture(t)
			output := filepath.Join(t.TempDir(), "assets")
			release, err := CreateSourceAssets(context.Background(), root, output, version, commit)
			if err != nil {
				t.Fatal(err)
			}
			names := SourceAssetNames(version)
			switch corruption {
			case "version":
				release.Version = "0.3.46"
			case "commit":
				release.Commit = parent
			case "tree":
				release.Tree = strings.Repeat("a", 40)
			case "format":
				release.FormatVersion++
			case "offline":
				release.OfflineDependencyLimitation = "offline build guaranteed"
			case "size":
				release.Artifacts[0].Size++
			case "hash":
				release.Artifacts[0].SHA256 = strings.Repeat("a", 64)
			case "name":
				release.Artifacts[0].Name = "../unsafe"
			case "archive", "bundle", "checksums":
				index := map[string]int{"archive": 0, "bundle": 1, "checksums": 3}[corruption]
				sourceTestWrite(t, filepath.Join(output, names[index]), []byte("tampered"))
			case "missing":
				if err := os.Remove(filepath.Join(output, names[1])); err != nil {
					t.Fatal(err)
				}
			case "extra":
				sourceTestWrite(t, filepath.Join(output, "unexpected.txt"), []byte("extra asset"))
			case "oversized-metadata":
				sourceTestWrite(t, filepath.Join(output, names[2]), bytes.Repeat([]byte(" "), (64<<10)+1))
			case "oversized-checksums":
				sourceTestWrite(t, filepath.Join(output, names[3]), bytes.Repeat([]byte(" "), 64<<10))
			case "symlink":
				if err := os.Remove(filepath.Join(output, names[1])); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(names[0], filepath.Join(output, names[1])); err != nil {
					t.Fatal(err)
				}
			case "rehashed-archive":
				snapshot, err := readSourceSnapshot(context.Background(), root, version, commit)
				if err != nil {
					t.Fatal(err)
				}
				snapshot.files[0].data = []byte("forged source input")
				var archive bytes.Buffer
				if err := writeSourceArchive(&archive, snapshot); err != nil {
					t.Fatal(err)
				}
				sourceTestWrite(t, filepath.Join(output, names[0]), archive.Bytes())
				release.Artifacts[0], err = sourceArtifact(output, names[0])
				if err != nil {
					t.Fatal(err)
				}
			case "rehashed-thin-bundle":
				if err := os.Remove(filepath.Join(output, names[1])); err != nil {
					t.Fatal(err)
				}
				sourceTestGit(t, root, "bundle", "create", "--version=2", filepath.Join(output, names[1]), "HEAD", "^"+parent)
				release.Artifacts[1], err = sourceArtifact(output, names[1])
				if err != nil {
					t.Fatal(err)
				}
			}
			if slices.Contains([]string{"version", "commit", "tree", "format", "offline", "size", "hash", "name", "rehashed-archive", "rehashed-thin-bundle"}, corruption) {
				data, err := json.Marshal(release)
				if err != nil {
					t.Fatal(err)
				}
				sourceTestWrite(t, filepath.Join(output, names[2]), append(data, '\n'))
				checksums, err := sourceChecksums(output, names[:3])
				if err != nil {
					t.Fatal(err)
				}
				sourceTestWrite(t, filepath.Join(output, names[3]), checksums)
			}
			if _, err := VerifySourceAssets(context.Background(), root, output, version, commit); err == nil {
				t.Fatal("tampered source companions were accepted", corruption)
			}
		})
	}
}
