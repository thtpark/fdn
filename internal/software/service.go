package software

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type serviceState struct {
	Loaded  bool   `json:"loaded"`
	Running bool   `json:"running"`
	Enabled bool   `json:"enabled"`
	PID     int    `json:"pid"`
	Config  string `json:"config_sha256,omitempty"`
}

func (n *Native) serviceFilename(policy Policy) string {
	if n.servicePath != nil {
		return n.servicePath(policy)
	}
	return policy.serviceFile(n.hostIdentity())
}

func (n *Native) nativeRequirements(_ context.Context, policy Policy) error {
	paths := []string{}
	if policy.Method == "debian" {
		paths = append(paths, "/usr/bin/dpkg", "/usr/bin/dpkg-query", "/usr/bin/dpkg-deb")
		if err := checkDPKGHooks(policy); err != nil {
			return err
		}
	}
	if policy.Service != "" {
		if n.hostIdentity().OS == "linux" {
			paths = append(paths, "/usr/bin/systemctl")
			if policy.Method == "user-service" {
				directory := "/run/user/" + strconv.Itoa(policy.UID)
				if err := protectedPath(directory, uint32(policy.UID), false); err != nil {
					return domain.Fail(domain.Prerequisite, "the installing user's systemd runtime directory is unavailable or unprotected")
				}
				info, err := os.Lstat(filepath.Join(directory, "bus"))
				if err != nil {
					return domain.Fail(domain.Prerequisite, "the installing user's systemd session bus is unavailable")
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Uid != uint32(policy.UID) || info.Mode()&os.ModeSocket == 0 {
					return domain.Fail(domain.Denied, "systemd-user requires the existing installing user's native session bus")
				}
			}
		} else {
			paths = append(paths, "/bin/launchctl")
		}
	}
	for _, filename := range paths {
		if err := protectedPath(filename, 0, false); err != nil {
			return domain.Fail(domain.Prerequisite, "the fixed native software manager is missing or unprotected")
		}
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return domain.Fail(domain.Prerequisite, "the fixed native software manager is not executable")
		}
	}
	return nil
}

func (n *Native) run(ctx context.Context, policy Policy, executable string, args []string, output io.Writer) error {
	if n.Runner == nil {
		return domain.Fail(domain.Prerequisite, "native software process runner is unavailable")
	}
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=" + policy.Home,
		"USER=" + policy.User, "LOGNAME=" + policy.User}
	if policy.Method == "debian" {
		env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root", "DEBIAN_FRONTEND=noninteractive"}
	} else if n.hostIdentity().OS == "linux" && policy.Service != "" {
		directory := "/run/user/" + strconv.Itoa(policy.UID)
		env = append(env, "XDG_RUNTIME_DIR="+directory, "DBUS_SESSION_BUS_ADDRESS=unix:path="+directory+"/bus")
	}
	diagnostic := process.NewBuffer(64 << 10)
	discarded := process.NewBuffer(64 << 10)
	if output == nil {
		output = discarded
	}
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runErr := n.Runner.Run(commandCtx, process.Spec{Path: executable, Args: args, Env: env,
		Dir: policy.StateDir, Stdout: output, Stderr: diagnostic})
	_, diagnosticErr := diagnostic.Bytes()
	_, discardedErr := discarded.Bytes()
	return errors.Join(runErr, diagnosticErr, discardedErr)
}

func (n *Native) readCommand(ctx context.Context, policy Policy, executable string, args []string) ([]byte, error) {
	output := process.NewBuffer(64 << 10)
	runErr := n.run(ctx, policy, executable, args, output)
	data, outputErr := output.Bytes()
	return data, errors.Join(runErr, outputErr)
}

func unitQuote(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "$", "$$")
	return strconv.Quote(value)
}

