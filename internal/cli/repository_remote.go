package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/gitrepo"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

const gitRemoteMarker = "# Foundation managed Git remote helper v1\n"
const workTaskEnvironment = "FDN_WORK_TASK"
const workRunEnvironment = "FDN_WORK_RUN"

func (a *app) repositoryLocal(ctx context.Context, r *resources, leaf string, args []string) (any, error) {
	switch leaf {
	case "link":
		if len(args) != 0 {
			return nil, domain.Fail(domain.Invalid, "repository link takes no caller executable or path")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		directory := filepath.Join(home, config.UserDirectory, "bin")
		if err := secure.Dir(directory); err != nil {
			return nil, err
		}
		path := filepath.Join(directory, "git-remote-fdn")
		content := "#!/bin/sh\n" + gitRemoteMarker + "exec " + quoteNativeShell(binary) +
			" --config " + quoteNativeShell(a.configFile) + " --non-interactive --timeout 300 repository remote \"$@\"\n"
		if existing, err := secure.Read(path, 16<<10); err == nil {
			if !strings.Contains(string(existing), gitRemoteMarker) {
				return nil, domain.Fail(domain.Conflict, "unmanaged Git helper collision; existing executable was preserved")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := secure.Write(path, []byte(content)); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0700); err != nil {
			return nil, err
		}
		return map[string]bool{"linked": true}, nil
	case "bundle":
		fs := flag.NewFlagSet("repository bundle", flag.ContinueOnError)
		workspace := fs.String("workspace", "", "owned workspace reference")
		ref := fs.String("ref", "", "exact source ref; no implicit private --all")
		output := fs.String("output", "", "new bundle destination")
		if err := parse(fs, args); err != nil {
			return nil, err
		}
		if *workspace == "" || *output == "" || gitrepo.ValidateRef(*ref) != nil {
			return nil, domain.Fail(domain.Invalid, "repository bundle requires --workspace REF --ref refs/... --output NEW_FILE")
		}
		binding, err := r.state.Workspace(ctx, *workspace)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(*output); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return nil, err
			}
			return nil, domain.Fail(domain.Conflict, "bundle output already exists; no files were overwritten")
		}
		if _, err := work.GitCommand(ctx, binding.Path, "", "bundle", "create", "--version=2", *output, *ref); err != nil {
			return nil, err
		}
		file, err := os.Open(*output)
		if err != nil {
			return nil, err
		}
		size, digest, hashErr := transferDigest(file)
		closeErr := file.Close()
		if err := errors.Join(hashErr, closeErr); err != nil {
			return nil, err
		}
		if size > domain.MaxGitBundleBytes {
			return nil, domain.Fail(domain.Prerequisite, "source bundle exceeds the 64 MiB bound; preserve it and reduce explicitly")
		}
		return map[string]any{"size": size, "sha256": digest, "ref": *ref}, nil
	case "remote":
		if len(args) != 2 {
			return nil, domain.Fail(domain.Invalid, "Git remote helper requires Git's exact remote name and fdn::repository_ID")
		}
		id := strings.TrimPrefix(args[1], "fdn::")
		if !strings.HasPrefix(args[1], "fdn::") || !strings.HasPrefix(id, "repository_") || !domain.ValidOperation(id) {
			return nil, domain.Fail(domain.Invalid, "Git remote must be fdn::repository_ID on this enrolled Foundation connection")
		}
		return nil, a.gitRemoteProtocol(ctx, r, id)
	default:
		return nil, domain.Fail(domain.Invalid, "unknown local repository operation")
	}
}

func quoteNativeShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (a *app) gitRemoteProtocol(ctx context.Context, r *resources, id string) error {
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	gitDirectory := os.Getenv("GIT_DIR")
	if gitDirectory != "" {
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(directory, gitDirectory)
		}
		absolute, err := filepath.EvalSymlinks(gitDirectory)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return domain.Fail(domain.Denied, "Git destination must be an owned directory without foreign write access")
		}
		if err := secure.CheckOwner(info); err != nil {
			return err
		}
		if pathContains(r.config.StateDir, absolute) || pathContains(absolute, r.config.StateDir) {
			return domain.Fail(domain.Denied, "source transport cannot use private Foundation state as a Git destination")
		}
		gitDirectory = absolute
	}
	reader := bufio.NewScanner(a.in)
	reader.Buffer(make([]byte, 4096), 8192)
	var pending []string
	for reader.Scan() {
		line := reader.Text()
		if line == "" {
			if len(pending) == 0 {
				continue
			}
			if err := a.remoteBatch(ctx, r, id, directory, gitDirectory, pending); err != nil {
				return err
			}
			pending = nil
			continue
		}
		switch {
		case line == "capabilities":
			if _, err := io.WriteString(a.out, "fetch\npush\noption\n\n"); err != nil {
				return err
			}
		case line == "list" || line == "list for-push":
			repo, err := api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", id), nil)
			if err != nil {
				return err
			}
			for _, ref := range repo.Refs {
				if _, err := fmt.Fprintf(a.out, "%s %s\n", ref.Commit, ref.Name); err != nil {
					return err
				}
			}
			for _, ref := range repo.Refs {
				if ref.Name == "refs/heads/"+repo.DefaultBranch {
					if _, err := fmt.Fprintf(a.out, "@%s HEAD\n", ref.Name); err != nil {
						return err
					}
				}
			}
			if _, err := io.WriteString(a.out, "\n"); err != nil {
				return err
			}
		case strings.HasPrefix(line, "option "):
			reply := "unsupported\n"
			if strings.HasPrefix(line, "option verbosity ") || strings.HasPrefix(line, "option progress ") {
				reply = "ok\n"
			}
			if _, err := io.WriteString(a.out, reply); err != nil {
				return err
			}
		case strings.HasPrefix(line, "fetch ") || strings.HasPrefix(line, "push "):
			if len(pending) >= domain.MaxRepositoryRefs {
				return domain.Fail(domain.Invalid, "Git batch exceeds its ref bound")
			}
			pending = append(pending, line)
		default:
			return domain.Fail(domain.Invalid, "unsupported bounded Git remote-helper command")
		}
	}
	return reader.Err()
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (a *app) remoteBatch(ctx context.Context, r *resources, id, directory, gitDirectory string, lines []string) error {
	repo, err := api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", id), nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(lines[0], "push ") {
		if len(lines) != 1 {
			return domain.Fail(domain.Invalid, "a task push admits exactly one work ref")
		}
		spec := strings.TrimPrefix(lines[0], "push ")
		source, destination, ok := strings.Cut(spec, ":")
		if !ok || strings.HasPrefix(source, "+") || source == "" || gitrepo.ValidateRef(destination) != nil {
			return domain.Fail(domain.Denied, "force/deletion or invalid Git pushes are not supported")
		}
		if err := a.remotePush(ctx, r, repo, directory, gitDirectory, source, destination); err != nil {
			_, writeErr := fmt.Fprintf(a.out, "error %s Foundation rejected or could not confirm the exact push; reconcile retained operation\n\n", destination)
			return errors.Join(err, writeErr)
		}
		_, err := fmt.Fprintf(a.out, "ok %s\n\n", destination)
		return err
	}
	artifacts := make(map[string]bool)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "fetch" || !gitrepo.Commit(fields[1]) {
			return domain.Fail(domain.Invalid, "fetch requires an exact listed commit/ref")
		}
		found := false
		for _, ref := range repo.Refs {
			if ref.Commit != fields[1] || (ref.Name != fields[2] && fields[2] != "HEAD") {
				continue
			}
			found = true
			if artifacts[ref.ArtifactID] {
				break
			}
			staging, err := os.CreateTemp(r.config.StateDir, "source-fetch-*.bundle")
			if err != nil {
				return err
			}
			_, downloadErr := r.client.DownloadArtifact(ctx, ref.ArtifactID, staging, false)
			closeErr := staging.Close()
			if err := errors.Join(downloadErr, closeErr); err != nil {
				return err
			}
			_, fetchErr := work.GitCommand(ctx, directory, gitDirectory, "fetch", "--no-tags", "--no-write-fetch-head", "--", staging.Name(), fields[1])
			removeErr := os.Remove(staging.Name())
			if err := errors.Join(fetchErr, removeErr); err != nil {
				return err
			}
			artifacts[ref.ArtifactID] = true
			break
		}
		if !found {
			return domain.Fail(domain.Conflict, "listed fetch ref changed; no unrelated source was adopted")
		}
	}
	_, err = io.WriteString(a.out, "\n")
	return err
}

