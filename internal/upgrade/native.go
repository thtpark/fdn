package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	guidance "github.com/thtpark/fdn"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type Native struct {
	Policy        Policy
	Runner        process.Runner
	Catalog       Catalog
	Now           func() time.Time
	Lease         func(context.Context, domain.UpgradeLeaseRequest) (domain.UpgradeLease, error)
	CheckServices func(context.Context) error
	Mailbox       func(context.Context, domain.MessageWorkerCommand) (domain.MessageWorkerReply, error)
}

type nativeJob struct {
	Permit domain.UpgradePermit `json:"permit"`
	State  string               `json:"state"`
	Backup string               `json:"backup_sha256"`
	Fence  string               `json:"session_fence_sha256"`
}

func (n *Native) policyHash() string {
	data, _ := json.Marshal(n.Policy)
	return domain.Hash(string(data))
}

func NewNative(policy Policy) *Native {
	native := &Native{Policy: policy, Runner: process.Native{}, Catalog: NewCatalog(), Now: time.Now}
	native.Lease = native.fetchLease
	native.CheckServices = native.checkServices
	native.Mailbox = native.messageMailbox
	return native
}

func (n *Native) checkServices(ctx context.Context) error {
	if n.Policy.Role == "agent" {
		var status struct {
			Identity struct{ Running bool } `json:"identity"`
			Worker   struct{ Running bool } `json:"worker"`
		}
		if err := n.jsonCommand(ctx, filepath.Join(n.Policy.BinaryDir, "agent-identity"),
			[]string{"service", "status", "--input", n.Policy.ServiceOptions}, true, &status); err != nil {
			return err
		}
		if !status.Identity.Running || !status.Worker.Running {
			return domain.Fail(domain.Unavailable, "upgraded identity/runner services have not both become healthy")
		}
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	uid, _, err := n.Policy.account()
	if err != nil {
		return err
	}
	for _, kind := range []string{"identity", "worker"} {
		unit, owner, binary := "fdn-"+kind+"-"+n.Policy.Name+".service", uint32(uid), "agent-service"
		if kind == "identity" {
			binary = "agent-identity"
		}
		if n.Policy.Role == "foundation" {
			unit, binary = "fdn-foundation.service", "fdn-service"
			if kind == "identity" {
				unit, owner = "fdn-accounts.service", 0
			}
		}
		args := []string{"show", unit, "--property=ActiveState,MainPID,NRestarts"}
		if n.Policy.Role == "agent" {
			args = append([]string{"--user"}, args...)
		}
		output := process.NewBuffer(8192)
		if err := n.command(ctx, "/usr/bin/systemctl", args, n.Policy.Role == "agent", output); err != nil {
			return err
		}
		data, err := output.Bytes()
		if err != nil {
			return err
		}
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok || fields[key] != "" {
				return domain.Fail(domain.Unavailable, "native service observation is malformed")
			}
			fields[key] = value
		}
		pid, err := strconv.Atoi(fields["MainPID"])
		if err != nil || pid <= 0 || fields["ActiveState"] != "active" || fields["NRestarts"] != "0" {
			return domain.Fail(domain.Unavailable, "native upgraded service is not stably running without restart")
		}
		info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != owner {
			return domain.Fail(domain.Denied, "native upgraded service is running as the wrong OS account")
		}
		actual, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
		if err != nil {
			return err
		}
		expected, err := filepath.EvalSymlinks(filepath.Join(n.Policy.BinaryDir, binary))
		if err != nil {
			return err
		}
		if actual != expected {
			return domain.Fail(domain.Conflict, "native service is not executing the activated release binary")
		}
	}
	return nil
}

func (n *Native) command(ctx context.Context, path string, args []string, asUser bool, stdout io.Writer) error {
	return n.commandInput(ctx, path, args, asUser, stdout, nil)
}