func serviceDefinition(policy Policy, host hostIdentity, installation string) []byte {
	executable := filepath.Join(installation, filepath.FromSlash(policy.Executable))
	if host.OS == "linux" {
		var data strings.Builder
		fmt.Fprintf(&data, "[Unit]\nDescription=Foundation managed software %s\n\n[Service]\nType=simple\n", policy.ProductID)
		if policy.Method == "debian" {
			fmt.Fprintf(&data, "User=%d\n", policy.UID)
		}
		fmt.Fprintf(&data, "ExecStart=%s", unitQuote(executable))
		for _, arg := range policy.Args {
			fmt.Fprintf(&data, " %s", unitQuote(arg))
		}
		fmt.Fprintf(&data, "\nWorkingDirectory=%s\nEnvironment=%s\nNoNewPrivileges=yes\n\n[Install]\nWantedBy=",
			unitQuote(installation), unitQuote("HOME="+policy.Home))
		if policy.Method == "debian" {
			data.WriteString("multi-user.target\n")
		} else {
			data.WriteString("default.target\n")
		}
		return []byte(data.String())
	}
	var data bytes.Buffer
	data.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>")
	element := func(key, value string) {
		data.WriteString("<key>")
		_ = xml.EscapeText(&data, []byte(key))
		data.WriteString("</key><string>")
		_ = xml.EscapeText(&data, []byte(value))
		data.WriteString("</string>")
	}
	element("Label", policy.Service)
	element("Program", executable)
	data.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range append([]string{executable}, policy.Args...) {
		data.WriteString("<string>")
		_ = xml.EscapeText(&data, []byte(arg))
		data.WriteString("</string>")
	}
	data.WriteString("</array>")
	element("WorkingDirectory", installation)
	data.WriteString("<key>EnvironmentVariables</key><dict>")
	element("HOME", policy.Home)
	data.WriteString("</dict><key>RunAtLoad</key><true/><key>KeepAlive</key><false/></dict></plist>\n")
	return data.Bytes()
}

func (n *Native) readService(ctx context.Context, policy Policy, installation string, absent bool) (serviceState, error) {
	if policy.Service == "" {
		return serviceState{}, nil
	}
	filename := n.serviceFilename(policy)
	info, statErr := os.Lstat(filename)
	present := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return serviceState{}, statErr
	}
	var configuration string
	if present {
		if absent || !info.Mode().IsRegular() {
			return serviceState{}, domain.Fail(domain.Conflict, "a first software installation cannot adopt an existing service definition")
		}
		if err := protectedPath(filename, uint32(os.Geteuid()), false); err != nil {
			return serviceState{}, err
		}
		data, err := secure.ReadPublic(filename, 64<<10)
		if err != nil {
			return serviceState{}, err
		}
		if !bytes.Equal(data, serviceDefinition(policy, n.hostIdentity(), installation)) {
			return serviceState{}, domain.Fail(domain.Conflict, "native software service differs from its fixed provisioned executable and arguments")
		}
		configuration = domain.Hash(string(data))
	} else if !absent {
		return serviceState{}, domain.Fail(domain.Conflict, "managed software service definition is missing")
	}
	if n.hostIdentity().OS == "linux" {
		args := []string{"show", policy.Service, "--property=LoadState,ActiveState,MainPID,FragmentPath,DropInPaths,UnitFileState"}
		if policy.Method == "user-service" {
			args = append([]string{"--user"}, args...)
		}
		data, err := n.readCommand(ctx, policy, "/usr/bin/systemctl", args)
		if err != nil {
			return serviceState{}, domain.Fail(domain.Prerequisite, "the fixed software service manager cannot provide a complete observation")
		}
		fields := make(map[string]string)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				return serviceState{}, domain.Fail(domain.Unavailable, "native software service observation is malformed")
			}
			if _, duplicate := fields[key]; duplicate {
				return serviceState{}, domain.Fail(domain.Unavailable, "native software service observation repeats a field")
			}
			fields[key] = value
		}
		for _, key := range []string{"LoadState", "ActiveState", "MainPID", "FragmentPath", "DropInPaths", "UnitFileState"} {
			if _, exists := fields[key]; !exists {
				return serviceState{}, domain.Fail(domain.Unavailable, "native software service observation is incomplete")
			}
		}
		pid, err := strconv.Atoi(fields["MainPID"])
		if err != nil || pid < 0 || fields["DropInPaths"] != "" {
			return serviceState{}, domain.Fail(domain.Denied, "native software service has unknown ownership or unmanaged overrides")
		}
		if fields["LoadState"] == "not-found" && fields["ActiveState"] == "inactive" && pid == 0 &&
			fields["FragmentPath"] == "" && !present {
			return serviceState{}, nil
		}
		if absent || !present || fields["LoadState"] != "loaded" || fields["FragmentPath"] != filename ||
			(fields["ActiveState"] != "active" && fields["ActiveState"] != "inactive") ||
			fields["ActiveState"] == "active" && pid == 0 || fields["ActiveState"] == "inactive" && pid != 0 ||
			(fields["UnitFileState"] != "enabled" && fields["UnitFileState"] != "disabled") {
			return serviceState{}, domain.Fail(domain.Conflict, "native software service is not the exact stable policy-managed unit")
		}
		return serviceState{Loaded: true, Running: pid > 0, Enabled: fields["UnitFileState"] == "enabled", PID: pid, Config: configuration}, nil
	}
	data, err := n.readCommand(ctx, policy, "/bin/launchctl", []string{"list"})
	if err != nil {
		return serviceState{}, domain.Fail(domain.Prerequisite, "the installing user's launchd domain is unavailable")
	}
	state := serviceState{Config: configuration}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "PID Status Label" {
		return state, domain.Fail(domain.Unavailable, "native launchd list is malformed")
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return state, domain.Fail(domain.Unavailable, "native launchd list is incomplete")
		}
		if fields[2] != policy.Service {
			continue
		}
		if state.Loaded || absent || !present {
			return state, domain.Fail(domain.Conflict, "software launchd service is duplicate or not explicitly managed")
		}
		state.Loaded, state.Enabled = true, true
		if fields[0] != "-" {
			pid, err := strconv.Atoi(fields[0])
			if err != nil || pid <= 0 {
				return state, domain.Fail(domain.Unavailable, "software launchd PID is malformed")
			}
			state.PID, state.Running = pid, true
		}
	}
	if state.Loaded {
		scope := "gui/" + strconv.Itoa(policy.UID) + "/" + policy.Service
		printed, err := n.readCommand(ctx, policy, "/bin/launchctl", []string{"print", scope})
		if err != nil {
			return state, domain.Fail(domain.Unavailable, "software launchd job cannot be independently inspected")
		}
		fields := make(map[string]string)
		for _, line := range strings.Split(string(printed), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
			if ok && (key == "program" || key == "path" || key == "pid") {
				if _, exists := fields[key]; exists {
					return state, domain.Fail(domain.Unavailable, "software launchd ownership observation is ambiguous")
				}
				fields[key] = value
			}
		}
		if fields["path"] != filename || fields["program"] != filepath.Join(installation, filepath.FromSlash(policy.Executable)) ||
			state.Running && fields["pid"] != strconv.Itoa(state.PID) {
			return state, domain.Fail(domain.Conflict, "launchd is not executing the exact policy-managed software job")
		}
	}
	return state, nil
}

