package upgrade

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type Target struct {
	Role       string     `json:"role"`
	TargetID   string     `json:"target_id"`
	Platform   string     `json:"platform"`
	SSH        config.SSH `json:"ssh"`
	Helper     string     `json:"helper"`
	PolicyFile string     `json:"policy_file"`
	Sudo       bool       `json:"sudo"`
}

type Targets struct {
	Targets []Target `json:"targets"`
}

type PlanItem struct {
	Target    Target                     `json:"target"`
	Proposal  domain.UpgradeProposal     `json:"proposal"`
	Permit    *domain.UpgradePermit      `json:"permit,omitempty"`
	Result    *domain.UpgradeNativeReply `json:"result,omitempty"`
	MessageID string                     `json:"message_id,omitempty"`
}

type Plan struct {
	Version     int        `json:"version"`
	ID          string     `json:"id"`
	InstanceID  string     `json:"instance_id"`
	FounderID   string     `json:"founder_id"`
	State       string     `json:"state"`
	RecoveryKey string     `json:"recovery_key"`
	Items       []PlanItem `json:"items"`
}

var operatorHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:-]{0,252}$`)
var operatorUser = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,63}$`)

func ValidateTarget(t Target) error {
	if (t.Role != "foundation" && t.Role != "agent") || t.TargetID == "" || !operatorHost.MatchString(t.SSH.Host) ||
		!operatorUser.MatchString(t.SSH.User) || t.SSH.Port < 1 || t.SSH.Port > 65535 ||
		(t.Platform != "linux-amd64" && t.Platform != "linux-arm64" && t.Platform != "darwin-arm64") ||
		t.Role == "foundation" && !strings.HasPrefix(t.Platform, "linux-") {
		return domain.Fail(domain.Invalid, "upgrade target requires its exact role/identity/platform and independently authorized operator SSH account")
	}
	if t.SSH.LocalPort != 0 || t.SSH.RemotePort != 0 {
		return domain.Fail(domain.Invalid, "operator updater SSH uses only its explicit host/port, not API TCP forwarding fields")
	}
	expected := "agent-service"
	if t.Role == "foundation" {
		expected = "fdn-service"
	}
	if filepath.Base(t.Helper) != expected {
		return domain.Fail(domain.Invalid, "native updater must be the configured role's fixed service binary")
	}
	for _, path := range []string{t.Helper, t.PolicyFile, t.SSH.IdentityFile, t.SSH.KnownHostsFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n\t") {
			return domain.Fail(domain.Invalid, "updater and operator credential paths must be clean absolute single-line paths")
		}
	}
	return nil
}

func operatorQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func OperatorArguments(t Target) ([]string, error) {
	if err := ValidateTarget(t); err != nil {
		return nil, err
	}
	if _, err := secure.Read(t.SSH.IdentityFile, domain.MaxBody); err != nil {
		return nil, err
	}
	if _, err := secure.ReadPublic(t.SSH.KnownHostsFile, domain.MaxBody); err != nil {
		return nil, err
	}
	command := operatorQuote(t.Helper) + " upgrade-helper --policy " + operatorQuote(t.PolicyFile)
	if t.Sudo {
		command = "sudo -n -- " + command
	}
	knownHosts := t.SSH.KnownHostsFile
	if strings.ContainsAny(knownHosts, " \t\"\\") {
		knownHosts = "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(knownHosts) + "\""
	}
	return []string{"-F", "/dev/null", "-T", "-p", strconv.Itoa(t.SSH.Port), "-i", t.SSH.IdentityFile,
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no", "-o", "ForwardAgent=no",
		"-o", "ClearAllForwardings=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ProxyCommand=none", "-o", "ProxyJump=none", "-o", "RequestTTY=no",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"--", t.SSH.User + "@" + t.SSH.Host, command}, nil
}

func CallNative(ctx context.Context, runner process.Runner, target Target, request domain.UpgradeNativeRequest) (domain.UpgradeNativeReply, error) {
	var reply domain.UpgradeNativeReply
	args, err := OperatorArguments(target)
	if err != nil {
		return reply, err
	}
	payload, err := message.MarshalPayload(request)
	if err != nil {
		return reply, err
	}
	output, diagnostic := process.NewBuffer(domain.MaxBody), process.NewBuffer(8192)
	runErr := runner.Run(ctx, process.Spec{Path: "ssh", Args: args, Stdin: bytes.NewReader(payload),
		Env: process.Environment(), Stdout: output, Stderr: diagnostic})
	data, err := output.Bytes()
	if err != nil {
		return reply, err
	}
	if len(data) != 0 {
		if err := message.UnmarshalPayload(data, &reply); err != nil {
			return reply, err
		}
		if reply.Error != nil {
			return reply, reply.Error
		}
	}
	if runErr != nil || len(data) == 0 {
		return reply, domain.Fail(domain.Unavailable, "native upgrade response is uncertain; inspect the same plan with upgrade status, do not repeat installation")
	}
	return reply, nil
}