func (n *Native) commandInput(ctx context.Context, path string, args []string, asUser bool, stdout io.Writer, stdin io.Reader) error {
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root", "DEBIAN_FRONTEND=noninteractive"}
	if asUser {
		env = []string{"HOME=" + n.Policy.Home, "USER=" + n.Policy.User, "LOGNAME=" + n.Policy.User,
			"PATH=" + n.Policy.BinaryDir + ":/usr/local/bin:/usr/bin:/bin", "LC_ALL=C"}
		if runtime.GOOS == "linux" {
			uid, _, err := n.Policy.account()
			if err != nil {
				return err
			}
			directory := "/run/user/" + strconv.Itoa(uid)
			env = append(env, "XDG_RUNTIME_DIR="+directory, "DBUS_SESSION_BUS_ADDRESS=unix:path="+directory+"/bus")
		}
		uid, _, err := n.Policy.account()
		if err != nil {
			return err
		}
		if os.Geteuid() == 0 && uid != 0 {
			if runtime.GOOS != "linux" {
				return domain.Fail(domain.Denied, "macOS worker archive updater must run as its existing user, not root")
			}
			args = append([]string{"-u", n.Policy.User, "--", "/usr/bin/env", "-i"}, append(env, append([]string{path}, args...)...)...)
			path, env = "/usr/sbin/runuser", []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}
		}
	}
	diagnostic := process.NewBuffer(8192)
	var discarded *process.BoundedBuffer
	if stdout == nil {
		discarded = process.NewBuffer(64 << 10)
		stdout = discarded
	}
	err := n.Runner.Run(ctx, process.Spec{Path: path, Args: args, Env: env, Stdin: stdin, Stdout: stdout, Stderr: diagnostic})
	if err != nil {
		return errors.Join(domain.Fail(domain.Unavailable, "native upgrade command failed; inspect retained evidence and native logs before any retry"), err)
	}
	_, err = diagnostic.Bytes()
	if discarded != nil {
		_, outputErr := discarded.Bytes()
		err = errors.Join(err, outputErr)
	}
	return err
}

func (n *Native) fetchLease(ctx context.Context, request domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
	var lease domain.UpgradeLease
	payload, err := message.MarshalPayload(request)
	if err != nil {
		return lease, err
	}
	if n.Policy.Role == "agent" {
		output := process.NewBuffer(domain.MaxBody)
		runErr := n.commandInput(ctx, filepath.Join(n.Policy.BinaryDir, "agent-cli"),
			[]string{"--config", n.Policy.ConfigFile, "--timeout", "15", "upgrade-lease"},
			true, output, bytes.NewReader(payload))
		data, err := output.Bytes()
		if err != nil {
			return lease, err
		}
		if len(data) == 0 {
			return lease, errors.Join(domain.Fail(domain.Unavailable, "target could not obtain a fresh server authority lease"), runErr)
		}
		var reply domain.UpgradeNativeReply
		if err := message.UnmarshalPayload(data, &reply); err != nil {
			return lease, err
		}
		if reply.Error != nil {
			return lease, reply.Error
		}
		if reply.Lease == nil || runErr != nil {
			return lease, domain.Fail(domain.Unavailable, "native start-lease response is incomplete")
		}
		return *reply.Lease, nil
	}
	var server config.Server
	if err := n.readConfig(n.Policy.ConfigFile, &server); err != nil {
		return lease, err
	}
	uid, _, err := n.Policy.account()
	if err != nil {
		return lease, err
	}
	socket := filepath.Join(server.StateDir, "maintenance.sock")
	if err := protected(filepath.Dir(socket), uint32(uid)); err != nil {
		return lease, err
	}
	info, err := os.Lstat(socket)
	if err != nil {
		return lease, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(uid) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return lease, domain.Fail(domain.Denied, "Foundation start-lease socket ownership changed")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return lease, err
	}
	defer conn.Close()
	peer, ok := ingress.UID(ingress.ConnContext(ctx, conn))
	if !ok || peer != uint32(uid) {
		return lease, domain.Fail(domain.Denied, "Foundation start lease is not the pinned service OS peer")
	}
	session, err := message.NewSession(ctx, conn, nil)
	if err != nil {
		return lease, err
	}
	defer session.Close()
	data, err := session.Call(ctx, message.Request{Operation: "command.upgrades.lease", Payload: payload})
	if err != nil {
		return lease, err
	}
	return lease, message.UnmarshalPayload(data, &lease)
}

