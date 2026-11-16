package distribution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// CreateSourceAssets creates a new, exclusive output directory. All archive
// bytes come from checked Git blobs; the bundle retains real release ancestry.
// This helper intentionally has no release-event bypass: only the guarded
// package command may use it for a real release, while tests use isolated Git.
func CreateSourceAssets(ctx context.Context, root, output, version, commit string) (SourceRelease, error) {
	snapshot, err := readSourceSnapshot(ctx, root, version, commit)
	if err != nil {
		return SourceRelease{}, err
	}
	if err := os.Mkdir(output, 0755); err != nil {
		return SourceRelease{}, err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(output)
		}
	}()
	names := SourceAssetNames(version)
	if err := sourceWrite(filepath.Join(output, names[0]), func(w io.Writer) error { return writeSourceArchive(w, snapshot) }); err != nil {
		return SourceRelease{}, err
	}
	bundle, err := filepath.Abs(filepath.Join(output, names[1]))
	if err != nil {
		return SourceRelease{}, err
	}
	if _, err := sourceGit(ctx, snapshot.root, "-c", "pack.threads=1", "-c", "pack.window=0", "-c", "pack.depth=0",
		"-c", "core.compression=9", "bundle", "create", "--version=2", bundle, "HEAD"); err != nil {
		return SourceRelease{}, err
	}
	if err := verifySourceBundle(ctx, bundle, snapshot, output); err != nil {
		return SourceRelease{}, err
	}
	if err := sourceSyncFile(bundle); err != nil {
		return SourceRelease{}, err
	}
	gitVersion, err := sourceGit(ctx, snapshot.root, "version")
	if err != nil {
		return SourceRelease{}, err
	}
	release := SourceRelease{FormatVersion: SourceFormatVersion, Version: version, Commit: commit, Tree: snapshot.Tree,
		SourceDateEpoch: snapshot.SourceDateEpoch, ArchivePrefix: "fdn-" + version + "-source/",
		ArchiveRules: sourceArchiveRules, ArchiveGoVersion: runtime.Version(), BundleRules: sourceBundleRules,
		GitVersion: strings.TrimSpace(string(gitVersion)), OfflineDependencyLimitation: sourceOfflineLimitation}
	for _, name := range names[:2] {
		artifact, err := sourceArtifact(output, name)
		if err != nil {
			return SourceRelease{}, err
		}
		release.Artifacts = append(release.Artifacts, artifact)
	}
	if err := sourceWrite(filepath.Join(output, names[2]), func(w io.Writer) error { return json.NewEncoder(w).Encode(release) }); err != nil {
		return SourceRelease{}, err
	}
	checksums, err := sourceChecksums(output, names[:3])
	if err != nil {
		return SourceRelease{}, err
	}
	if err := sourceWrite(filepath.Join(output, names[3]), func(w io.Writer) error { _, err := w.Write(checksums); return err }); err != nil {
		return SourceRelease{}, err
	}
	if err := sourceSyncFile(output); err != nil {
		return SourceRelease{}, err
	}
	complete = true
	return release, nil
}

// VerifySourceAssets checks the metadata/checksums, exact archive tree and bytes,
// and a self-contained bundle in a fresh local object store beneath input.
// It requires a complete checked-out release and a writable input directory,
// but never fetches dependencies or changes tracked source/existing Git objects.
func VerifySourceAssets(ctx context.Context, root, input, version, commit string) (SourceRelease, error) {
	snapshot, err := readSourceSnapshot(ctx, root, version, commit)
	if err != nil {
		return SourceRelease{}, err
	}
	names := SourceAssetNames(version)
	info, err := os.Lstat(input)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return SourceRelease{}, errors.New("source assets must be in a real directory")
	}
	entries, err := os.ReadDir(input)
	if err != nil {
		return SourceRelease{}, err
	}
	if len(entries) != len(names) {
		return SourceRelease{}, errors.New("source companion asset set is incomplete or contains extras")
	}
	for _, entry := range entries {
		if !slices.Contains(names, entry.Name()) || !entry.Type().IsRegular() {
			return SourceRelease{}, errors.New("source companion assets must be named regular files")
		}
	}
	metadata, err := sourceReadLimited(filepath.Join(input, names[2]), 64<<10)
	if err != nil {
		return SourceRelease{}, err
	}
	var release SourceRelease
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil {
		return SourceRelease{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return SourceRelease{}, errors.New("source metadata must contain exactly one JSON object")
	}
	if release.FormatVersion != SourceFormatVersion || release.Version != version || release.Commit != commit ||
		release.Tree != snapshot.Tree || release.SourceDateEpoch != snapshot.SourceDateEpoch ||
		release.ArchivePrefix != "fdn-"+version+"-source/" || release.ArchiveRules != sourceArchiveRules ||
		release.BundleRules != sourceBundleRules || release.OfflineDependencyLimitation != sourceOfflineLimitation ||
		!strings.HasPrefix(release.ArchiveGoVersion, "go1.") || !strings.HasPrefix(release.GitVersion, "git version ") ||
		len(release.ArchiveGoVersion) > 128 || len(release.GitVersion) > 128 || len(release.Artifacts) != 2 {
		return SourceRelease{}, errors.New("source metadata differs from the release identity or companion contract")
	}
	for i, artifact := range release.Artifacts {
		if artifact.Name != names[i] || artifact.Size <= 0 || !sourceDigestPattern.MatchString(artifact.SHA256) {
			return SourceRelease{}, errors.New("source payload metadata is incomplete or invalid")
		}
		actual, err := sourceArtifact(input, artifact.Name)
		if err != nil {
			return SourceRelease{}, err
		}
		if actual != artifact {
			return SourceRelease{}, errors.New("source payload size or SHA-256 differs from metadata")
		}
	}
	expected, err := sourceChecksums(input, names[:3])
	if err != nil {
		return SourceRelease{}, err
	}
	manifest, err := sourceReadLimited(filepath.Join(input, names[3]), int64(len(expected)))
	if err != nil {
		return SourceRelease{}, err
	}
	if !bytes.Equal(manifest, expected) {
		return SourceRelease{}, errors.New("source checksums differ from the exact companion assets")
	}
	if err := verifySourceArchive(filepath.Join(input, names[0]), snapshot); err != nil {
		return SourceRelease{}, err
	}
	bundle, err := filepath.Abs(filepath.Join(input, names[1]))
	if err != nil {
		return SourceRelease{}, err
	}
	if err := verifySourceBundle(ctx, bundle, snapshot, input); err != nil {
		return SourceRelease{}, err
	}
	return release, nil
}

