package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/platform"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

type additionTarget struct {
	Host         string `json:"host"`
	User         string `json:"user"`
	Name         string `json:"name"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identity_file,omitempty"`
}

type agentAddition struct {
	OperationID string                 `json:"operation_id"`
	FounderID   string                 `json:"founder_id"`
	InstanceID  string                 `json:"instance_id"`
	Target      additionTarget         `json:"target"`
	Stage       string                 `json:"stage"`
	Pending     string                 `json:"uncertain_native_stage,omitempty"`
	Directory   string                 `json:"evidence_directory"`
	Home        string                 `json:"target_home,omitempty"`
	UID         int                    `json:"target_uid,omitempty"`
	GID         int                    `json:"target_gid,omitempty"`
	Release     domain.UpgradeRelease  `json:"release"`
	Public      *domain.PublicIdentity `json:"public_request,omitempty"`
	Identity    *domain.AgentIdentity  `json:"identity,omitempty"`
	Installed   bool                   `json:"installed"`
	Enrolled    bool                   `json:"enrolled"`
	Connected   bool                   `json:"connected"`
	Mode        string                 `json:"mode,omitempty"`
	Version     string                 `json:"observed_version,omitempty"`
	Commit      string                 `json:"observed_commit,omitempty"`
}

func (a *app) agentAddition(ctx context.Context, r *resources, args []string) (any, error) {
	if args[0] == "add-status" {
		if err := requireArgs(args, 2); err != nil {
			return nil, err
		}
		if !domain.ValidOperation(args[1]) {
			return nil, domain.Fail(domain.Invalid, "invalid addition operation ID")
		}
		var saved agentAddition
		found, err := r.state.Get(ctx, "agent-add:"+args[1], &saved)
		if err != nil {
			return nil, err
		}
		var credential config.Credential
		if err := config.Read(r.config.CredentialFile, &credential); err != nil {
			return nil, err
		}
		if !found {
			return nil, domain.Fail(domain.NotFound, "no retained addition exists for this operation")
		}
		if saved.FounderID != credential.AgentID {
			return nil, domain.Fail(domain.Denied, "addition evidence belongs to another identity")
		}
		return saved, nil
	}
	target, err := parseAdditionTarget(args[1:])
	if err != nil {
		return nil, err
	}
	if err := additionFounder(ctx, r); err != nil {
		return nil, err
	}
	input, _, diagnostic, err := a.humanTerminal()
	if err != nil {
		return nil, err
	}
	return a.addAgentWith(ctx, r, target, upgrade.NewCatalog(), process.Native{}, input, diagnostic)
}