func (n *Native) startLease(ctx context.Context, permit domain.UpgradePermit, ca []byte) error {
	request := domain.UpgradeLeaseRequest{PermitSHA256: domain.Hash(permit.Signature), Nonce: domain.NewToken()}
	if n.Lease == nil {
		return domain.Fail(domain.Prerequisite, "native updater cannot start without a fresh server authority lease")
	}
	leaseCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	lease, err := n.Lease(leaseCtx, request)
	if err != nil {
		return err
	}
	if time.Since(start) >= 20*time.Second {
		return domain.Fail(domain.Unavailable, "native start-lease request exceeded its monotonic freshness bound")
	}
	return VerifyLease(lease, request, ca, n.Now())
}
func (n *Native) jsonCommand(ctx context.Context, binary string, arguments []string, asUser bool, target any) error {
	output := process.NewBuffer(domain.MaxBody)
	if err := n.command(ctx, binary, arguments, asUser, output); err != nil {
		return err
	}
	data, err := output.Bytes()
	if err != nil {
		return err
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *domain.Fault   `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return envelope.Error
		}
		return domain.Fail(domain.Unavailable, "native command returned an unsuccessful envelope")
	}
	return json.Unmarshal(envelope.Data, target)
}

func (n *Native) installed(ctx context.Context) (string, string, error) {
	binary := "agent-cli"
	if n.Policy.Role == "foundation" {
		binary = "fdn-service"
	}
	var result struct{ Version, Commit string }
	err := n.jsonCommand(ctx, filepath.Join(n.Policy.BinaryDir, binary), []string{"--version"}, true, &result)
	if err != nil {
		return "", "", err
	}
	_, err = CompareVersion(result.Version, result.Version)
	if err != nil || !sourceCommit.MatchString(result.Commit) {
		return "", "", domain.Fail(domain.Prerequisite, "native updater cannot replace development or unknown-source installations")
	}
	return result.Version, result.Commit, nil
}

func (n *Native) readConfig(path string, value any) error {
	uid, _, err := n.Policy.account()
	if err != nil {
		return err
	}
	if err := protected(path, uint32(uid)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (owner.Uid != uint32(uid) && owner.Uid != 0) || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0077 != 0 || info.Size() > domain.MaxBody {
		return domain.Fail(domain.Denied, "native application configuration is not private or owned by its pinned service user")
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if err != nil {
		return err
	}
	return config.Decode(data, value)
}

func (n *Native) applicationLock(path string) (*os.File, error) {
	uid, _, err := n.Policy.account()
	if err != nil {
		return nil, err
	}
	if err := protected(filepath.Dir(path), uint32(uid)); err != nil {
		return nil, err
	}
	// Native services already created their lock; never adopt/create a new
	// root-owned application lock during an upgrade.
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(uid) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			err = domain.Fail(domain.Denied, "application lock does not belong to its pinned service user")
		}
	}
	if err == nil {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (n *Native) applicationState() (string, error) {
	var state string
	if n.Policy.Role == "agent" {
		var worker config.Worker
		if err := n.readConfig(n.Policy.ConfigFile, &worker); err != nil {
			return "", err
		}
		state = worker.StateDir
	} else {
		var server config.Server
		if err := n.readConfig(n.Policy.ConfigFile, &server); err != nil {
			return "", err
		}
		state = server.StateDir
	}
	if !filepath.IsAbs(state) || !inside(n.Policy.Home, state) || inside(state, n.Policy.StateDir) ||
		inside(n.Policy.StateDir, state) {
		return "", domain.Fail(domain.Denied, "application state must remain in its pinned service HOME, outside native updater evidence")
	}
	return state, nil
}

func (n *Native) lockApplication() (*os.File, error) {
	state, err := n.applicationState()
	if err != nil {
		return nil, err
	}
	name := "work.lock"
	if n.Policy.Role == "foundation" {
		name = "foundation.lock"
	}
	return n.applicationLock(filepath.Join(state, name))
}

func (n *Native) target(ctx context.Context) (int64, error) {
	if n.Policy.Role == "foundation" {
		return 1, nil
	}
	var identity domain.AgentIdentity
	err := n.jsonCommand(ctx, filepath.Join(n.Policy.BinaryDir, "agent-cli"),
		[]string{"--config", n.Policy.ConfigFile, "agent", "identity"}, true, &identity)
	if err != nil {
		return 0, err
	}
	if identity.AgentID != n.Policy.TargetID || identity.InstanceID != n.Policy.InstanceID || identity.State != "active" {
		return 0, domain.Fail(domain.Denied, "native updater policy does not match the currently enrolled local identity")
	}
	return identity.Generation, nil
}

func (n *Native) idle(ctx context.Context) (string, error) {
	if n.Policy.Role == "foundation" {
		return "", nil
	}
	var result struct {
		Pending []json.RawMessage `json:"pending_invocations"`
		Fence   json.RawMessage   `json:"session_fence"`
		Work    struct {
			Mode           string `json:"mode"`
			PendingReports *int   `json:"pending_reports"`
		} `json:"work"`
	}
	if err := n.jsonCommand(ctx, filepath.Join(n.Policy.BinaryDir, "agent-cli"),
		[]string{"--config", n.Policy.ConfigFile, "work", "status"}, true, &result); err != nil {
		return "", err
	}
	if result.Pending == nil || result.Work.PendingReports == nil {
		return "", domain.Fail(domain.Unavailable, "native work status omitted required pending-invocation/report observations")
	}
	if len(result.Pending) != 0 || *result.Work.PendingReports != 0 {
		return "", domain.Fail(domain.Prerequisite, "worker must be paused with no pending invocation/report; existing fences are preserved, never cleared")
	}
	if result.Work.Mode != "manual" {
		return "", errors.Join(domain.Fail(domain.Prerequisite, "worker has not yet observed the authoritative Founder pause"), pauseObservationPending)
	}
	return domain.Hash(string(result.Fence)), nil
}

var pauseObservationPending = errors.New("waiting for local Founder pause observation")

func (n *Native) waitIdle(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		fence, err := n.idle(ctx)
		if !errors.Is(err, pauseObservationPending) {
			return fence, err
		}
		select {
		case <-ctx.Done():
			return "", errors.Join(err, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (n *Native) readyAfterIdentity(ctx context.Context, generation int64) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		var err error
		if n.Policy.Role == "agent" {
			current, readErr := n.target(ctx)
			err = readErr
			if err == nil && current != generation {
				return domain.Fail(domain.Conflict, "native identity generation changed; runner was not started")
			}
		} else {
			var server config.Server
			if err := n.readConfig(n.Policy.ConfigFile, &server); err != nil {
				return err
			}
			info, readErr := os.Lstat(server.AccountSocket)
			err = readErr
			if err == nil {
				owner, ok := info.Sys().(*syscall.Stat_t)
				if !ok || owner.Uid != 0 || info.Mode()&os.ModeSocket == 0 {
					return domain.Fail(domain.Denied, "Foundation account helper is not the expected protected root socket")
				}
			}
		}
		if err == nil {
			return nil
		}
		code := domain.AsFault(err).Code
		if n.Policy.Role == "agent" && code != domain.Unavailable && code != domain.Prerequisite {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(domain.Fail(domain.Unavailable, "identity/account helper readiness was not proven before starting the next native service"), err, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (n *Native) services(ctx context.Context, action string, generation int64) error {
	p := n.Policy
	if runtime.GOOS == "linux" {
		args := []string{}
		if p.Role == "agent" {
			args = append(args, "--user")
		}
		units := []string{"fdn-identity-" + p.Name + ".service", "fdn-worker-" + p.Name + ".service"}
		if p.Role == "foundation" {
			units = []string{"fdn-accounts.service", "fdn-foundation.service"}
		}
		if action == "stop" {
			units[0], units[1] = units[1], units[0]
		}
		for i, unit := range units {
			if err := n.command(ctx, "/usr/bin/systemctl", append(append([]string{}, args...), action, unit), p.Role == "agent", nil); err != nil {
				return err
			}
			if action == "start" && i == 0 {
				if err := n.readyAfterIdentity(ctx, generation); err != nil {
					return err
				}
			}
		}
		return nil
	}
	uid, _, err := p.account()
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(uid)
	for i, kind := range []string{"identity", "runner"} {
		label := "org.foundation." + kind + "." + p.Name
		args := []string{"bootstrap", domain, filepath.Join(p.Home, "Library", "LaunchAgents", label+".plist")}
		if action == "stop" {
			args = []string{"bootout", domain + "/" + label}
		}
		if err := n.command(ctx, "/bin/launchctl", args, true, nil); err != nil {
			return err
		}
		if action == "start" && i == 0 {
			if err := n.readyAfterIdentity(ctx, generation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *Native) backupPaths() ([]string, error) {
	state, err := n.applicationState()
	if err != nil {
		return nil, err
	}
	paths := append([]string{}, n.Policy.BackupPaths...)
	paths = append(paths, state, n.Policy.ConfigFile, n.Policy.CAFile)
	if n.Policy.Role == "agent" {
		var identity config.IdentityService
		if err := n.readConfig(n.Policy.IdentityConfig, &identity); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(identity.StateDir) || !inside(n.Policy.Home, identity.StateDir) ||
			inside(identity.StateDir, n.Policy.StateDir) || inside(n.Policy.StateDir, identity.StateDir) {
			return nil, domain.Fail(domain.Denied, "identity state is outside its pinned service HOME or overlaps updater evidence")
		}
		paths = append(paths, identity.StateDir, n.Policy.IdentityConfig, n.Policy.ServiceOptions)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			return nil, domain.Fail(domain.Prerequisite, "an explicitly required cold-backup path is unavailable; fix native policy before stopping services")
		}
	}
	return paths, nil
}

func (n *Native) backup(ctx context.Context, directory, recipient string) (string, error) {
	paths, err := n.backupPaths()
	if err != nil {
		return "", err
	}
	public, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(directory, "before-upgrade.tar.age"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writer, err := age.Encrypt(io.MultiWriter(file, hash), public)
	if err != nil {
		file.Close()
		return "", err
	}
	args := append([]string{"-czf", "-", "--"}, paths...)
	err = n.command(ctx, "/usr/bin/tar", args, false, writer)
	err = errors.Join(err, writer.Close(), file.Sync(), file.Close())
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (n *Native) verify(ctx context.Context, job nativeJob) (domain.UpgradeNativeReply, error) {
	reply := domain.UpgradeNativeReply{State: job.State, BackupSHA256: job.Backup}
	version, commit, err := n.installed(ctx)
	if err != nil {
		return reply, err
	}
	reply.Version, reply.Commit = version, commit
	if job.State != "verified" {
		return reply, nil
	}
	if n.CheckServices == nil {
		return reply, domain.Fail(domain.Prerequisite, "native running-service verification is not configured")
	}
	if err := n.CheckServices(ctx); err != nil {
		return reply, err
	}
	if version != job.Permit.Proposal.Release.Version || commit != job.Permit.Proposal.Release.Commit {
		return reply, domain.Fail(domain.Conflict, "retained upgrade receipt is not the currently installed source")
	}
	if n.Policy.Role == "agent" {
		generation, err := n.target(ctx)
		if err != nil {
			return reply, err
		}
		if generation != job.Permit.Proposal.Challenge.Generation {
			return reply, domain.Fail(domain.Conflict, "upgrade changed the retained identity generation")
		}
		fence, err := n.idle(ctx)
		if err != nil {
			return reply, err
		}
		if fence != job.Fence {
			return reply, domain.Fail(domain.Conflict, "upgrade changed the retained session fence")
		}
	}
	binary := "agent-cli"
	args := []string{"--config", n.Policy.ConfigFile, "doctor"}
	if n.Policy.Role == "foundation" {
		binary = "fdn-service"
	}
	var result map[string]any
	return reply, n.jsonCommand(ctx, filepath.Join(n.Policy.BinaryDir, binary), args, true, &result)
}

func (n *Native) Handle(ctx context.Context, request domain.UpgradeNativeRequest) (domain.UpgradeNativeReply, error) {
	var reply domain.UpgradeNativeReply
	if !domain.ValidOperation(request.ID) || (request.Action != "inspect" && request.Action != "challenge" && request.Action != "prepare" && request.Action != "apply" && request.Action != "status") {
		return reply, domain.Fail(domain.Invalid, "native updater accepts only exact challenge/prepare/apply/status requests")
	}
	p := n.Policy
	if err := p.Validate(); err != nil {
		return reply, err
	}
	if request.Action == "inspect" {
		version, commit, err := n.installed(ctx)
		if err != nil {
			return reply, err
		}
		generation, err := n.target(ctx)
		if err != nil {
			return reply, err
		}
		return domain.UpgradeNativeReply{State: "observed", Version: version, Commit: commit,
			Challenge: &domain.UpgradeChallenge{InstanceID: p.InstanceID, TargetID: p.TargetID, Role: p.Role,
				Generation: generation, CurrentVersion: version, CurrentCommit: commit, PolicySHA256: n.policyHash()}}, nil
	}
	if request.Action == "status" {
		if _, err := os.Lstat(p.StateDir); err != nil {
			return reply, err
		}
	}
	if err := secure.Dir(p.StateDir); err != nil {
		return reply, err
	}
	directory := filepath.Join(p.StateDir, request.ID)
	jobPath, challengePath := filepath.Join(directory, "job.json"), filepath.Join(directory, "challenge.json")
	if request.Action == "status" {
		var job nativeJob
		if err := config.Read(jobPath, &job); err != nil {
			return reply, err
		}
		if job.Permit.Proposal.Challenge.PolicySHA256 != n.policyHash() {
			return reply, domain.Fail(domain.Conflict, "native policy changed; retained receipt is not authority for the new configuration")
		}
		reply, err := n.verify(ctx, job)
		if err != nil {
			return reply, err
		}
		if job.State != "verified" && job.State != "prepared" {
			lock, lockErr := secure.Acquire(filepath.Join(p.StateDir, "upgrade.lock"))
			if lockErr == nil {
				lock.Close()
				reply.State, reply.Code = "operator-required", job.State
			} else if domain.AsFault(lockErr).Code != domain.Conflict {
				return reply, lockErr
			}
		}
		return reply, nil
	}
	lock, err := secure.Acquire(filepath.Join(p.StateDir, "upgrade.lock"))
	if err != nil {
		return reply, err
	}
	defer lock.Close()
	if request.Action == "challenge" {
		entries, err := os.ReadDir(p.StateDir)
		if err != nil {
			return reply, err
		}
		if len(entries) >= 1024 {
			return reply, domain.Fail(domain.Prerequisite, "native upgrade evidence inventory is full; an administrator must review retention without erasing uncertainty")
		}
	}
	if err := secure.Dir(directory); err != nil {
		return reply, err
	}
	if request.Action == "challenge" {
		var challenge domain.UpgradeChallenge
		if err := config.Read(challengePath, &challenge); err == nil {
			return domain.UpgradeNativeReply{State: "planned", Challenge: &challenge}, nil
		} else if !os.IsNotExist(err) {
			return reply, err
		}
		version, commit, err := n.installed(ctx)
		if err != nil {
			return reply, err
		}
		generation, err := n.target(ctx)
		if err != nil {
			return reply, err
		}
		challenge = domain.UpgradeChallenge{ID: request.ID, Nonce: domain.NewToken(), Role: p.Role,
			InstanceID: p.InstanceID, TargetID: p.TargetID, Generation: generation,
			CurrentVersion: version, CurrentCommit: commit, PolicySHA256: n.policyHash(), ExpiresAt: n.Now().Unix() + 900}
		if err := config.Write(challengePath, challenge); err != nil {
			return reply, err
		}
		return domain.UpgradeNativeReply{State: "planned", Challenge: &challenge}, nil
	}
	if request.Permit == nil {
		return reply, domain.Fail(domain.Denied, "native updater requires a Foundation-signed Founder authorization, not an ordinary message")
	}
	permit := *request.Permit
	var challenge domain.UpgradeChallenge
	if err := config.Read(challengePath, &challenge); err != nil {
		return reply, err
	}
	if challenge != permit.Proposal.Challenge || challenge.ID != request.ID || challenge.InstanceID != p.InstanceID ||
		challenge.TargetID != p.TargetID || challenge.Role != p.Role || challenge.PolicySHA256 != n.policyHash() {
		return reply, domain.Fail(domain.Denied, "upgrade authorization does not match this native target challenge")
	}
	var job nativeJob
	readErr := config.Read(jobPath, &job)
	if readErr == nil {
		old, _ := json.Marshal(job.Permit)
		next, _ := json.Marshal(permit)
		if !bytes.Equal(old, next) {
			return reply, domain.Fail(domain.Conflict, "native operation ID belongs to another immutable upgrade proposal")
		}
		if job.State == "verified" || request.Action == "prepare" && job.State == "prepared" {
			return n.verify(ctx, job)
		}
	} else if !os.IsNotExist(readErr) {
		return reply, readErr
	}
	ca, err := secure.ReadPublic(p.CAFile, domain.MaxBody)
	if err != nil {
		return reply, err
	}
	if err := VerifyPermit(permit, ca, n.Now()); err != nil {
		return reply, err
	}
	if request.Action == "prepare" {
		if readErr == nil {
			return reply, domain.Fail(domain.Conflict, "native upgrade has uncertain side effects; inspect its exact status, never blindly rerun it")
		}
		version, commit, err := n.installed(ctx)
		if err != nil {
			return reply, err
		}
		generation, err := n.target(ctx)
		if err != nil {
			return reply, err
		}
		if version != challenge.CurrentVersion || commit != challenge.CurrentCommit || generation != challenge.Generation {
			return reply, domain.Fail(domain.Conflict, "native software or identity changed since upgrade planning")
		}
		release := permit.Proposal.Release
		if release.Platform != runtime.GOOS+"-"+runtime.GOARCH {
			return reply, domain.Fail(domain.Invalid, "release platform differs from the native computer")
		}
		if err := n.Catalog.Download(ctx, release, filepath.Join(directory, "release.zip")); err != nil {
			return reply, err
		}
		if _, err := ExtractBundle(filepath.Join(directory, "release.zip"), filepath.Join(directory, "bundle"), release); err != nil {
			return reply, err
		}
		fence, err := n.waitIdle(ctx)
		if err != nil {
			return reply, err
		}
		if _, err := n.backupPaths(); err != nil {
			return reply, err
		}
		if err := n.startLease(ctx, permit, ca); err != nil {
			return reply, err
		}
		job = nativeJob{Permit: permit, State: "stopping", Fence: fence}
		if err := config.Write(jobPath, job); err != nil {
			return reply, err
		}
		if err := n.services(ctx, "stop", challenge.Generation); err != nil {
			return reply, err
		}
		workLock, err := n.lockApplication()
		if err != nil {
			return reply, err
		}
		defer func() {
			if workLock != nil {
				workLock.Close()
			}
		}()
		job.Backup, err = n.backup(ctx, directory, permit.Proposal.BackupRecipient)
		if err != nil {
			return reply, err
		}
		job.State = "prepared"
		if err := config.Write(jobPath, job); err != nil {
			return reply, err
		}
		return n.verify(ctx, job)
	}
	if job.State != "prepared" {
		return reply, domain.Fail(domain.Conflict, "native apply requires the exact successful cold-backup receipt")
	}
	workLock, err := n.lockApplication()
	if err != nil {
		return reply, err
	}
	defer func() {
		if workLock != nil {
			workLock.Close()
		}
	}()
	job.State = "installing"
	if err := config.Write(jobPath, job); err != nil {
		return reply, err
	}
	if p.Method == "debian" {
		role := "agent"
		if p.Role == "foundation" {
			role = "foundation"
		}
		architecture := strings.TrimPrefix(permit.Proposal.Release.Platform, "linux-")
		name := "fdn-" + role + "_" + permit.Proposal.Release.Version + "_" + architecture + ".deb"
		if err := n.command(ctx, "/usr/bin/dpkg", []string{"-i", filepath.Join(directory, "bundle", name)}, false, nil); err != nil {
			return reply, err
		}
	} else {
		script := filepath.Join(directory, "install.sh")
		if err := secure.Write(script, []byte(guidance.NativeInstaller)); err != nil {
			return reply, err
		}
		sum, err := fileHash(filepath.Join(directory, "bundle", "SHA256SUMS"))
		if err != nil {
			return reply, err
		}
		if err := n.command(ctx, "/bin/sh", []string{script, "--bundle", filepath.Join(directory, "bundle"),
			"--manifest-sha256", sum, "--prefix", p.Prefix, "--acknowledge-stopped"}, false, nil); err != nil {
			return reply, err
		}
	}
	if err := workLock.Close(); err != nil {
		return reply, err
	}
	workLock = nil
	if err := n.services(ctx, "start", challenge.Generation); err != nil {
		return reply, err
	}
	job.State = "verified"
	verifyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		reply, err = n.verify(verifyCtx, job)
		if err == nil {
			break
		}
		code := domain.AsFault(err).Code
		if verifyCtx.Err() != nil || (code != domain.Unavailable && code != domain.Prerequisite) {
			return reply, err
		}
		select {
		case <-verifyCtx.Done():
			return reply, errors.Join(err, verifyCtx.Err())
		case <-time.After(time.Second):
		}
	}
	return reply, config.Write(jobPath, job)
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), err
}