func verifySourceBundle(ctx context.Context, name string, snapshot sourceSnapshot, workspace string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(io.LimitReader(file, 1024))
	signature, err := reader.ReadString('\n')
	if err != nil || signature != "# v2 git bundle\n" {
		file.Close()
		return errors.New("source must contain a Git bundle v2")
	}
	ref, refErr := reader.ReadString('\n')
	end, endErr := reader.ReadString('\n')
	if err := errors.Join(refErr, endErr, file.Close()); err != nil {
		return err
	}
	if ref != snapshot.Commit+" HEAD\n" || end != "\n" {
		return errors.New("source bundle must advertise only the release HEAD, with no prerequisites")
	}
	store, err := os.MkdirTemp(workspace, ".source-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(store)
	if _, err := sourceGit(ctx, store, "init", "--bare", "--quiet"); err != nil {
		return err
	}
	refs, err := sourceGit(ctx, store, "bundle", "unbundle", name)
	if err != nil {
		return err
	}
	if string(refs) != snapshot.Commit+" HEAD\n" {
		return errors.New("source bundle imported a different release ref")
	}
	if _, err := sourceGit(ctx, store, "update-ref", "refs/heads/release-source", snapshot.Commit); err != nil {
		return err
	}
	if _, err := sourceGit(ctx, store, "fsck", "--full", "--strict", "--no-reflogs"); err != nil {
		return err
	}
	tree, err := sourceGit(ctx, store, "rev-parse", snapshot.Commit+"^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != snapshot.Tree {
		return errors.New("source bundle release tree differs")
	}
	expected, err := sourceGit(ctx, snapshot.root, "rev-list", "--objects", "--no-object-names", "--missing=error", snapshot.Commit)
	if err != nil {
		return err
	}
	actual, err := sourceGit(ctx, store, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return err
	}
	want, have := strings.Fields(string(expected)), strings.Fields(string(actual))
	slices.Sort(want)
	slices.Sort(have)
	if !slices.Equal(want, have) {
		return errors.New("source bundle does not contain exactly the complete release ancestry")
	}
	return nil
}

func sourceArtifact(directory, name string) (SourceArtifact, error) {
	file := filepath.Join(directory, name)
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return SourceArtifact{}, errors.New("source artifacts must be regular files")
	}
	digest, err := SHA256(file)
	if err != nil {
		return SourceArtifact{}, err
	}
	return SourceArtifact{Name: name, Size: info.Size(), SHA256: digest}, nil
}

func sourceChecksums(directory string, names []string) ([]byte, error) {
	names = slices.Clone(names)
	slices.Sort(names)
	var manifest bytes.Buffer
	for _, name := range names {
		artifact, err := sourceArtifact(directory, name)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&manifest, "%s  %s\n", artifact.SHA256, name)
	}
	return manifest.Bytes(), nil
}

func sourceWrite(name string, write func(io.Writer) error) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	return errors.Join(write(file), file.Sync(), file.Close())
}

func sourceSyncFile(name string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func sourceReadLimited(name string, limit int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("source metadata/checksums must be bounded regular files")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, errors.New("source metadata/checksums were replaced while checking")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("source metadata/checksums exceed their size limit")
	}
	return data, nil
}
