package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/gitrepo"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/work"
)

const upstreamTrackingPrefix = "refs/heads/upstream/"
const maxReportedConflicts = 200

// repositoryUpstream is this computer's private upstream selection for one hosted
// repository. It is not sent to Foundation: fetching uses this account's own Git
// credentials, and Foundation only receives verified bundles.
type repositoryUpstream struct {
	RepositoryID string `json:"repository_id"`
	URL          string `json:"url"`
	Branch       string `json:"branch"`
	TrackingRef  string `json:"tracking_ref"`
	UpdatedAt    int64  `json:"updated_at"`
}

type repositoryUpstreamInput struct {
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

type repositorySyncResolution struct {
	Workspace string `json:"workspace"`
	Ref       string `json:"ref"`
}

type repositorySyncInput struct {
	Reason     string                    `json:"reason"`
	AssignTo   string                    `json:"assign_to,omitempty"`
	DryRun     bool                      `json:"dry_run,omitempty"`
	Resolution *repositorySyncResolution `json:"resolution,omitempty"`
}

type repositorySyncJournal struct {
	RepositoryID string                   `json:"repository_id"`
	DefaultRef   string                   `json:"default_ref"`
	InputSHA256  string                   `json:"input_sha256"`
	Outcome      string                   `json:"outcome"`
	State        string                   `json:"state"`
	Request      domain.RepositoryMerge   `json:"request"`
	Upload       domain.FileUploadRequest `json:"upload"`
	Path         string                   `json:"path,omitempty"`
	Conflicts    []string                 `json:"conflicts"`
	TaskID       string                   `json:"task_id,omitempty"`
	Revision     int64                    `json:"revision,omitempty"`
}

type repositorySyncResult struct {
	RepositoryID   string   `json:"repository_id"`
	Outcome        string   `json:"outcome"`
	Applied        bool     `json:"applied"`
	DryRun         bool     `json:"dry_run,omitempty"`
	DefaultRef     string   `json:"default_ref"`
	TrackingRef    string   `json:"tracking_ref"`
	BaseCommit     string   `json:"base_commit"`
	UpstreamCommit string   `json:"upstream_commit"`
	Commit         string   `json:"commit,omitempty"`
	Revision       int64    `json:"revision,omitempty"`
	Conflicts      []string `json:"conflicts"`
	TaskID         string   `json:"task_id,omitempty"`
	Next           string   `json:"next,omitempty"`
}

func (a *app) repositoryUpstreamCommand(ctx context.Context, r *resources, leaf string, args []string) (any, error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "repository_") || !domain.ValidOperation(args[0]) {
		return nil, domain.Fail(domain.Invalid, "repository "+leaf+" requires an exact hosted repository ID")
	}
	id, tail := args[0], args[1:]
	if leaf == "upstream" {
		return a.configureRepositoryUpstream(ctx, r, id, tail)
	}
	var input repositorySyncInput
	if err := a.input(tail, &input); err != nil {
		return nil, err
	}
	return a.syncRepositoryUpstream(ctx, r, id, input)
}

func (a *app) configureRepositoryUpstream(ctx context.Context, r *resources, id string, args []string) (any, error) {
	key := "repository-upstream:" + id
	if len(args) == 0 {
		var current repositoryUpstream
		found, err := r.state.Get(ctx, key, &current)
		if err != nil {
			return nil, err
		}
		if !found || current.URL == "" {
			return map[string]any{"repository_id": id, "configured": false}, nil
		}
		return current, nil
	}
	fs := flag.NewFlagSet("repository upstream", flag.ContinueOnError)
	path := fs.String("input", "", "JSON file or -")
	if err := parse(fs, args); err != nil || *path == "" {
		return nil, domain.Fail(domain.Invalid, "repository upstream takes REPOSITORY_ID [--input FILE]")
	}
	var input repositoryUpstreamInput
	if err := inputFile(*path, a.in, &input); err != nil {
		return nil, err
	}
	if input.URL == "" {
		if input.Branch != "" {
			return nil, domain.Fail(domain.Invalid, "clearing an upstream takes no branch")
		}
		if err := r.state.Put(ctx, key, repositoryUpstream{RepositoryID: id, UpdatedAt: time.Now().UTC().Unix()}); err != nil {
			return nil, err
		}
		return map[string]any{"repository_id": id, "configured": false}, nil
	}
	if input.Branch == "" {
		input.Branch = "main"
	}
	if err := validateUpstreamURL(input.URL); err != nil {
		return nil, err
	}
	tracking, err := upstreamTrackingRef(input.Branch)
	if err != nil {
		return nil, err
	}
	repo, err := api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", id), nil)
	if err != nil {
		return nil, err
	}
	if err := upstreamNamespaceFree(repo); err != nil {
		return nil, err
	}
	value := repositoryUpstream{RepositoryID: repo.ID, URL: input.URL, Branch: input.Branch, TrackingRef: tracking, UpdatedAt: time.Now().UTC().Unix()}
	return value, r.state.Put(ctx, key, value)
}