func parseAdditionTarget(args []string) (additionTarget, error) {
	var target additionTarget
	flags := flag.NewFlagSet("agent add", flag.ContinueOnError)
	flags.StringVar(&target.Host, "host", "", "new worker's literal IP address")
	flags.StringVar(&target.User, "user", "", "existing non-root installing SSH user")
	flags.StringVar(&target.Name, "name", "", "new Foundation agent name")
	flags.IntVar(&target.Port, "port", 22, "SSH port")
	flags.StringVar(&target.IdentityFile, "identity-file", "", "optional local SSH private-key file; never transferred")
	if err := parse(flags, args); err != nil {
		return target, err
	}
	address, err := netip.ParseAddr(target.Host)
	if err != nil || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" ||
		target.Port < 1 || target.Port > 65535 ||
		!regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9._-]{0,63}$`).MatchString(target.User) || target.User == "root" {
		return target, domain.Fail(domain.Invalid, "provide a literal IP, port 1-65535 and an existing non-root SSH username")
	}
	if _, err := domain.AgentUsername(target.Name); err != nil {
		return target, err
	}
	if target.IdentityFile != "" {
		if !filepath.IsAbs(target.IdentityFile) {
			return target, domain.Fail(domain.Invalid, "SSH identity-file must be absolute")
		}
		if _, err := secure.Read(target.IdentityFile, 1<<20); err != nil {
			return target, err
		}
	}
	return target, nil
}

func additionFounder(ctx context.Context, r *resources) error {
	me, err := api.Call[domain.Agent](ctx, r.client, "GET", "/v1/me", nil)
	if err != nil {
		return err
	}
	if !me.Active || me.Banned || !me.Founder || me.ID != r.client.Credential.AgentID {
		return domain.Fail(domain.Denied, "only active Founder may add a new agent")
	}
	return nil
}

func additionQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func additionSSH(target additionTarget, known string, first bool) []string {
	check := "yes"
	if first {
		check = "ask"
	}
	args := []string{"-F", "/dev/null", "-T", "-p", strconv.Itoa(target.Port), "-l", target.User,
		"-o", "StrictHostKeyChecking=" + check, "-o", "UserKnownHostsFile=" + known,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no",
		"-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	if target.IdentityFile != "" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", target.IdentityFile)
	}
	return append(args, target.Host)
}

func (a *app) addAgentWith(ctx context.Context, r *resources, target additionTarget, catalog upgrade.Catalog, runner process.Runner, input, diagnostic *os.File) (_ any, err error) {
	if err := additionFounder(ctx, r); err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(r.config.StateDir, "agent-add.lock"))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	key := "agent-add:" + a.operation
	var saved agentAddition
	found, err := r.state.Get(ctx, key, &saved)
	if err != nil {
		return nil, err
	}
	if found && (saved.Target != target || saved.FounderID != r.client.Credential.AgentID) {
		return nil, domain.Fail(domain.Conflict, "addition operation belongs to another exact target or Founder")
	}
	if saved.Pending != "" {
		return nil, domain.Fail(domain.Prerequisite, "native addition outcome is uncertain; inspect agent add-status and retained target evidence with an authorized operator, never blindly rerun installation or key preparation")
	}
	if saved.Stage == "connected" {
		return saved, nil
	}
	if !found {
		directory, err := os.MkdirTemp(r.config.StateDir, "agent-add-")
		if err != nil {
			return nil, err
		}
		saved = agentAddition{OperationID: a.operation, FounderID: r.client.Credential.AgentID,
			Target: target, Stage: "created", Directory: directory}
		if err := r.state.Put(ctx, key, saved); err != nil {
			return nil, err
		}
	}
	call := func(script string, body io.Reader, first bool) ([]byte, error) {
		if err := additionFounder(ctx, r); err != nil {
			return nil, err
		}
		output := process.NewBuffer(domain.MaxBody)
		args := additionSSH(target, filepath.Join(saved.Directory, "known_hosts"), first)
		args = append(args, "/bin/sh -c "+additionQuote(script))
		env := process.Environment()
		if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
			env = append(env, "SSH_AUTH_SOCK="+socket)
		}
		if err := runner.Run(ctx, process.Spec{Path: "ssh", Args: args, Stdin: body,
			Stdout: output, Stderr: diagnostic, Terminal: input, Env: env}); err != nil {
			return nil, domain.Fail(domain.Unavailable, "SSH addition step failed; inspect the protected terminal and retained operation before continuing")
		}
		return output.Bytes()
	}
	save := func() error { return r.state.Put(ctx, key, saved) }
	// A dispatched native mutation is never automatically repeated after a lost response.
	native := func(stage, script string, body io.Reader) ([]byte, error) {
		saved.Pending = stage
		if err := save(); err != nil {
			return nil, err
		}
		return call(script, body, false)
	}
	if saved.Stage == "created" {
		facts, err := call(`set -eu
