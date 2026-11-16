package distribution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const SourceFormatVersion = 1

const sourceArchiveRules = "All release-tree blobs, including export-ignore/export-subst paths, sorted by Git path; canonical UTF-8 relative paths; symlinks resolvable solely in this tree within 40 hops; no filesystem Go module replacements; each blob at most 256 MiB; relative source prefix; Git executable/symlink modes; parent directories 0755; uid/gid 0; commit timestamp; Go tar PAX and gzip.BestCompression with no gzip timestamp/name/comment."
const sourceBundleRules = "Git bundle v2 of HEAD only, with the exact release commit and all reachable ancestor objects, no prerequisites or other refs; pack.threads=1, pack.window=0, pack.depth=0, core.compression=9. Bundle bytes are not promised reproducible across Git versions, repository packing or other Git defaults; the actual Git version, size and SHA-256 are recorded."
const sourceOfflineLimitation = "Only tracked Foundation source, tests, manifests, guides and notices plus release Git history are supplied. Go module, provider and toolchain caches are excluded. go.mod/go.sum retain dependency pins; compatible Go/native toolchains and external dependencies not tracked in this tree must be obtained separately. This is not a certified offline-dependency closure or offline-build guarantee."

var sourceVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var sourceObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sourceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SourceIdentity struct {
	Version         string
	Commit          string
	Tree            string
	SourceDateEpoch int64
}

type SourceArtifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SourceRelease is a companion contract, not a change to the native ZIP format.
type SourceRelease struct {
	FormatVersion               int              `json:"format_version"`
	Version                     string           `json:"version"`
	Commit                      string           `json:"commit"`
	Tree                        string           `json:"tree"`
	SourceDateEpoch             int64            `json:"source_date_epoch"`
	ArchivePrefix               string           `json:"archive_prefix"`
	ArchiveRules                string           `json:"archive_rules"`
	ArchiveGoVersion            string           `json:"archive_go_version"`
	BundleRules                 string           `json:"bundle_rules"`
	GitVersion                  string           `json:"git_version"`
	OfflineDependencyLimitation string           `json:"offline_dependency_limitation"`
	Artifacts                   []SourceArtifact `json:"artifacts"`
}

// SourceAssetNames returns the archive, bundle, metadata and checksum companions
// for an already validated version.
// Metadata hashes the two payloads; the checksum companion also hashes metadata.
func SourceAssetNames(version string) []string {
	base := "fdn-" + version + "-source"
	return []string{base + ".tar.gz", base + ".bundle", base + ".json", base + ".SHA256SUMS"}
}

type sourceFile struct {
	name, mode, object string
	data               []byte
}

type sourceSnapshot struct {
	SourceIdentity
	root  string
	files []sourceFile
}

// CheckSourceCheckout checks HEAD, the index and raw tracked working-tree bytes.
// It never trusts status flags or Git clean/export filters, and never reads
// untracked files into the source snapshot.
func CheckSourceCheckout(ctx context.Context, root, version, commit string) (SourceIdentity, error) {
	snapshot, err := readSourceSnapshot(ctx, root, version, commit)
	return snapshot.SourceIdentity, err
}

// CheckTrackedBuildInputs refuses additional local inputs before native builds.
// Only known output/cache/private-state directories outside the native source
// packages are exempt; their bytes are never included in source companions.
func CheckTrackedBuildInputs(ctx context.Context, root string) error {
	for _, options := range [][]string{
		{"ls-files", "--others", "--exclude-standard", "-z"},
		{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"},
	} {
		data, err := sourceGit(ctx, root, options...)
		if err != nil {
			return err
		}
		for _, name := range strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00") {
			if name == "" {
				continue
			}
			first, _, _ := strings.Cut(name, "/")
			if slices.Contains([]string{".local", ".git", ".cache", ".foundation", "bin", "dist", "publish"}, first) {
				continue
			}
			return fmt.Errorf("native build input is not tracked in the release: %s", name)
		}
	}
	return nil
}

func sourceCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	options := []string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never"}
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	return cmd
}

func sourceGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := sourceCommand(ctx, root, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("source git %s failed: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

func readSourceSnapshot(ctx context.Context, root, version, commit string) (sourceSnapshot, error) {
	snapshot := sourceSnapshot{SourceIdentity: SourceIdentity{Version: version, Commit: commit}}
	if !sourceVersionPattern.MatchString(version) || !sourceObjectPattern.MatchString(commit) {
		return snapshot, errors.New("source requires an exact version and release commit")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return snapshot, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return snapshot, errors.New("source checkout must be a real directory")
	}
	// Git reports a physical checkout path, while macOS callers may use an
	// ancestor alias such as /var. Reject a symlink root before resolving aliases.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return snapshot, err
	}
	snapshot.root = root
	top, err := sourceGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return snapshot, err
	}
	checkoutRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return snapshot, err
	}
	if checkoutRoot != root {
		return snapshot, errors.New("source must be the Git checkout root")
	}
	head, err := sourceGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != commit {
		return snapshot, errors.New("source checkout is not the exact release commit")
	}
	shallow, err := sourceGit(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(string(shallow)) != "false" {
		return snapshot, errors.New("source delivery requires complete, non-shallow release ancestry")
	}
	grafts, err := sourceGit(ctx, root, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return snapshot, err
	}
	graftPath := strings.TrimSpace(string(grafts))
	if !filepath.IsAbs(graftPath) {
		graftPath = filepath.Join(root, graftPath)
	}
	if _, err := os.Lstat(graftPath); !errors.Is(err, os.ErrNotExist) {
		return snapshot, errors.New("source ancestry cannot use local Git grafts")
	}
	tree, err := sourceGit(ctx, root, "rev-parse", commit+"^{tree}")
	if err != nil {
		return snapshot, err
	}
	snapshot.Tree = strings.TrimSpace(string(tree))
	if !sourceObjectPattern.MatchString(snapshot.Tree) {
		return snapshot, errors.New("source tree is not a Git SHA-1 tree")
	}
	epoch, err := sourceGit(ctx, root, "show", "-s", "--format=%ct", commit)
	if err != nil {
		return snapshot, err
	}
	snapshot.SourceDateEpoch, err = strconv.ParseInt(strings.TrimSpace(string(epoch)), 10, 64)
	if err != nil || snapshot.SourceDateEpoch < 0 {
		return snapshot, errors.New("source commit timestamp is invalid")
	}
	entries, err := sourceGit(ctx, root, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return snapshot, err
	}
	for _, entry := range bytes.Split(bytes.TrimSuffix(entries, []byte{0}), []byte{0}) {
		header, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !sourceObjectPattern.MatchString(fields[2]) {
			return snapshot, errors.New("invalid release Git tree entry")
		}
		if fields[0] == "160000" {
			return snapshot, errors.New("submodule inputs cannot be delivered as a complete source snapshot")
		}
		if fields[1] != "blob" || !slices.Contains([]string{"100644", "100755", "120000"}, fields[0]) {
			return snapshot, errors.New("unsupported source Git tree mode")
		}
		if err := checkSourcePath(name); err != nil {
			return snapshot, err
		}
		snapshot.files = append(snapshot.files, sourceFile{name: name, mode: fields[0], object: fields[2]})
	}
	if err := readSourceBlobs(ctx, &snapshot); err != nil {
		return snapshot, err
	}
	required := map[string]bool{"VERSION": false, "go.mod": false, "go.sum": false}
	for _, file := range snapshot.files {
		if _, ok := required[file.name]; ok {
			if file.mode != "100644" {
				return snapshot, errors.New("source version/module manifests must be tracked regular files")
			}
			required[file.name] = true
		}
		if file.name == "VERSION" && strings.TrimSpace(string(file.data)) != version {
			return snapshot, errors.New("source VERSION does not match the requested release")
		}
		if file.name == "go.mod" {
			if err := checkSourceModule(file.data); err != nil {
				return snapshot, err
			}
		}
		if bytes.HasPrefix(file.data, []byte("version https://git-lfs.github.com/spec/")) {
			return snapshot, errors.New("unresolved Git LFS inputs cannot be delivered as complete source")
		}
		if file.mode == "120000" && !safeSourceLink(file.name, string(file.data)) {
			return snapshot, errors.New("source symlink escapes the archive or has a nonportable target")
		}
	}
	for name, present := range required {
		if !present {
			return snapshot, fmt.Errorf("source is missing tracked %s", name)
		}
	}
	if err := checkSourceLinks(snapshot.files); err != nil {
		return snapshot, err
	}
	index, err := sourceGit(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return snapshot, err
	}
	expected := map[string]sourceFile{}
	for _, file := range snapshot.files {
		expected[file.name] = file
	}
	indexEntries := bytes.Split(bytes.TrimSuffix(index, []byte{0}), []byte{0})
	if len(indexEntries) != len(expected) {
		return snapshot, errors.New("source index differs from the release tree")
	}
	for _, entry := range indexEntries {
		header, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(header)
		file, exists := expected[name]
		if !ok || len(fields) != 3 || !exists || fields[2] != "0" || fields[0] != file.mode || fields[1] != file.object {
			return snapshot, errors.New("source index differs from the release tree")
		}
		delete(expected, name)
	}
	for _, file := range snapshot.files {
		if err := checkSourceWorkingFile(root, file); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func readSourceBlobs(ctx context.Context, snapshot *sourceSnapshot) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := sourceCommand(ctx, snapshot.root, "cat-file", "--batch")
	var requests strings.Builder
	for _, file := range snapshot.files {
		fmt.Fprintln(&requests, file.object)
	}
	cmd.Stdin = strings.NewReader(requests.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := func() error {
		reader := bufio.NewReader(stdout)
		for i := range snapshot.files {
			header, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[0] != snapshot.files[i].object || fields[1] != "blob" {
				return errors.New("source blob is missing or has the wrong type")
			}
			size, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil || size < 0 || size > 256<<20 {
				return errors.New("source blob exceeds the 256 MiB portable snapshot limit")
			}
			data := make([]byte, int(size))
			if _, err := io.ReadFull(reader, data); err != nil {
				return err
			}
			if separator, err := reader.ReadByte(); err != nil || separator != '\n' {
				return errors.New("invalid source blob framing")
			}
			digest := sha1.New()
			fmt.Fprintf(digest, "blob %d\x00", len(data))
			digest.Write(data)
			if hex.EncodeToString(digest.Sum(nil)) != snapshot.files[i].object {
				return errors.New("source blob does not match its Git object")
			}
			snapshot.files[i].data = data
		}
		return nil
	}()
	if readErr != nil {
		cancel()
	}
	return errors.Join(readErr, cmd.Wait())
}

func checkSourcePath(name string) error {
	if name == "" || name == "." || path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") ||
		!utf8.ValidString(name) || strings.Contains(name, "\\") || sourceHasControl(name) {
		return errors.New("source paths must be canonical, portable and relative")
	}
	for _, component := range strings.Split(name, "/") {
		if slices.Contains([]string{".git", ".local", ".foundation", ".ssh", ".aws", ".azure", ".cache", "__pycache__", "node_modules"}, component) {
			return errors.New("tracked private state or cache cannot be published as source")
		}
	}
	first, _, _ := strings.Cut(name, "/")
	if slices.Contains([]string{"bin", "dist", "publish", ".git-credentials", ".netrc", ".env"}, first) {
		return errors.New("tracked runtime/output/private inputs cannot be published as source")
	}
	return nil
}

func safeSourceLink(name, target string) bool {
	if target == "" || path.IsAbs(target) || !utf8.ValidString(target) || strings.Contains(target, "\\") || sourceHasControl(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../") && checkSourcePath(resolved) == nil
}

func sourceHasControl(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return r < 32 || r == 127 }) != -1
}

func checkSourceWorkingFile(root string, file sourceFile) error {
	for directory := path.Dir(file.name); directory != "."; directory = path.Dir(directory) {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(directory)))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source tracked parent is missing or is not a real directory")
		}
	}
	name := filepath.Join(root, filepath.FromSlash(file.name))
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("source tracked file is missing: %s", file.name)
	}
	if file.mode == "120000" {
		target, err := os.Readlink(name)
		if err != nil || target != string(file.data) {
			return fmt.Errorf("source tracked symlink differs: %s", file.name)
		}
		return nil
	}
	if !info.Mode().IsRegular() || (info.Mode().Perm()&0100 != 0) != (file.mode == "100755") || info.Size() != int64(len(file.data)) {
		return fmt.Errorf("source tracked file mode/size differs: %s", file.name)
	}
	current, err := os.Open(name)
	if err != nil {
		return err
	}
	opened, err := current.Stat()
	if err != nil || !os.SameFile(info, opened) {
		current.Close()
		return errors.New("source tracked file was replaced while checking")
	}
	data, readErr := io.ReadAll(io.LimitReader(current, int64(len(file.data))+1))
	if err := errors.Join(readErr, current.Close()); err != nil {
		return err
	}
	if !bytes.Equal(data, file.data) {
		return fmt.Errorf("source tracked bytes differ: %s", file.name)
	}
	return nil
}