type repositoryPushJournal struct {
	Request domain.RepositoryPush    `json:"request"`
	Upload  domain.FileUploadRequest `json:"upload"`
	Path    string                   `json:"path"`
	State   string                   `json:"state"`
}

func (a *app) remotePush(ctx context.Context, r *resources, repo domain.HostedRepository, directory, gitDirectory, source, destination string) error {
	taskID, runID := os.Getenv(workTaskEnvironment), os.Getenv(workRunEnvironment)
	if !domain.ValidOperation(taskID) || !domain.ValidOperation(runID) {
		return domain.Fail(domain.Prerequisite, "Git push must run inside its admitted source task; FDN_WORK_TASK and FDN_WORK_RUN are missing")
	}
	head, err := work.GitCommand(ctx, directory, gitDirectory, "rev-parse", "--verify", source+"^{commit}")
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(string(head))
	if !gitrepo.Commit(commit) {
		return domain.Fail(domain.Invalid, "local push source is not an exact commit")
	}
	operation := "op_" + domain.Hash(repo.ID+":"+taskID+":"+runID+":"+destination+":"+commit)
	key := "repository-push:" + operation
	var journal repositoryPushJournal
	found, err := r.state.Get(ctx, key, &journal)
	if err != nil {
		return err
	}
	if !found {
		old := ""
		for _, ref := range repo.Refs {
			if ref.Name == destination {
				old = ref.Commit
			}
		}
		staging := filepath.Join(r.config.StateDir, "source-pushes")
		if err := secure.Dir(staging); err != nil {
			return err
		}
		path := filepath.Join(staging, operation+".bundle")
		temporary := path + ".preparing"
		if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
			return domain.Fail(domain.Conflict, "an interrupted source-bundle preparation is retained; inspect it before another push")
		}
		if _, err := work.GitCommand(ctx, directory, gitDirectory, "bundle", "create", "--version=2", temporary, source); err != nil {
			return err
		}
		file, err := os.Open(temporary)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, domain.MaxGitBundleBytes+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if int64(len(data)) > domain.MaxGitBundleBytes {
			return domain.Fail(domain.Prerequisite, "source bundle exceeds its 64 MiB bound; retained preparation was not pushed")
		}
		headerEnd := bytes.Index(data, []byte("\n\n"))
		if headerEnd < 0 {
			return domain.Fail(domain.Invalid, "local Git returned an invalid source bundle")
		}
		header := "# v2 git bundle\n" + commit + " " + destination + "\n\n"
		data = append([]byte(header), data[headerEnd+2:]...)
		if _, err := gitrepo.Open(data); err != nil {
			return err
		}
		if err := secure.Create(path, data); err != nil {
			return err
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
		journal = repositoryPushJournal{Path: path, State: "prepared",
			Request: domain.RepositoryPush{Command: domain.Command{OperationID: operation}, TaskID: taskID, RunID: runID,
				Updates: []domain.GitRefUpdate{{Name: destination, Old: old, New: commit}}},
			Upload: domain.FileUploadRequest{Command: domain.Command{OperationID: operation + ".upload"}, Name: operation + ".bundle",
				MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: domain.Hash(string(data))}}
		if err := r.state.Put(ctx, key, journal); err != nil {
			return err
		}
	}
	if journal.State == "confirmed" {
		return nil
	}
	file, err := os.Open(journal.Path)
	if err != nil {
		return err
	}
	uploaded, uploadErr := r.client.UploadFile(ctx, journal.Upload, file)
	closeErr := file.Close()
	if err := errors.Join(uploadErr, closeErr); err != nil {
		return err
	}
	journal.Request.BundleFileID = uploaded.ID
	if err := r.state.Put(ctx, key, journal); err != nil {
		return err
	}
	if _, err := api.Call[domain.HostedRepository](ctx, r.client, "POST", api.Path("repositories", repo.ID, "push"), journal.Request); err != nil {
		return err
	}
	journal.State = "confirmed"
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.state.Put(save, key, journal); err != nil {
		return err
	}
	return os.Remove(journal.Path)
}
