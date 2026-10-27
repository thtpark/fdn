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
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Server struct {
	StateDir      string `json:"state_dir"`
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
	VNCVerified              bool              `json:"vnc_verified,omitempty"`
	Screens                  []Screen          `json:"screens,omitempty"`
	SSH                      *SSH              `json:"ssh,omitempty"`
	sourcePath               string
}

type Screen struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Scope        string              `json:"scope"`
	Width        int                 `json:"width"`
	Height       int                 `json:"height"`
	X            int                 `json:"x,omitempty"`
	Y            int                 `json:"y,omitempty"`
	Primary      bool                `json:"primary"`
	VNCAddress   string              `json:"vnc_address,omitempty"`
	VNCVerified  bool                `json:"vnc_verified"`
	Display      string              `json:"display"`
	XAuthority   string              `json:"xauthority,omitempty"`
	Applications []ScreenApplication `json:"applications,omitempty"`
}

type ScreenApplication struct {
	ID         string   `json:"id"`
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments,omitempty"`
}

type Credential struct {
	AgentID string `json:"agent_id"`
}

const UserDirectory = ".foundation"

func DefaultWorker(stateDir string) Worker {
	return Worker{StateDir: stateDir, URL: "https://localhost:8443",
		CAFile: filepath.Join(stateDir, "ca.pem"), CredentialFile: filepath.Join(stateDir, "credential.json"),
		Mode: "ai", Provider: "codex", ProviderPaths: map[string]string{}, AutomaticProviderUpdates: true,
		WorkspaceRoot: filepath.Join(filepath.Dir(stateDir), "workspaces"),
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
	if !filepath.IsAbs(c.IngressSocket) || !filepath.IsAbs(c.JoinSocket) || c.IngressSocket == c.JoinSocket || c.IngressGroup < -1 || (c.AccountSocket != "" && !filepath.IsAbs(c.AccountSocket)) {
		return domain.Fail(domain.Invalid, "Foundation requires distinct absolute Unix ingress sockets")
	}
	return nil
}

func (c Worker) Validate() error {
	return c.ValidateForOwner(uint32(os.Geteuid()))
}

// ValidateForOwner lets a native inspector check an already-bound runner UID.
// Worker execution must use Validate, which always requires its actual EUID.
func (c Worker) ValidateForOwner(ownerUID uint32) error {
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
	if err := c.validateScreensForOwner(ownerUID); err != nil {
		return err
	}
	return URL(c.URL)
}

func ValidScreenID(id string) bool { return domain.ValidScreenID(id) }

// LocalDisplay accepts only existing local X11 displays or explicit macOS display IDs.
func LocalDisplay(display string) bool {
	if strings.HasPrefix(display, "macos:") {
		id, err := strconv.ParseUint(strings.TrimPrefix(display, "macos:"), 10, 32)
		return err == nil && id != 0 && display == "macos:"+strconv.FormatUint(id, 10)
	}
	if !strings.HasPrefix(display, ":") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(display, ":"), ".")
	if len(parts) > 2 {
		return false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil || part != strconv.FormatUint(n, 10) || (i == 1 && n > 255) {
			return false
		}
	}
	return true
}

func (c Worker) ValidateScreens() error {
	return c.validateScreensForOwner(uint32(os.Geteuid()))
}

