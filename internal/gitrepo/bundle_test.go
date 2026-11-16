package gitrepo

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type gitFixture struct {
	root string
	repo string
	git  string
	env  []string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("native Git is required only for disposable bundle fixtures")
	}
	root, err := os.MkdirTemp(".", ".gitrepo-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	f := &gitFixture{root: root, repo: filepath.Join(root, "repo"), git: git}
	for _, name := range []string{"repo", "home", "hooks", "scratch"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "empty-config")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") || name == "HOME" || name == "XDG_CONFIG_HOME" ||
			name == "TMPDIR" || name == "LC_ALL" {
			continue
		}
		f.env = append(f.env, entry)
	}
	f.env = append(f.env,
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "home"),
		"TMPDIR="+filepath.Join(root, "scratch"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+config, "GIT_CONFIG_COUNT=0",
		"GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_DATE=1700000000 +0000", "GIT_COMMITTER_DATE=1700000000 +0000",
		"LC_ALL=C",
	)
	f.run(t, "init", "--object-format=sha1")
	return f
}

func (f *gitFixture) run(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	options := []string{
		"--no-pager",
		"-c", "core.hooksPath=" + filepath.Join(f.root, "hooks"),
		"-c", "user.name=Bundle Fixture", "-c", "user.email=bundle@example.invalid",
		"-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false",
		"-c", "init.defaultBranch=main", "-c", "pack.window=50", "-c", "pack.depth=50",
	}
	cmd := exec.CommandContext(ctx, f.git, append(options, args...)...)
	cmd.Dir = f.repo
	cmd.Env = f.env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, output)
	}
	return output
}