var scpUpstream = regexp.MustCompile(`^([A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9.-]*:[A-Za-z0-9._~/+-]+$`)

// validateUpstreamURL admits HTTPS, SSH and local file/path remotes without
// embedded credentials, options, transport helpers or query/fragment data.
func validateUpstreamURL(value string) error {
	invalid := domain.Fail(domain.Invalid, "upstream must be an https://, ssh://, scp-style SSH or absolute local Git URL without credentials")
	if len(value) > 2048 || value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "::") {
		return invalid
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return invalid
		}
	}
	if filepath.IsAbs(value) {
		return nil
	}
	if !strings.Contains(value, "://") {
		if !scpUpstream.MatchString(value) {
			return invalid
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return invalid
	}
	switch parsed.Scheme {
	case "https":
		if parsed.Host == "" || parsed.User != nil {
			return invalid
		}
	case "ssh":
		if parsed.Host == "" || strings.HasPrefix(parsed.Host, "-") ||
			parsed.User != nil && (strings.HasPrefix(parsed.User.Username(), "-") || func() bool { _, set := parsed.User.Password(); return set }()) {
			return invalid
		}
	case "file":
		if parsed.Host != "" || !filepath.IsAbs(parsed.Path) {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func upstreamTrackingRef(branch string) (string, error) {
	tracking := upstreamTrackingPrefix + branch
	if gitrepo.ValidateRef("refs/heads/"+branch) != nil || gitrepo.ValidateRef(tracking) != nil {
		return "", domain.Fail(domain.Invalid, "upstream branch must be a valid Git branch name")
	}
	return tracking, nil
}

func upstreamNamespaceFree(repo domain.HostedRepository) error {
	if repo.DefaultBranch == "upstream" || strings.HasPrefix(repo.DefaultBranch, "upstream/") {
		return domain.Fail(domain.Invalid, "default branch collides with the protected upstream tracking namespace")
	}
	return nil
}

func (a *app) syncRepositoryUpstream(ctx context.Context, r *resources, id string, input repositorySyncInput) (any, error) {
	if err := domain.CheckText(input.Reason, "upstream sync reason", 2048, true); err != nil {
		return nil, err
	}
	if input.Resolution != nil && (input.AssignTo != "" || input.DryRun) {
		return nil, domain.Fail(domain.Invalid, "a resolution submission accepts no assignment or dry run")
	}
	if input.AssignTo != "" && (!strings.HasPrefix(input.AssignTo, "agent_") || !domain.ValidOperation(input.AssignTo)) {
		return nil, domain.Fail(domain.Invalid, "assign_to must be an exact agent ID")
	}
	if !domain.ValidOperation(a.operation) {
		return nil, domain.Fail(domain.Invalid, "invalid operation ID")
	}
	var upstream repositoryUpstream
	found, err := r.state.Get(ctx, "repository-upstream:"+id, &upstream)
	if err != nil {
		return nil, err
	}
	if !found || upstream.URL == "" {
		return nil, domain.Fail(domain.Prerequisite, "configure this repository's upstream with repository upstream REPOSITORY_ID --input FILE first")
	}
	raw, err := json.Marshal(struct {
		Input    repositorySyncInput `json:"input"`
		Upstream repositoryUpstream  `json:"upstream"`
	}{input, repositoryUpstream{URL: upstream.URL, Branch: upstream.Branch}})
	if err != nil {
		return nil, err
	}
	fingerprint := domain.Hash(string(raw))
	key := "repository-sync:" + a.operation
	var journal repositorySyncJournal
	found, err = r.state.Get(ctx, key, &journal)
	if err != nil {
		return nil, err
	}
	if found && (journal.RepositoryID != id || journal.InputSHA256 != fingerprint) {
		return nil, domain.Fail(domain.Conflict, "operation ID belongs to a different upstream sync; it was not replaced")
	}
	if !found {
		repo, err := api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", id), nil)
		if err != nil {
			return nil, err
		}
		if err := upstreamNamespaceFree(repo); err != nil {
			return nil, err
		}
		if input.Resolution != nil {
			journal, err = a.prepareUpstreamResolution(ctx, r, repo, upstream, input)
		} else {
			journal, err = a.prepareUpstreamSync(ctx, r, repo, upstream, input)
		}
		if err != nil {
			return nil, err
		}
		journal.RepositoryID, journal.InputSHA256, journal.DefaultRef = id, fingerprint, "refs/heads/"+repo.DefaultBranch
		if input.DryRun || journal.State == "complete" {
			return syncResult(journal, upstream, input), nil
		}
		if err := r.state.Put(ctx, key, journal); err != nil {
			return nil, err
		}
	}
	if journal.State == "prepared" {
		file, err := os.Open(journal.Path)
		if err != nil {
			return nil, err
		}
		uploaded, uploadErr := r.client.UploadFile(ctx, journal.Upload, file)
		closeErr := file.Close()
		if err := errors.Join(uploadErr, closeErr); err != nil {
			return nil, err
		}
		journal.Request.BundleFileID = uploaded.ID
		if err := r.state.Put(ctx, key, journal); err != nil {
			return nil, err
		}
		repo, err := api.Call[domain.HostedRepository](ctx, r.client, "POST", api.Path("repositories", id, "upstream"), journal.Request)
		if err != nil {
			return nil, err
		}
		journal.State, journal.Revision = "imported", repo.Revision
		if err := putDetached(ctx, r, key, journal); err != nil {
			return nil, err
		}
		if err := os.Remove(journal.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if journal.State == "imported" && journal.Outcome == "conflict" && input.AssignTo != "" {
		taskID, err := a.assignUpstreamConflict(ctx, r, journal, upstream, input)
		if err != nil {
			return nil, err
		}
		journal.TaskID = taskID
	}
	if journal.State == "imported" {
		journal.State = "complete"
		if err := putDetached(ctx, r, key, journal); err != nil {
			return nil, err
		}
	}
	return syncResult(journal, upstream, input), nil
}

func putDetached(ctx context.Context, r *resources, key string, value any) error {
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return r.state.Put(save, key, value)
}

func syncResult(journal repositorySyncJournal, upstream repositoryUpstream, input repositorySyncInput) repositorySyncResult {
	req := journal.Request
	result := repositorySyncResult{RepositoryID: journal.RepositoryID, Outcome: journal.Outcome, DryRun: input.DryRun,
		TrackingRef: upstream.TrackingRef, BaseCommit: req.BaseCommit, UpstreamCommit: req.SourceCommit, Commit: req.Commit,
		Revision: journal.Revision, Conflicts: append([]string{}, journal.Conflicts...), TaskID: journal.TaskID,
		DefaultRef: journal.DefaultRef, Applied: !input.DryRun && journal.State == "complete" && req.Commit != ""}
	switch {
	case input.DryRun:
		result.Next = "Preview only; nothing was fetched into Foundation. Rerun without dry_run under a new operation ID to apply."
	case journal.Outcome == "conflict" && journal.TaskID != "":
		result.Next = "Upstream tip imported; the conflict-resolution source task was created and assigned. It needs normal readiness/approval, a pushed two-parent merge and independent verification."
	case journal.Outcome == "conflict":
		result.Next = "Upstream tip imported to the tracking ref. Resolve locally with a two-parent merge (current default first, upstream second) and submit resolution, or rerun with assign_to."
	}
	return result
}

// upstreamGit runs Git against the private sync repository. Upstream access keeps
// this account's own Git configuration and credential helpers (never Foundation's
// identity) but limits protocols and disables hooks, prompts and LFS smudging.
func upstreamGit(ctx context.Context, gitDirectory string, upstream bool, extra []string, args ...string) ([]byte, int, error) {
	binary, err := exec.LookPath("git")
	if err != nil {
		return nil, 0, domain.Fail(domain.Prerequisite, "native Git is required for upstream sync")
	}
	environment := process.Environment(extra...)
	if upstream {
		if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
			environment = append(environment, "SSH_AUTH_SOCK="+socket)
		}
		environment = append(environment, "GIT_ALLOW_PROTOCOL=https:ssh:file")
	} else {
		environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file")
	}
	environment = append(environment, "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_DIR="+gitDirectory)
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.autocrlf=false",
		"-c", "transfer.fsckObjects=true", "-c", "submodule.recurse=false", "-c", "gc.auto=0", "-c", "gc.autoDetach=false",
		"-c", "maintenance.auto=false", "-c", "fetch.writeCommitGraph=false"}, args...)
	output, diagnostic := process.NewBuffer(4<<20), process.NewBuffer(128<<10)
	runErr := (process.Native{}).Run(ctx, process.Spec{Path: binary, Args: args, Dir: gitDirectory, Env: environment, Stdout: output, Stderr: diagnostic})
	data, bufferErr := output.Bytes()
	if bufferErr != nil {
		return nil, 0, bufferErr
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && exit.ExitCode() > 0 {
		return data, exit.ExitCode(), nil
	}
	if runErr != nil {
		return nil, 0, errors.Join(domain.Fail(domain.Prerequisite, "native Git upstream operation failed; private sync evidence was preserved"), runErr)
	}
	return data, 0, nil
}

func syncGit(ctx context.Context, gitDirectory string, upstream bool, extra []string, args ...string) (string, error) {
	output, code, err := upstreamGit(ctx, gitDirectory, upstream, extra, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", domain.Fail(domain.Prerequisite, fmt.Sprintf("native Git %s failed (exit %d); check the upstream URL, branch and this account's Git credentials", args[0], code))
	}
	return strings.TrimSpace(string(output)), nil
}

func (a *app) fetchHostedCommit(ctx context.Context, r *resources, repo domain.HostedRepository, gitDirectory string, ref domain.GitRef, fetched map[string]bool) error {
	if fetched[ref.ArtifactID] {
		return nil
	}
	staging, err := os.CreateTemp(r.config.StateDir, "source-sync-*.bundle")
	if err != nil {
		return err
	}
	_, downloadErr := r.client.DownloadArtifact(ctx, ref.ArtifactID, staging, false)
	closeErr := staging.Close()
	if err := errors.Join(downloadErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(staging.Name()))
	}
	_, fetchErr := syncGit(ctx, gitDirectory, false, nil, "fetch", "--no-tags", "--no-write-fetch-head", "--", staging.Name(), ref.Commit)
	if err := errors.Join(fetchErr, os.Remove(staging.Name())); err != nil {
		return err
	}
	fetched[ref.ArtifactID] = true
	return nil
}

func isAncestor(ctx context.Context, gitDirectory, ancestor, commit string) (bool, error) {
	_, code, err := upstreamGit(ctx, gitDirectory, false, nil, "merge-base", "--is-ancestor", ancestor, commit)
	if err != nil {
		return false, err
	}
	if code > 1 {
		return false, domain.Fail(domain.Prerequisite, "native Git could not compare source history")
	}
	return code == 0, nil
}

func (a *app) prepareUpstreamSync(ctx context.Context, r *resources, repo domain.HostedRepository, upstream repositoryUpstream, input repositorySyncInput) (journal repositorySyncJournal, err error) {
	scratch := filepath.Join(r.config.StateDir, "source-sync", a.operation+".git")
	if err := os.RemoveAll(scratch); err != nil {
		return journal, err
	}
	if err := secure.Dir(scratch); err != nil {
		return journal, err
	}
	defer func() {
		// Scratch holds only fetched copies; failing to remove it must still be reported.
		err = errors.Join(err, os.RemoveAll(scratch))
	}()
	if _, err := syncGit(ctx, scratch, false, nil, "init", "--bare", "--quiet"); err != nil {
		return journal, err
	}
	defaultRef := "refs/heads/" + repo.DefaultBranch
	base, hasBase := repositoryHostedRef(repo, defaultRef)
	tracking, hasTracking := repositoryHostedRef(repo, upstream.TrackingRef)
	fetched := map[string]bool{}
	for _, ref := range []domain.GitRef{base, tracking} {
		if ref.Commit == "" {
			continue
		}
		if err := a.fetchHostedCommit(ctx, r, repo, scratch, ref, fetched); err != nil {
			return journal, err
		}
	}
	if _, err := syncGit(ctx, scratch, true, nil, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--", upstream.URL, "+refs/heads/"+upstream.Branch+":refs/fdn-sync/upstream"); err != nil {
		return journal, err
	}
	head, err := syncGit(ctx, scratch, false, nil, "rev-parse", "--verify", "refs/fdn-sync/upstream^{commit}")
	if err != nil || !gitrepo.Commit(head) {
		return journal, domain.Fail(domain.Prerequisite, "upstream branch did not resolve to an exact commit")
	}
	journal.Request = domain.RepositoryMerge{Command: domain.Command{OperationID: a.operation, ExpectedRevision: repo.Revision},
		SourceRef: upstream.TrackingRef, SourceCommit: head, BaseCommit: base.Commit, Reason: input.Reason}
	journal.Conflicts = []string{}
	if hasTracking && tracking.Commit != head {
		forward, err := isAncestor(ctx, scratch, tracking.Commit, head)
		if err != nil {
			return journal, err
		}
		if !forward {
			return journal, domain.Fail(domain.Conflict, "upstream history was rewritten since the last import; Foundation will not replace retained upstream history")
		}
	}
	switch {
	case !hasBase:
		journal.Outcome, journal.Request.Commit = "initial", head
	case base.Commit == head:
		journal.Outcome = "up-to-date"
	default:
		contained, err := isAncestor(ctx, scratch, head, base.Commit)
		if err != nil {
			return journal, err
		}
		forward, err := isAncestor(ctx, scratch, base.Commit, head)
		if err != nil {
			return journal, err
		}
		switch {
		case contained:
			journal.Outcome = "up-to-date"
		case forward:
			journal.Outcome, journal.Request.Commit = "fast-forward", head
		default:
			if _, code, err := upstreamGit(ctx, scratch, false, nil, "merge-base", base.Commit, head); err != nil || code != 0 {
				return journal, errors.Join(err, domain.Fail(domain.Prerequisite, "hosted default and upstream have unrelated histories; integrate them explicitly"))
			}
			output, code, err := upstreamGit(ctx, scratch, false, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", base.Commit, head)
			if err != nil {
				return journal, err
			}
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			switch code {
			case 0:
				if len(lines) != 1 || len(lines[0]) != 40 {
					return journal, domain.Fail(domain.Prerequisite, "native Git returned an unexpected merge result")
				}
				commit, err := mergeCommit(ctx, scratch, lines[0], base.Commit, head, repo.DefaultBranch, upstream.Branch)
				if err != nil {
					return journal, err
				}
				journal.Outcome, journal.Request.Commit = "merged", commit
			case 1:
				journal.Outcome = "conflict"
				for _, path := range lines[1:] {
					if path != "" && !slices.Contains(journal.Conflicts, path) && len(journal.Conflicts) < maxReportedConflicts {
						journal.Conflicts = append(journal.Conflicts, path)
					}
				}
			default:
				return journal, domain.Fail(domain.Prerequisite, "native Git merge-tree failed; Git 2.38 or newer is required")
			}
		}
	}
	if journal.Request.Commit == "" && hasTracking && tracking.Commit == head {
		// Nothing new to import; a retained conflict may still be assigned.
		journal.State, journal.Revision = "complete", repo.Revision
		if journal.Outcome == "conflict" {
			journal.State = "imported"
		}
		return journal, nil
	}
	if input.DryRun {
		return journal, nil
	}
	refs := []string{upstream.TrackingRef}
	if _, err := syncGit(ctx, scratch, false, nil, "update-ref", upstream.TrackingRef, head); err != nil {
		return journal, err
	}
	if journal.Request.Commit != "" {
		if _, err := syncGit(ctx, scratch, false, nil, "update-ref", defaultRef, journal.Request.Commit); err != nil {
			return journal, err
		}
		refs = append(refs, defaultRef)
	}
	return journal, a.stageSyncBundle(ctx, r, &journal, func(path string) error {
		_, err := syncGit(ctx, scratch, false, nil, append([]string{"bundle", "create", "--version=2", path}, refs...)...)
		return err
	}, nil)
}

func mergeCommit(ctx context.Context, gitDirectory, tree, base, upstream, defaultBranch, upstreamBranch string) (string, error) {
	identity := func(key, fallback string) string {
		value, err := syncGit(ctx, gitDirectory, true, nil, "config", "--default", "", "--get", key)
		if err != nil || value == "" || strings.ContainsAny(value, "<>\n") {
			return fallback
		}
		return value
	}
	name, email := identity("user.name", "Foundation upstream sync"), identity("user.email", "upstream-sync@foundation.invalid")
	message := "Merge upstream/" + upstreamBranch + " into " + defaultBranch + "\n\nUpstream-Commit: " + upstream + "\n"
	extra := []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email}
	commit, err := syncGit(ctx, gitDirectory, false, extra, "commit-tree", tree, "-p", base, "-p", upstream, "-m", message)
	if err != nil || !gitrepo.Commit(commit) {
		return "", errors.Join(err, domain.Fail(domain.Prerequisite, "native Git could not create the upstream merge commit"))
	}
	return commit, nil
}

func (a *app) prepareUpstreamResolution(ctx context.Context, r *resources, repo domain.HostedRepository, upstream repositoryUpstream, input repositorySyncInput) (repositorySyncJournal, error) {
	var journal repositorySyncJournal
	resolution := input.Resolution
	if resolution.Workspace == "" || gitrepo.ValidateRef(resolution.Ref) != nil || resolution.Ref == "HEAD" {
		return journal, domain.Fail(domain.Invalid, "resolution requires an owned workspace reference and an exact refs/... merge ref")
	}
	defaultRef := "refs/heads/" + repo.DefaultBranch
	base, hasBase := repositoryHostedRef(repo, defaultRef)
	tracking, hasTracking := repositoryHostedRef(repo, upstream.TrackingRef)
	if !hasBase || !hasTracking {
		return journal, domain.Fail(domain.Prerequisite, "resolution requires a hosted default branch and an imported upstream tracking ref; run sync first")
	}
	binding, err := r.state.Workspace(ctx, resolution.Workspace)
	if err != nil {
		return journal, err
	}
	head, err := work.GitCommand(ctx, binding.Path, "", "rev-parse", "--verify", resolution.Ref+"^{commit}")
	if err != nil {
		return journal, err
	}
	commit := strings.TrimSpace(string(head))
	parents, err := work.GitCommand(ctx, binding.Path, "", "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return journal, err
	}
	if fields := strings.Fields(string(parents)); len(fields) != 3 || fields[0] != commit || fields[1] != base.Commit || fields[2] != tracking.Commit {
		return journal, domain.Fail(domain.Conflict, "resolution must be a two-parent merge of the current hosted default (first parent) and the imported upstream tip (second parent)")
	}
	journal.Outcome, journal.Conflicts = "resolved", []string{}
	journal.Request = domain.RepositoryMerge{Command: domain.Command{OperationID: a.operation, ExpectedRevision: repo.Revision},
		SourceRef: upstream.TrackingRef, SourceCommit: tracking.Commit, BaseCommit: base.Commit, Commit: commit, Reason: input.Reason}
	return journal, a.stageSyncBundle(ctx, r, &journal, func(path string) error {
		_, err := work.GitCommand(ctx, binding.Path, "", "bundle", "create", "--version=2", path, resolution.Ref)
		return err
	}, func(data []byte) ([]byte, error) {
		end := bytes.Index(data, []byte("\n\n"))
		if end < 0 {
			return nil, domain.Fail(domain.Invalid, "local Git returned an invalid source bundle")
		}
		return append([]byte("# v2 git bundle\n"+commit+" "+defaultRef+"\n\n"), data[end+2:]...), nil
	})
}

func (a *app) stageSyncBundle(ctx context.Context, r *resources, journal *repositorySyncJournal, create func(string) error, rewrite func([]byte) ([]byte, error)) error {
	staging := filepath.Join(r.config.StateDir, "source-sync")
	if err := secure.Dir(staging); err != nil {
		return err
	}
	path := filepath.Join(staging, a.operation+".bundle")
	temporary := path + ".preparing"
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := create(temporary); err != nil {
		return err
	}
	file, err := os.Open(temporary)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, domain.MaxGitBundleBytes+1))
	err = errors.Join(readErr, file.Close(), os.Remove(temporary))
	if err != nil {
		return err
	}
	if int64(len(data)) > domain.MaxGitBundleBytes {
		return domain.Fail(domain.Prerequisite, "upstream source bundle exceeds the 64 MiB bound; nothing was imported")
	}
	if rewrite != nil {
		if data, err = rewrite(data); err != nil {
			return err
		}
	}
	bundle, err := gitrepo.Open(data)
	if err != nil {
		return err
	}
	if !bundle.Contains(journal.Request.SourceCommit) || (journal.Request.Commit != "" && !bundle.Contains(journal.Request.Commit)) {
		return domain.Fail(domain.Invalid, "prepared upstream bundle does not contain its exact commits")
	}
	if _, err := os.Lstat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := secure.Create(path, data); err != nil {
		return err
	}
	journal.Path, journal.State = path, "prepared"
	journal.Upload = domain.FileUploadRequest{Command: domain.Command{OperationID: a.operation + ".upload"}, Name: a.operation + ".bundle",
		MediaType: "application/octet-stream", Size: int64(len(data)), SHA256: domain.Hash(string(data))}
	return nil
}

