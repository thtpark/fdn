package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func GitCommand(ctx context.Context, directory, gitDirectory string, args ...string) ([]byte, error) {
	binary, err := exec.LookPath("git")
	if err != nil {
		return nil, domain.Fail(domain.Prerequisite, "native Git is required for source operations")
	}
	output, diagnostic := process.NewBuffer(128<<10), process.NewBuffer(128<<10)
	environment := append(process.Environment(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file", "GIT_LFS_SKIP_SMUDGE=1")
	if gitDirectory != "" {
		environment = append(environment, "GIT_DIR="+gitDirectory)
	}
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.autocrlf=false"}, args...)
	if err := (process.Native{}).Run(ctx, process.Spec{Path: binary, Args: args, Dir: directory, Env: environment, Stdout: output, Stderr: diagnostic}); err != nil {
		return nil, errors.Join(domain.Fail(domain.Prerequisite, "native Git operation failed; private workspace/evidence were preserved"), err)
	}
	if _, err := diagnostic.Bytes(); err != nil {
		return nil, err
	}
	return output.Bytes()
}

func SourceWorkspace(ctx context.Context, client *api.Client, source domain.TaskSource, root, scope string) (string, error) {
	if !domain.ValidOperation(scope) {
		return "", domain.Fail(domain.Invalid, "source workspace requires a stable owned scope")
	}
	path := filepath.Join(root, "source-"+scope)
	marker := filepath.Join(path, ".git", "foundation", "source")
	if _, err := os.Lstat(path); err == nil {
		return retainedSourceWorkspace(ctx, source, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := secure.Dir(path); err != nil {
		return "", err
	}
	if _, err := GitCommand(ctx, path, "", "init", "--quiet"); err != nil {
		return "", err
	}
	if source.Commit != "" {
		repo, err := api.Call[domain.HostedRepository](ctx, client, "GET", api.Path("repositories", source.RepositoryID), nil)
		if err != nil {
			return "", err
		}
		if len(repo.Refs) == 0 {
			return "", domain.Fail(domain.Prerequisite, "source repository has no retained commit")
		}
		selected := repo.Refs[0]
		for _, ref := range repo.Refs {
			if ref.Commit == source.Commit {
				selected = ref
				break
			}
		}
		bundlePath := filepath.Join(path, ".git", "foundation-bundle")
		file, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, downloadErr := client.DownloadArtifact(ctx, selected.ArtifactID, file, false)
		if err := errors.Join(downloadErr, file.Sync(), file.Close()); err != nil {
			return "", err
		}
		if _, err := GitCommand(ctx, path, "", "fetch", "--quiet", "--no-tags", "--", bundlePath, source.Commit); err != nil {
			return "", err
		}
		if _, err := GitCommand(ctx, path, "", "checkout", "--quiet", "--detach", source.Commit); err != nil {
			return "", err
		}
		if err := os.Remove(bundlePath); err != nil {
			return "", err
		}
	}
	if source.Ref != "" {
		branch := strings.TrimPrefix(source.Ref, "refs/heads/")
		if _, err := GitCommand(ctx, path, "", "checkout", "--quiet", "-b", branch); err != nil {
			return "", err
		}
	}
	if err := secure.Create(marker, []byte(source.RepositoryID+"\n"+source.Commit+"\n"+source.Ref+"\n")); err != nil {
		return "", err
	}
	if err := sourceGitBinding(path, true); err != nil {
		return "", err
	}
	return path, nil
}

func sourceGitBinding(path string, create bool) error {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return domain.Fail(domain.Conflict, "source Git directory identity is unavailable")
	}
	expected := fmt.Sprintf("%d:%d\n", stat.Dev, stat.Ino)
	marker := filepath.Join(path, ".git", "foundation", "git-directory")
	if create {
		return secure.Create(marker, []byte(expected))
	}
	data, err := secure.Read(marker, 128)
	if err != nil {
		return err
	}
	if string(data) != expected {
		return domain.Fail(domain.Conflict, "source Git directory was replaced; retained evidence requires inspection")
	}
	return nil
}

func retainedSourceWorkspace(ctx context.Context, source domain.TaskSource, path string) (string, error) {
	for _, directory := range []string{path, filepath.Join(path, ".git")} {
		info, err := os.Lstat(directory)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", domain.Fail(domain.Conflict, "source workspace requires an owned real Git directory; retained files were preserved")
		}
		if err := secure.CheckOwner(info); err != nil {
			return "", err
		}
	}
	private := filepath.Join(path, ".git", "foundation")
	if info, err := os.Lstat(private); err == nil {
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return "", domain.Fail(domain.Conflict, "source ownership namespace was replaced or is not private")
		}
		if err := secure.CheckOwner(info); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	marker := filepath.Join(private, "source")
	legacy := filepath.Join(path, ".foundation-source")
	data, markerErr := secure.Read(marker, 4096)
	old, legacyErr := secure.Read(legacy, 4096)
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return "", markerErr
	}
	if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return "", legacyErr
	}
	if (markerErr == nil) == (legacyErr == nil) {
		return "", domain.Fail(domain.Conflict, "source ownership is missing or conflicting; preserved evidence requires inspection")
	}
	if markerErr == nil {
		if err := sourceGitBinding(path, false); err != nil {
			return "", err
		}
	}
	if legacyErr == nil {
		data = old
	}
	if string(data) != source.RepositoryID+"\n"+source.Commit+"\n"+source.Ref+"\n" {
		return "", domain.Fail(domain.Conflict, "source workspace is bound to different retained inputs; it was not replaced")
	}
	gitDirectory := filepath.Join(path, ".git")
	top, err := GitCommand(ctx, path, gitDirectory, "rev-parse", "--show-toplevel")
	physical, physicalErr := filepath.EvalSymlinks(path)
	if err != nil || physicalErr != nil || strings.TrimSpace(string(top)) != physical {
		return "", domain.Fail(domain.Conflict, "source checkout is incomplete or its Git binding changed")
	}
	if source.Commit != "" {
		if _, err := GitCommand(ctx, path, gitDirectory, "merge-base", "--is-ancestor", source.Commit, "HEAD"); err != nil {
			return "", domain.Fail(domain.Conflict, "source checkout no longer contains its bound commit")
		}
	}
	if source.Ref != "" {
		ref, err := GitCommand(ctx, path, gitDirectory, "symbolic-ref", "HEAD")
		if err != nil || strings.TrimSpace(string(ref)) != source.Ref {
			return "", domain.Fail(domain.Conflict, "source checkout no longer has its bound work ref")
		}
	} else if source.Commit != "" {
		head, err := GitCommand(ctx, path, gitDirectory, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(head)) != source.Commit {
			return "", domain.Fail(domain.Conflict, "source checkout differs from its bound commit")
		}
	}
	if legacyErr == nil {
		if _, err := os.Lstat(filepath.Join(path, ".foundation-bundle")); !errors.Is(err, os.ErrNotExist) {
			return "", domain.Fail(domain.Conflict, "legacy source preparation is incomplete; retained bundle was preserved")
		}
		binding := filepath.Join(private, "git-directory")
		if _, err := os.Lstat(binding); errors.Is(err, os.ErrNotExist) {
			if err := sourceGitBinding(path, true); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		} else if err := sourceGitBinding(path, false); err != nil {
			return "", err
		}
		if err := secure.Create(filepath.Join(private, "legacy-source"), old); err != nil {
			return "", err
		}
		if err := secure.Create(marker, old); err != nil {
			return "", err
		}
		if err := os.Remove(legacy); err != nil {
			return "", err
		}
		if err := secure.SyncDir(path); err != nil {
			return "", err
		}
	}
	return path, nil
}