func (f *gitFixture) write(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(f.repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func (f *gitFixture) commit(t *testing.T, message string) string {
	t.Helper()
	f.run(t, "add", "--all")
	f.run(t, "commit", "-m", message)
	return strings.TrimSpace(string(f.run(t, "rev-parse", "HEAD")))
}

func (f *gitFixture) bundle(t *testing.T, version int, revs ...string) ([]byte, string) {
	t.Helper()
	path := filepath.Join(f.root, fmt.Sprintf("source-v%d-%d.bundle", version, time.Now().UnixNano()))
	args := []string{"bundle", "create", fmt.Sprintf("--version=%d", version), path}
	f.run(t, append(args, revs...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, path
}

func TestBundleRealGitHistoryAndReads(t *testing.T) {
	f := newGitFixture(t)
	file := []byte{0, 1, 2, 255, '\r', '\n', 0, 'z'}
	recipe := []byte("all:\n\tprintf 'fixture\\n'\n")
	f.write(t, "README.md", []byte("base\n"), 0600)
	f.write(t, "recipes/Makefile", recipe, 0600)
	f.write(t, "bytes.bin", file, 0600)
	f.write(t, "empty", nil, 0600)
	f.write(t, "run", []byte("#!/bin/sh\nexit 0\n"), 0700)
	if err := os.Symlink("recipes/Makefile", filepath.Join(f.repo, "recipe-link")); err != nil {
		t.Fatal(err)
	}
	root := f.commit(t, "root")
	f.run(t, "tag", "root", root)
	for i := 0; i < 12; i++ {
		content := []byte(strings.Repeat("original bundle fixture block\n", 8192) + fmt.Sprintf("revision %d\n", i))
		f.write(t, "history.txt", content, 0600)
		f.commit(t, fmt.Sprintf("history %d", i))
	}
	mainParent := strings.TrimSpace(string(f.run(t, "rev-parse", "HEAD")))
	f.run(t, "switch", "-c", "feature", root)
	f.write(t, "feature.txt", []byte("independent feature\n"), 0600)
	feature := f.commit(t, "feature")
	f.run(t, "switch", "main")
	f.run(t, "merge", "--no-ff", "-m", "merge feature", "feature")
	head := strings.TrimSpace(string(f.run(t, "rev-parse", "HEAD")))
	f.run(t, "switch", "--orphan", "unrelated")
	f.run(t, "rm", "-rf", "--ignore-unmatch", ".")
	f.write(t, "other.txt", []byte("unrelated history\n"), 0600)
	unrelated := f.commit(t, "unrelated root")
	f.run(t, "switch", "main")

	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			data, path := f.bundle(t, version, "--all")
			b, err := Open(data)
			if err != nil {
				t.Fatal(err)
			}
			wantRefs := map[string]string{"HEAD": head}
			lines := strings.Split(strings.TrimSpace(string(f.run(t, "for-each-ref", "--format=%(objectname) %(refname)"))), "\n")
			for _, line := range lines {
				value, name, _ := strings.Cut(line, " ")
				wantRefs[name] = value
			}
			if !reflect.DeepEqual(b.Refs(), wantRefs) {
				t.Fatalf("refs = %v, want %v", b.Refs(), wantRefs)
			}
			for _, commit := range []string{root, mainParent, feature, head, unrelated} {
				tree, err := b.Tree(commit)
				wantTree := strings.TrimSpace(string(f.run(t, "rev-parse", commit+"^{tree}")))
				if err != nil || tree != wantTree {
					t.Fatalf("tree = %q, %v; want %q", tree, err, wantTree)
				}
				parents, err := b.Parents(commit)
				wantParents := strings.Fields(string(f.run(t, "show", "-s", "--format=%P", commit)))
				if err != nil || !reflect.DeepEqual(parents, wantParents) {
					t.Fatalf("parents = %v, %v; want %v", parents, err, wantParents)
				}
				id, _ := decodeID([]byte(commit))
				body := f.run(t, "cat-file", "commit", commit)
				hash := sha1.Sum(append([]byte(fmt.Sprintf("commit %d\x00", len(body))), body...))
				if hash != id || !bytes.Equal(b.objects[id].data, body) {
					t.Fatal("commit bytes do not prove the native Git object ID")
				}
				treeBody := f.run(t, "cat-file", "tree", tree)
				treeID, _ := decodeID([]byte(tree))
				treeHash := sha1.Sum(append([]byte(fmt.Sprintf("tree %d\x00", len(treeBody))), treeBody...))
				if treeHash != treeID {
					t.Fatal("tree proof does not match native Git")
				}
			}
			for _, test := range []struct {
				ancestor string
				commit   string
				want     bool
			}{
				{root, head, true}, {feature, head, true}, {mainParent, head, true},
				{head, head, true}, {head, root, false}, {feature, mainParent, false},
				{unrelated, head, false}, {root, unrelated, false},
			} {
				got, err := b.IsAncestor(test.ancestor, test.commit)
				if err != nil || got != test.want {
					t.Fatalf("ancestry = %v, %v; want %v", got, err, test.want)
				}
			}
			for _, test := range []struct {
				path string
				want []byte
			}{{"bytes.bin", file}, {"recipes/Makefile", recipe}, {"run", []byte("#!/bin/sh\nexit 0\n")}, {"empty", []byte{}}} {
				got, err := b.File(head, test.path, int64(len(test.want)))
				if err != nil || !bytes.Equal(got, test.want) {
					t.Fatalf("file %s = %q, %v; want %q", test.path, got, err, test.want)
				}
			}
			got, err := b.File(root, "bytes.bin", math.MaxInt64)
			if err != nil {
				t.Fatal(err)
			}
			got[0] = 99
			again, _ := b.File(root, "bytes.bin", int64(len(file)))
			if !bytes.Equal(again, file) {
				t.Fatal("file read aliases retained bytes")
			}
			refs := b.Refs()
			refs["HEAD"] = root
			delete(refs, "refs/heads/main")
			parents, _ := b.Parents(head)
			parents[0] = unrelated
			againParents, _ := b.Parents(head)
			if b.Refs()["HEAD"] != head || againParents[0] != mainParent {
				t.Fatal("returned metadata aliases bundle state")
			}
			for _, bad := range []string{
				"/README.md", "../README.md", "recipes/../README.md", "./README.md",
				"recipes//Makefile", "recipes/./Makefile", "recipes\\Makefile",
				"C:/README.md", ".git/config", "README.md\x00", "missing",
				"recipes", "recipe-link", "recipe-link/Makefile", "README.md/child",
			} {
				if _, err := b.File(head, bad, 4096); err == nil {
					t.Errorf("unsafe/nonregular path %q was accepted", bad)
				}
			}
			if _, err := b.File(head, "bytes.bin", int64(len(file)-1)); err == nil {
				t.Fatal("file was silently truncated instead of refusing the limit")
			}
			if _, err := b.File(head, "empty", -1); err == nil {
				t.Fatal("negative file limit was accepted")
			}
			clone := filepath.Join(f.root, fmt.Sprintf("clone-v%d", version))
			f.run(t, "clone", "--no-local", path, clone)
			f.run(t, "-C", clone, "fsck", "--full", "--strict")
			originalCount := f.run(t, "rev-list", "--all", "--count")
			cloneCount := f.run(t, "-C", clone, "rev-list", "--all", "--count")
			if !bytes.Equal(originalCount, cloneCount) {
				t.Fatalf("clone lost history: %s != %s", cloneCount, originalCount)
			}
			clear(data)
			if tree, err := b.Tree(head); err != nil || !Commit(tree) {
				t.Fatal("bundle retained caller-owned input")
			}
			checkConcurrentReads(t, b, root, head, recipe)
		})
	}
}

func checkConcurrentReads(t *testing.T, b *Bundle, root, head string, recipe []byte) {
	t.Helper()
	var workers sync.WaitGroup
	failures := make(chan string, 32)
	for i := 0; i < 32; i++ {
		workers.Go(func() {
			for range 16 {
				file, err := b.File(head, "recipes/Makefile", 4096)
				if err != nil || !bytes.Equal(file, recipe) {
					failures <- "concurrent file read failed"
					return
				}
				if yes, err := b.IsAncestor(root, head); err != nil || !yes {
					failures <- "concurrent ancestry failed"
					return
				}
				refs := b.Refs()
				refs["HEAD"] = root
				if _, err := b.Parents(head); err != nil {
					failures <- "concurrent parents failed"
					return
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestBundleRejectsNativePrerequisitesAndAnnotatedRefs(t *testing.T) {
	f := newGitFixture(t)
	f.write(t, "file", []byte("first\n"), 0600)
	root := f.commit(t, "first")
	f.write(t, "file", []byte("second\n"), 0600)
	f.commit(t, "second")
	partial, _ := f.bundle(t, 2, root+"..HEAD")
	if _, err := Open(partial); err == nil || !strings.Contains(err.Error(), "prerequisites") {
		t.Fatalf("incremental bundle was accepted: %v", err)
	}
	end := bytes.Index(partial, []byte("\n\n")) + 2
	var stripped []byte
	for _, line := range bytes.Split(partial[:end-1], []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("-")) {
			stripped = append(stripped, line...)
			stripped = append(stripped, '\n')
		}
	}
	stripped = append(stripped, partial[end:]...)
	if _, err := Open(stripped); err == nil {
		t.Fatal("removing prerequisite declarations hid an incomplete history")
	}
	f.run(t, "tag", "-a", "annotated", "-m", "original fixture tag")
	annotated, _ := f.bundle(t, 3, "--all")
	if _, err := Open(annotated); err == nil {
		t.Fatal("annotated tag ref was accepted as a commit")
	}
}

func TestCommitAndRefSyntax(t *testing.T) {
	for _, value := range []string{strings.Repeat("0", 40), strings.Repeat("a", 40), strings.Repeat("0123456789abcdef", 2) + "01234567"} {
		if !Commit(value) {
			t.Errorf("valid commit syntax rejected: %q", value)
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 39) + "\n"} {
		if Commit(value) {
			t.Errorf("invalid commit syntax accepted: %q", value)
		}
	}
	for _, name := range []string{"HEAD", "refs/heads/main", "refs/tags/v1.2", "refs/heads/topic/nested", "refs/heads/한글"} {
		if err := ValidateRef(name); err != nil {
			t.Errorf("valid ref rejected: %q: %v", name, err)
		}
	}
	for _, name := range []string{
		"", "main", "refs/", "refs//main", "/refs/heads/main", "refs/heads/main/",
		"refs/heads/.hidden", "refs/heads/a..b", "refs/heads/a.lock",
		"refs/heads/a.", "refs/heads/a b", "refs/heads/a\tb", "refs/heads/a\x7fb",
		"refs/heads/a\x00b", "refs/heads/a~b", "refs/heads/a^b", "refs/heads/a:b",
		"refs/heads/a?b", "refs/heads/a*b", "refs/heads/a[b", "refs/heads/a\\b",
		"refs/heads/a@{b", "refs/heads/\xff", "refs/heads/a\u200eb",
		"refs/heads/" + strings.Repeat("x", maxComponent+1),
		"refs/heads/" + strings.Repeat("a/", maxPathDepth),
		strings.Repeat("x", maxRefBytes+1),
	} {
		if err := ValidateRef(name); err == nil {
			t.Errorf("invalid ref accepted: %q", name)
		}
	}
}

func TestBundleMissingCommitAndNilReceiver(t *testing.T) {
	data, head, tree := basicBundle(t)
	b, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"HEAD", strings.ToUpper(head), strings.Repeat("0", 40), tree.String()} {
		if _, err := b.Tree(invalid); err == nil {
			t.Errorf("invalid Tree commit %q accepted", invalid)
		}
		if _, err := b.Parents(invalid); err == nil {
			t.Errorf("invalid Parents commit %q accepted", invalid)
		}
		if _, err := b.File(invalid, "file", 4096); err == nil {
			t.Errorf("invalid File commit %q accepted", invalid)
		}
		if _, err := b.IsAncestor(invalid, head); err == nil {
			t.Errorf("invalid ancestor %q accepted", invalid)
		}
		if _, err := b.IsAncestor(head, invalid); err == nil {
			t.Errorf("invalid target %q accepted", invalid)
		}
	}
	var absent *Bundle
	if absent.Refs() != nil {
		t.Fatal("nil bundle unexpectedly has refs")
	}
	if _, err := absent.Tree(head); err == nil {
		t.Fatal("nil bundle Tree succeeded")
	}
	if _, err := absent.Parents(head); err == nil {
		t.Fatal("nil bundle Parents succeeded")
	}
	if _, err := absent.File(head, "file", 1); err == nil {
		t.Fatal("nil bundle File succeeded")
	}
	if _, err := absent.IsAncestor(head, head); err == nil {
		t.Fatal("nil bundle ancestry succeeded")
	}
}
