package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Server struct {
	StateDir      string `json:"state_dir"`
	Listen        string `json:"listen,omitempty"`
	CertFile      string `json:"cert_file"`
	KeyFile       string `json:"key_file"`
	IngressSocket string `json:"ingress_socket,omitempty"`
	JoinSocket    string `json:"join_socket,omitempty"`
	IngressGroup  int    `json:"ingress_group,omitempty"`
	AccountSocket string `json:"account_socket,omitempty"`
}

type SSH struct {
	Host           string `json:"host"`
	User           string `json:"user"`
	Port           int    `json:"port"`
	IdentityFile   string `json:"identity_file"`
	KnownHostsFile string `json:"known_hosts_file"`
	LocalPort      int    `json:"local_port"`
	RemotePort     int    `json:"remote_port"`
}

type Worker struct {
	StateDir                 string            `json:"state_dir"`
	URL                      string            `json:"url"`
	CAFile                   string            `json:"ca_file"`
	ServerName               string            `json:"server_name,omitempty"`
	CredentialFile           string            `json:"credential_file"`
	IdentitySocket           string            `json:"identity_socket,omitempty"`
	Mode                     string            `json:"mode"`
	Provider                 string            `json:"provider"`
	Model                    string            `json:"model,omitempty"`
	ProviderPaths            map[string]string `json:"provider_paths"`
	AutomaticProviderUpdates bool              `json:"automatic_provider_updates"`
	WorkspaceRoot            string            `json:"workspace_root"`
	AllowWorkspaceTools      bool              `json:"allow_workspace_tools"`
	PollSeconds              int               `json:"poll_seconds"`
	InvocationTimeoutSeconds int               `json:"invocation_timeout_seconds"`
	ResearchIntervalHours    int               `json:"research_interval_hours"`
	VNCAddress               string            `json:"vnc_address,omitempty"`
	VNCVerified              bool              `json:"vnc_verified"`
	SSH                      *SSH              `json:"ssh,omitempty"`
}

type Credential struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token,omitempty"`
}

type InvitationBundle struct {
	Token       string `json:"token"`
	OperationID string `json:"operation_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

func DefaultWorker(stateDir string) Worker {
	return Worker{StateDir: stateDir, URL: "https://localhost:8443",
		CAFile: filepath.Join(stateDir, "ca.pem"), CredentialFile: filepath.Join(stateDir, "credential.json"),
		Mode: "ai", Provider: "codex", ProviderPaths: map[string]string{}, AutomaticProviderUpdates: true,
		WorkspaceRoot: filepath.Join(filepath.Dir(stateDir), "fdn-workspaces"),
		PollSeconds:   30, InvocationTimeoutSeconds: 3600, ResearchIntervalHours: 12}
}

func Loopback(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return domain.Fail(domain.Invalid, "listener needs a loopback host and port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return domain.Fail(domain.Invalid, "port must be in 0-65535")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return domain.Fail(domain.Denied, "listeners and tunneled endpoints must bind to loopback")
		}
	}
	return nil
}

func URL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return domain.Fail(domain.Invalid, "Foundation URL must be an HTTPS origin without credentials, query, or path")
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return domain.Fail(domain.Denied, "use a pinned SSH tunnel to reach a remote Foundation")
		}
	}
	return nil
}

func (c Server) Validate() error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.CertFile) || !filepath.IsAbs(c.KeyFile) {
		return domain.Fail(domain.Invalid, "server paths must be absolute")
	}
	if c.IngressSocket != "" || c.JoinSocket != "" {
		if c.Listen != "" || !filepath.IsAbs(c.IngressSocket) || !filepath.IsAbs(c.JoinSocket) || c.IngressSocket == c.JoinSocket || c.IngressGroup < -1 || (c.AccountSocket != "" && !filepath.IsAbs(c.AccountSocket)) {
			return domain.Fail(domain.Invalid, "secure Foundation uses distinct absolute Unix sockets, not a TCP listener")
		}
		return nil
	}
	return Loopback(c.Listen)
}

func (c Worker) Validate() error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.CredentialFile) || !filepath.IsAbs(c.CAFile) || !filepath.IsAbs(c.WorkspaceRoot) {
		return domain.Fail(domain.Invalid, "worker paths must be absolute")
	}
	if c.IdentitySocket != "" && !filepath.IsAbs(c.IdentitySocket) {
		return domain.Fail(domain.Invalid, "identity socket must be an absolute path")
	}
	if c.Mode != "ai" && c.Mode != "manual" {
		return domain.Fail(domain.Invalid, "mode must be ai or manual")
	}
	if !domain.ValidProvider(c.Provider) {
		return domain.Fail(domain.Invalid, "preferred provider must be codex or copilot")
	}
	if err := domain.CheckText(c.Model, "preferred model", 256, false); err != nil {
		return err
	}
	for name, path := range c.ProviderPaths {
		if !domain.ValidProvider(name) || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return domain.Fail(domain.Invalid, "provider paths must name supported adapters and absolute executables")
		}
	}
	if c.PollSeconds < 1 || c.PollSeconds > 3600 || c.InvocationTimeoutSeconds < 1 || c.InvocationTimeoutSeconds > 86400 ||
		!slices.Contains([]int{12, 24}, c.ResearchIntervalHours) {
		return domain.Fail(domain.Invalid, "invalid poll, execution timeout, or idle research interval (12 or 24 hours)")
	}
	if c.VNCAddress != "" {
		if err := Loopback(c.VNCAddress); err != nil {
			return err
		}
	}
	return URL(c.URL)
}

func Decode(data []byte, value any) error {
	if !utf8.Valid(data) {
		return domain.Fail(domain.Invalid, "input must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return domain.Fail(domain.Invalid, "invalid JSON or unknown fields")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return domain.Fail(domain.Invalid, "exactly one JSON value is required")
	}
	return nil
}

func Read(path string, value any) error {
	data, err := secure.Read(path, domain.MaxBody)
	if err != nil {
		return err
	}
	return Decode(data, value)
}

func Write(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return secure.Write(path, append(data, '\n'))
}

func LoadWorker(path string) (Worker, error) {
	var c Worker
	if err := Read(path, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func LoadServer(path string) (Server, error) {
	var c Server
	if err := Read(path, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func DefaultConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "fdn", "agent.json"), nil
}