func (n *Native) serviceEffect(ctx context.Context, policy Policy, operation string) error {
	var executable string
	var args []string
	if n.hostIdentity().OS == "linux" {
		executable, args = "/usr/bin/systemctl", []string{operation}
		if operation != "daemon-reload" {
			args = append(args, policy.Service)
		}
		if policy.Method == "user-service" {
			args = append([]string{"--user"}, args...)
		}
	} else {
		executable = "/bin/launchctl"
		scope := "gui/" + strconv.Itoa(policy.UID)
		switch operation {
		case "stop":
			args = []string{"bootout", scope + "/" + policy.Service}
		case "start":
			args = []string{"bootstrap", scope, n.serviceFilename(policy)}
		default:
			return domain.Fail(domain.Invalid, "unsupported fixed launchd lifecycle action")
		}
	}
	if err := n.run(ctx, policy, executable, args, nil); err != nil {
		return domain.Fail(domain.Unavailable, "native software lifecycle effect is uncertain; inspect retained proof before operator recovery")
	}
	return nil
}

func (n *Native) writeService(policy Policy, id string, previousRoot string) (bool, error) {
	if policy.Service == "" {
		return false, nil
	}
	filename := n.serviceFilename(policy)
	parent := filepath.Dir(filename)
	if err := protectedPath(parent, uint32(os.Geteuid()), true); err != nil {
		return false, err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return false, err
	}
	if err := protectedPath(parent, uint32(os.Geteuid()), false); err != nil {
		return false, err
	}
	data := serviceDefinition(policy, n.hostIdentity(), filepath.Join(policy.Prefix, "current"))
	if _, err := os.Lstat(filename); err == nil {
		old, err := secure.ReadPublic(filename, 64<<10)
		if err != nil {
			return false, err
		}
		if bytes.Equal(old, data) {
			return false, nil
		}
		if previousRoot == "" || !bytes.Equal(old, serviceDefinition(policy, n.hostIdentity(), previousRoot)) {
			return false, domain.Fail(domain.Conflict, "refusing to overwrite an unrecognized native service definition")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	next := filename + ".software-" + id
	file, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return false, err
	}
	_, writeErr := file.Write(data)
	syncErr, closeErr := file.Sync(), file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return false, err
	}
	if err := os.Rename(next, filename); err != nil {
		return false, err
	}
	return true, secure.SyncDir(parent)
}