test "$(id -u)" -ne 0
test -d "$HOME" && test ! -L "$HOME"
test ! -e "$HOME/.foundation" && test ! -L "$HOME/.foundation"
case "$(uname -s)" in
Linux) systemctl --user show-environment >/dev/null ;;
Darwin) launchctl print "gui/$(id -u)" >/dev/null ;;
*) exit 6 ;;
esac
printf '%s\n' "$(uname -s)" "$(uname -m)" "$(id -u)" "$(id -g)" "$(id -un)" "$HOME"
`, nil, true)
		if err != nil {
			return nil, err
		}
		if err := saved.setFacts(facts); err != nil {
			return nil, err
		}
		authority, err := api.Call[domain.UpgradeContext](ctx, r.client, "GET", "/v1/upgrades/context", nil)
		if err != nil {
			return nil, err
		}
		release, err := catalog.Published(ctx, authority.Version, saved.Release.Platform)
		if err != nil {
			return nil, err
		}
		if release.Commit != authority.Commit {
			return nil, domain.Fail(domain.Denied, "published package source does not match the running Foundation server")
		}
		saved.Release, saved.InstanceID, saved.Stage = release, authority.InstanceID, "inspected"
		if err := save(); err != nil {
			return nil, err
		}
	}
	root := filepath.Join(saved.Home, ".foundation")
	authority, err := api.Call[domain.UpgradeContext](ctx, r.client, "GET", "/v1/upgrades/context", nil)
	if err != nil {
		return nil, err
	}
	if authority.InstanceID != saved.InstanceID || authority.Version != saved.Release.Version || authority.Commit != saved.Release.Commit {
		return nil, domain.Fail(domain.Conflict, "Foundation source or instance changed; retained addition cannot install a different release")
	}
	binary := func(name string) string { return additionQuote(filepath.Join(root, "bin", name)) }
	identityConfig := filepath.Join(root, "config", "identity.json")
	workerConfig := filepath.Join(root, "config", "agent.json")
	identity := binary("agent-identity") + " --config " + additionQuote(identityConfig)
	cli := binary("agent-cli") + " --config " + additionQuote(workerConfig)
	marker := "test \"$(cat " + additionQuote(filepath.Join(root, "addition-operation")) + ")\" = " + additionQuote(a.operation) + "\n"
	if saved.Stage == "inspected" {
		archive, err := additionArchive(ctx, catalog, saved)
		if err != nil {
			return nil, err
		}
		defer archive.Close()
		script := "set -eu\numask 077\nmkdir -m 700 " + additionQuote(root) +
			"\nprintf '%s' " + additionQuote(a.operation) + " > " + additionQuote(filepath.Join(root, "addition-operation")) +
			"\ntar -xzf - -C " + additionQuote(root) + "\nchmod 700 " + additionQuote(filepath.Join(root, "bin")) + "\n"
		if _, err := native("installed", script, archive); err != nil {
			return nil, err
		}
		saved.Installed, saved.Pending, saved.Stage = true, "", "installed"
		if err := save(); err != nil {
			return nil, err
		}
	}
	if saved.Stage == "installed" {
		data, err := call(binary("agent-cli")+" --version", nil, false)
		if err != nil {
			return nil, err
		}
		var version struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
			API     int    `json:"api_version"`
			Schema  int    `json:"schema_version"`
		}
		if err := additionData(data, &version); err != nil {
			return nil, err
		}
		if version.Version != saved.Release.Version || version.Commit != saved.Release.Commit ||
			version.API != domain.APIVersion || version.Schema != domain.SchemaVersion {
			return nil, domain.Fail(domain.Denied, "installed target CLI source/version does not match the verified published server release")
		}
		saved.Version, saved.Commit = version.Version, version.Commit
		if err := save(); err != nil {
			return nil, err
		}
		worker := config.DefaultWorker(filepath.Join(root, "state"))
		worker.IdentitySocket = filepath.Join(root, "run", "identity.sock")
		configuration := config.IdentityService{StateDir: filepath.Join(root, "identity"), Socket: worker.IdentitySocket,
			WorkerUID: saved.UID, WorkerGID: saved.GID, Worker: worker}
		configurationScript, err := additionJSON(identityConfig, configuration)
		if err != nil {
			return nil, err
		}
		script := "set -eu\numask 077\n" + marker + "mkdir -m 700 " +
			additionQuote(filepath.Join(root, "config")) + " " + additionQuote(filepath.Join(root, "run")) + "\n" +
			cli + " config init >&2\n" + configurationScript +
			identity + " prepare --name " + additionQuote(target.Name) + " --output " + additionQuote(filepath.Join(root, "public-request.json"))
		data, err = native("prepared", script, nil)
		if err != nil {
			return nil, err
		}
		var public domain.PublicIdentity
		if err := additionData(data, &public); err != nil {
			return nil, err
		}
		if _, _, err := device.Validate(public); err != nil {
			return nil, err
		}
		if public.Name != target.Name {
			return nil, domain.Fail(domain.Denied, "target prepared a different agent name")
		}
		saved.Public, saved.Pending, saved.Stage = &public, "", "prepared"
		if err := save(); err != nil {
			return nil, err
		}
	}
	if saved.Public == nil {
		return nil, domain.Fail(domain.Prerequisite, "addition has no acknowledged signed public request; inspect retained evidence")
	}
	if saved.Identity == nil {
		req := domain.RegisterIdentityRequest{Command: domain.Command{OperationID: "add_" + domain.Hash(a.operation)}, Identity: *saved.Public}
		data, err := a.write(ctx, r, "/v1/identity/register", req, "")
		if err != nil {
			return nil, err
		}
		var identity domain.AgentIdentity
		if err := json.Unmarshal(data, &identity); err != nil {
			return nil, err
		}
		if identity.InstanceID != saved.InstanceID || identity.RequestID != saved.Public.RequestID || identity.AgentID == "" {
			return nil, domain.Fail(domain.Denied, "registration result does not match the retained signed request and Foundation instance")
		}
		saved.Identity, saved.Enrolled, saved.Stage = &identity, true, "registered"
		if err := save(); err != nil {
			return nil, err
		}
	}
	profile, err := api.Call[config.ConnectionProfile](ctx, r.client, "GET", "/local/profile", nil)
	if err != nil {
		return nil, err
	}
	profile.Identity = *saved.Identity
	profile.Identity.OwnerKey, profile.Identity.OwnerGeneration = "", 0
	options, err := saved.serviceOptions()
	if err != nil {
		return nil, err
	}
	profilePath, optionsPath := filepath.Join(root, "public-profile.json"), filepath.Join(root, "services.json")
	if saved.Stage == "registered" {
		profileScript, err := additionJSON(profilePath, profile)
		if err != nil {
			return nil, err
		}
		optionsScript, err := additionJSON(optionsPath, options)
		if err != nil {
			return nil, err
		}
		script := "set -eu\numask 077\n" + marker + profileScript + optionsScript +
			identity + " connect --profile " + additionQuote(profilePath) + " >&2\n" +
			binary("agent-identity") + " service install --input " + additionQuote(optionsPath) + " --apply >&2\n" +
			"i=0\nuntil " + cli + " --timeout 5 connect >&2; do i=$((i + 1)); test \"$i\" -lt 15; sleep 1; done\n" +
			cli + " doctor"
		data, err := native("activated", script, nil)
		if err != nil {
			return nil, err
		}
		var doctor struct {
			AgentID string `json:"agent_id"`
		}
		if err := additionData(data, &doctor); err != nil {
			return nil, err
		}
		if doctor.AgentID != saved.Identity.AgentID {
			return nil, domain.Fail(domain.Denied, "target's connected identity does not match the registered public request")
		}
		saved.Pending, saved.Stage = "", "activated"
		if err := save(); err != nil {
			return nil, err
		}
	}
	status, err := call("set -eu\n"+marker+binary("agent-identity")+" service status --input "+additionQuote(optionsPath), nil, false)
	if err != nil {
		return nil, err
	}
	var services platform.IdentityServiceStatus
	if err := additionData(status, &services); err != nil {
		return nil, err
	}
	if !services.Identity.Registered || !services.Identity.Running || !services.Worker.Registered || !services.Worker.Running {
		return nil, domain.Fail(domain.Prerequisite, "identity is attached but both native user services are not observed running; inspect the target, do not repeat installation")
	}
	agent, err := api.Call[domain.Agent](ctx, r.client, "GET", api.Path("agents", saved.Identity.AgentID), nil)
	if err != nil {
		return nil, err
	}
	if agent.ID != saved.Identity.AgentID || !agent.Active || agent.Founder || (agent.Mode != "ai" && agent.Mode != "manual") {
		return nil, domain.Fail(domain.Denied, "new worker's current identity or authoritative mode is invalid")
	}
	saved.Mode = agent.Mode
	saved.Connected, saved.Stage = true, "connected"
	if err := save(); err != nil {
		return nil, err
	}
	return saved, nil
}

func (s *agentAddition) setFacts(data []byte) error {
	fields := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(fields) != 6 || fields[4] != s.Target.User || !filepath.IsAbs(fields[5]) ||
		filepath.Clean(fields[5]) != fields[5] || fields[5] == "/" || strings.ContainsAny(fields[5], "\x00\r\t") {
		return domain.Fail(domain.Denied, "SSH target did not prove the expected existing user and canonical HOME")
	}
	uid, uidErr := strconv.Atoi(fields[2])
	gid, gidErr := strconv.Atoi(fields[3])
	if uidErr != nil || gidErr != nil || uid <= 0 || uid > 1<<31-1 || gid < 0 || gid > 1<<31-1 {
		return domain.Fail(domain.Denied, "SSH target must be an existing non-root account")
	}
	platforms := map[string]string{"Linux/x86_64": "linux-amd64", "Linux/aarch64": "linux-arm64",
		"Linux/arm64": "linux-arm64", "Darwin/arm64": "darwin-arm64"}
	platform := platforms[fields[0]+"/"+fields[1]]
	if platform == "" {
		return domain.Fail(domain.Prerequisite, "SSH target has no supported native Foundation package")
	}
	s.Home, s.UID, s.GID, s.Release.Platform = fields[5], uid, gid, platform
	return nil
}

func additionJSON(path string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	delimiter := "FDN_" + domain.Hash(string(data))
	return "set -C\ncat > " + additionQuote(path) + " <<'" + delimiter + "'\n" + string(data) + "\n" + delimiter + "\nset +C\n", nil
}

func additionData(data []byte, target any) error {
	var reply api.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(data), &reply); err != nil {
		return domain.Fail(domain.Unavailable, "target returned an invalid structured result; preserve the addition operation")
	}
	if !reply.OK || reply.Error != nil {
		if reply.Error != nil {
			return reply.Error
		}
		return domain.Fail(domain.Unavailable, "target did not acknowledge the addition step")
	}
	return json.Unmarshal(reply.Data, target)
}

func (s agentAddition) serviceOptions() (platform.IdentityServiceOptions, error) {
	root := filepath.Join(s.Home, ".foundation")
	osName := strings.SplitN(s.Release.Platform, "-", 2)[0]
	name, err := domain.AgentUsername(s.Target.Name)
	if err != nil {
		return platform.IdentityServiceOptions{}, err
	}
	options := platform.IdentityServiceOptions{OS: osName, Name: name, User: s.Target.User, UID: s.UID, GID: s.GID, Home: s.Home,
		IdentityBinary: filepath.Join(root, "bin", "agent-identity"), WorkerBinary: filepath.Join(root, "bin", "agent-service"),
		IdentityConfig: filepath.Join(root, "config", "identity.json"), WorkerConfig: filepath.Join(root, "config", "agent.json"),
		StateDir: filepath.Join(root, "identity"), WorkerStateDir: filepath.Join(root, "state"), SocketDir: filepath.Join(root, "run")}
	if osName == "darwin" {
		options.IdentityDefinition = filepath.Join(s.Home, "Library", "LaunchAgents", "org.foundation.identity."+name+".plist")
		options.WorkerDefinition = filepath.Join(s.Home, "Library", "LaunchAgents", "org.foundation.runner."+name+".plist")
	} else {
		options.IdentityDefinition = filepath.Join(s.Home, ".config", "systemd", "user", "fdn-identity-"+name+".service")
		options.WorkerDefinition = filepath.Join(s.Home, ".config", "systemd", "user", "fdn-worker-"+name+".service")
	}
	_, err = platform.RenderIdentityServices(options)
	return options, err
}

func additionArchive(ctx context.Context, catalog upgrade.Catalog, saved agentAddition) (*os.File, error) {
	directory, err := os.MkdirTemp(saved.Directory, "payload-")
	if err != nil {
		return nil, err
	}
	bundle := filepath.Join(directory, "release.zip")
	if err := catalog.Download(ctx, saved.Release, bundle); err != nil {
		return nil, err
	}
	unpacked := filepath.Join(directory, "verified")
	if _, err := upgrade.ExtractBundle(bundle, unpacked, saved.Release); err != nil {
		return nil, err
	}
	archive, err := os.Open(filepath.Join(unpacked, fmt.Sprintf("fdn-%s-%s.tar.gz", saved.Release.Version, saved.Release.Platform)))
	if err != nil {
		return nil, err
	}
	if err := validateAdditionArchive(archive); err != nil {
		archive.Close()
		return nil, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		archive.Close()
		return nil, err
	}
	return archive, nil
}

func validateAdditionArchive(source io.Reader) error {
	compressed, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(io.LimitReader(compressed, upgrade.MaxBundleBytes+1))
	seen := map[string]bool{}
	required := []string{"bin/agent-cli", "bin/agent-identity", "bin/agent-service", "bin/fdn-service", "guide/MANIFEST.json", "guide/AGENTS.md", "THIRD_PARTY_NOTICES"}
	guide := regexp.MustCompile(`^guide/([a-zA-Z0-9._-]+/)*[a-zA-Z0-9._-]+\.md$`)
	var total int64
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		valid := false
		for _, name := range required {
			valid = valid || entry.Name == name
		}
		valid = valid || guide.MatchString(entry.Name)
		total += entry.Size
		if !valid || filepath.Clean(entry.Name) != entry.Name || seen[entry.Name] || entry.Typeflag != tar.TypeReg ||
			entry.Size < 0 || total > upgrade.MaxBundleBytes || entry.Mode&07000 != 0 {
			return domain.Fail(domain.Denied, "native package contains an unsafe, duplicate or unbounded worker payload")
		}
		seen[entry.Name] = true
	}
	for _, name := range required {
		if !seen[name] {
			return domain.Fail(domain.Denied, "native package lacks a required binary or embedded guide")
		}
	}
	return nil
}