func repositoryHostedRef(repo domain.HostedRepository, name string) (domain.GitRef, bool) {
	for _, ref := range repo.Refs {
		if ref.Name == name {
			return ref, true
		}
	}
	return domain.GitRef{}, false
}

func (a *app) assignUpstreamConflict(ctx context.Context, r *resources, journal repositorySyncJournal, upstream repositoryUpstream, input repositorySyncInput) (string, error) {
	repo, err := api.Call[domain.HostedRepository](ctx, r.client, "GET", api.Path("repositories", journal.RepositoryID), nil)
	if err != nil {
		return "", err
	}
	req := journal.Request
	short := req.SourceCommit[:12]
	ref := "refs/heads/work/" + input.AssignTo + "/upstream-" + upstream.Branch + "-" + short
	if gitrepo.ValidateRef(ref) != nil {
		ref = "refs/heads/work/" + input.AssignTo + "/upstream-" + short
	}
	conflicts := "(see the merge output)"
	if len(journal.Conflicts) > 0 {
		conflicts = "- " + strings.Join(journal.Conflicts, "\n- ")
	}
	description := "Integrate the imported upstream tip into the hosted default branch of " + repo.Name + ".\n\n" +
		"- Default branch: refs/heads/" + repo.DefaultBranch + " at " + req.BaseCommit + "\n" +
		"- Upstream tracking ref: " + upstream.TrackingRef + " at " + req.SourceCommit + "\n\n" +
		"Conflicting paths reported by the Founder's merge attempt:\n" + conflicts + "\n\n" +
		"Fetch both refs from fdn::" + repo.ID + ", merge the upstream commit into the default commit, resolve the conflicts while keeping both sides' intended behavior, run the repository's checks, and push the merge to the task work ref."
	criteria := "The pushed work ref is a two-parent merge whose first parent is " + req.BaseCommit + " and second parent is " + req.SourceCommit +
		"; all conflicts are resolved without dropping either side's intended changes; the repository's relevant checks pass; an independent verifier integrates the exact merge with repository merge."
	task, err := api.Call[domain.Task](ctx, r.client, "POST", "/v1/tasks", domain.TaskRequest{Command: domain.Command{OperationID: a.operation + ".task"},
		ProjectID: repo.ProjectID, Title: "Resolve upstream merge conflicts in " + truncateText(repo.Name, 80),
		Description: description, AcceptanceCriteria: criteria, Priority: 1, Importance: 2,
		Source: &domain.TaskSource{RepositoryID: repo.ID, Commit: req.BaseCommit, Ref: ref}})
	if err != nil {
		return "", err
	}
	if task.AssigneeID == input.AssignTo {
		return task.ID, nil
	}
	_, err = api.Call[domain.Task](ctx, r.client, "POST", api.Path("tasks", task.ID, "change"), domain.TaskChange{
		Command: domain.Command{OperationID: a.operation + ".assign", ExpectedRevision: task.Revision}, Kind: "assign",
		AgentID: input.AssignTo, Reason: "Founder assigned upstream conflict resolution"})
	return task.ID, err
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