func (c Worker) validateScreensForOwner(ownerUID uint32) error {
	if len(c.Screens) > 16 {
		return domain.Fail(domain.Invalid, "at most 16 local screen profiles are supported")
	}
	ids, endpoints := map[string]bool{}, map[string]bool{}
	primary := false
	if c.VNCAddress != "" || c.VNCVerified {
		return domain.Fail(domain.Invalid, "configure explicit screen profiles; per-worker VNC options are not supported")
	}
	for _, screen := range c.Screens {
		if !ValidScreenID(screen.ID) || ids[screen.ID] {
			return domain.Fail(domain.Invalid, "screen IDs must be unique safe identifiers of at most 64 bytes")
		}
		ids[screen.ID] = true
		if err := domain.CheckText(screen.Name, "screen name", 128, true); err != nil {
			return err
		}
		if screen.Name != strings.TrimSpace(screen.Name) || strings.IndexFunc(screen.Name, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && !strings.ContainsRune(" ._()-", r)
		}) >= 0 || (screen.Scope != "screen" && screen.Scope != "desktop") ||
			screen.Width < 1 || screen.Width > 32768 || screen.Height < 1 || screen.Height > 32768 ||
			screen.X < -131072 || screen.X > 131072 || screen.Y < -131072 || screen.Y > 131072 || !LocalDisplay(screen.Display) {
			return domain.Fail(domain.Invalid, "screen profiles need a safe name, screen/desktop scope, bounded geometry and an explicit local display")
		}
		if screen.Scope == "desktop" && (screen.X != 0 || screen.Y != 0) {
			return domain.Fail(domain.Invalid, "whole-desktop fallback geometry starts at the desktop origin")
		}
		if primary && screen.Primary {
			return domain.Fail(domain.Invalid, "at most one local screen profile may be primary")
		}
		primary = primary || screen.Primary
		if screen.VNCVerified && screen.VNCAddress == "" {
			return domain.Fail(domain.Invalid, "a verified screen backend needs a loopback VNC address")
		}
		if screen.VNCAddress != "" {
			if err := Loopback(screen.VNCAddress); err != nil {
				return err
			}
			_, port, _ := net.SplitHostPort(screen.VNCAddress)
			number, _ := strconv.Atoi(port)
			key := strconv.Itoa(number)
			if number == 0 || endpoints[key] {
				return domain.Fail(domain.Invalid, "screen VNC endpoints must use distinct nonzero loopback ports, not a reused whole-desktop backend")
			}
			endpoints[key] = true
		}
		if screen.XAuthority != "" {
			if strings.HasPrefix(screen.Display, "macos:") || !filepath.IsAbs(screen.XAuthority) || strings.ContainsRune(screen.XAuthority, 0) {
				return domain.Fail(domain.Invalid, "Xauthority must explicitly name an absolute private X11 file")
			}
			if err := checkXAuthorityForOwner(screen.XAuthority, ownerUID); err != nil {
				return err
			}
		}
		if len(screen.Applications) > 32 {
			return domain.Fail(domain.Invalid, "at most 32 applications per screen are supported")
		}
		apps := map[string]bool{}
		for _, app := range screen.Applications {
			if !ValidScreenID(app.ID) || apps[app.ID] || !filepath.IsAbs(app.Executable) || strings.ContainsRune(app.Executable, 0) || len(app.Executable) > 4096 || len(app.Arguments) > 64 {
				return domain.Fail(domain.Invalid, "screen applications need unique safe IDs, absolute executables and bounded argument arrays")
			}
			apps[app.ID] = true
			size := 0
			for _, arg := range app.Arguments {
				if err := domain.CheckText(arg, "application argument", 4096, false); err != nil {
					return err
				}
				size += len(arg)
			}
			if size > 32768 {
				return domain.Fail(domain.Invalid, "application argument array is too large")
			}
		}
	}
	return nil
}

// CheckXAuthority checks metadata for the actual EUID, without reading cookies.
// O_NONBLOCK prevents a substituted pipe or device from blocking validation.
func CheckXAuthority(path string) error {
	return checkXAuthorityForOwner(path, uint32(os.Geteuid()))
}

func checkXAuthorityForOwner(path string, ownerUID uint32) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return domain.Fail(domain.Denied, "Xauthority must explicitly name an absolute runner-owned private file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return domain.Fail(domain.Denied, "Xauthority must be an existing private regular file owned by the runner UID")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.Fail(domain.Denied, "cannot inspect private Xauthority metadata")
	}
	return checkXAuthorityMetadata(info, ownerUID)
}

func checkXAuthorityMetadata(info os.FileInfo, ownerUID uint32) error {
	if info == nil {
		return domain.Fail(domain.Denied, "Xauthority metadata is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 || stat.Uid != ownerUID {
		return domain.Fail(domain.Denied, "Xauthority must be a bounded private regular file owned by the runner UID")
	}
	return nil
}

// SourcePath stays local and is not part of the worker configuration JSON.
func (c Worker) SourcePath() string { return c.sourcePath }

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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, err
	}
	c.sourcePath = absolute
	return c, c.Validate()
}

func LoadServer(path string) (Server, error) {
	var c Server
	if err := Read(path, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, UserDirectory), nil
}

func DefaultConfig() (string, error) {
	return defaultConfig("agent.json")
}

func DefaultServerConfig() (string, error) {
	return defaultConfig("server.json")
}

func defaultConfig(name string) (string, error) {
	root, err := DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config", name), nil
}
