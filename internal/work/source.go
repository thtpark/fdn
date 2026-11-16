package work

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	marker := filepath.Join(path, ".foundation-source")
	if data, err := secure.Read(marker, 4096); err == nil {
		if string(data) != source.RepositoryID+"\n"+source.Commit+"\n"+source.Ref+"\n" {
			return "", domain.Fail(domain.Conflict, "source workspace is bound to different retained inputs; it was not replaced")
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return "", domain.Fail(domain.Conflict, "source preparation is incomplete or collides with existing files; inspect preserved local evidence")
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
		bundlePath := filepath.Join(path, ".foundation-bundle")
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
	ignore := filepath.Join(path, ".git", "info", "exclude")
	file, err := os.OpenFile(ignore, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString("\n.foundation-source\n.foundation-bundle\n"); err != nil {
		return "", errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}
