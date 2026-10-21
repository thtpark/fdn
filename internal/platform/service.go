package platform

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type Options struct {
	OS           string   `json:"os"`
	Role         string   `json:"role"`
	Binary       string   `json:"binary"`
	ConfigFile   string   `json:"config_file"`
	StateDir     string   `json:"state_dir"`
	Home         string   `json:"home"`
	User         string   `json:"user"`
	IngressGroup string   `json:"ingress_group,omitempty"`
	IngressDirs  []string `json:"ingress_dirs,omitempty"`
	Definition   string   `json:"definition"`
}

type Definition struct {
	Path     string     `json:"path"`
	Content  string     `json:"content"`
	Commands [][]string `json:"commands"`
}

var username = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func Render(o Options) (Definition, error) {
	return renderService(o, serviceAccountUID)
}

func renderService(o Options, lookup serviceUIDLookup) (Definition, error) {
	if o.Role != "agent" && o.Role != "foundation" {
		return Definition{}, domain.Fail(domain.Invalid, "service role must be agent or foundation")
	}
	for _, path := range []string{o.Binary, o.ConfigFile, o.StateDir, o.Home, o.Definition} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\n\r\x00") {
			return Definition{}, domain.Fail(domain.Invalid, "service paths must be absolute and single-line")
		}
	}
	if !username.MatchString(o.User) {
		return Definition{}, domain.Fail(domain.Invalid, "invalid dedicated service account")
	}
	if o.Role == "foundation" {
		if o.User == "root" {
			return Definition{}, domain.Fail(domain.Denied, "Foundation API must use a dedicated unprivileged service account")
		}
		uid, found, err := lookup(o.User)
		if err != nil {
			return Definition{}, err
		}
		if found && uid == 0 {
			return Definition{}, domain.Fail(domain.Denied, "Foundation API cannot use a root-UID account alias")
		}
	}
	if o.IngressGroup != "" && (o.Role != "foundation" || o.OS != "linux" || !username.MatchString(o.IngressGroup) || o.IngressGroup == "root" || o.IngressGroup == o.User) {
		return Definition{}, domain.Fail(domain.Invalid, "ingress group must be a distinct Foundation-only Linux supplementary group")
	}
	if len(o.IngressDirs) > 2 || len(o.IngressDirs) > 0 && (o.Role != "foundation" || o.OS != "linux" || o.IngressGroup == "") || o.IngressGroup != "" && len(o.IngressDirs) == 0 {
		return Definition{}, domain.Fail(domain.Invalid, "Foundation ingress needs its explicit supplementary group and one or two writable socket directories")
	}
	seenIngress := map[string]bool{}
	for _, directory := range o.IngressDirs {
		if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || strings.ContainsAny(directory, "\x00\r\n%") || seenIngress[directory] {
			return Definition{}, domain.Fail(domain.Invalid, "ingress directories must be distinct canonical native paths")
		}
		seenIngress[directory] = true
	}
	if o.OS == "linux" {
		content := "[Unit]\nDescription=Foundation " + o.Role + "\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n"
		if o.Role == "foundation" {
			content += "User=" + o.User + "\nGroup=" + o.User + "\n"
			if o.IngressGroup != "" {
				content += "SupplementaryGroups=" + o.IngressGroup + "\n"
			}
			content += "ProtectSystem=strict\nProtectHome=true\nReadWritePaths=" + strconv.Quote(o.StateDir)
			for _, directory := range o.IngressDirs {
				content += " " + strconv.Quote(directory)
			}
			content += "\n"
		}
		content += "ExecStart=" + strconv.Quote(o.Binary) + " --config " + strconv.Quote(o.ConfigFile) + " serve\n" +
			"Restart=on-failure\nRestartSec=5\nTimeoutStopSec=15\nKillMode=control-group\nUMask=0077\nNoNewPrivileges=true\n" +
			"Environment=" + strconv.Quote("PATH="+filepath.Join(o.Home, ".local", "bin")+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin") + "\n" +
			"WorkingDirectory=" + strings.ReplaceAll(o.StateDir, "%", "%%") + "\n\n[Install]\n"
		commands := [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "--now", filepath.Base(o.Definition)}}
		if o.Role == "agent" {
			content += "WantedBy=default.target\n"
			commands = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", filepath.Base(o.Definition)}}
		} else {
			content += "WantedBy=multi-user.target\n"
		}
		return Definition{Path: o.Definition, Content: content, Commands: commands}, nil
	}
	if o.OS == "darwin" && o.Role == "agent" {
		escape := func(value string) string {
			var buf bytes.Buffer
			_ = xml.EscapeText(&buf, []byte(value))
			return buf.String()
		}
		content := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>org.foundation.agent</string>
<key>ProgramArguments</key><array><string>` + escape(o.Binary) + `</string><string>--config</string><string>` + escape(o.ConfigFile) + `</string><string>serve</string></array>
<key>WorkingDirectory</key><string>` + escape(o.StateDir) + `</string>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>` + escape(o.Home) + `</string><key>PATH</key><string>` + escape(filepath.Join(o.Home, ".local", "bin")+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin") + `</string></dict>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>` + escape(filepath.Join(o.StateDir, "service.log")) + `</string>
<key>StandardErrorPath</key><string>` + escape(filepath.Join(o.StateDir, "service-error.log")) + `</string>
</dict></plist>
`
		return Definition{Path: o.Definition, Content: content, Commands: [][]string{{"launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Geteuid()), o.Definition}}}, nil
	}
	return Definition{}, domain.Fail(domain.Prerequisite, "Foundation server targets Linux; agents support Linux and macOS")
}

func Install(ctx context.Context, o Options, runner process.Runner) (err error) {
	if o.OS != runtime.GOOS {
		return domain.Fail(domain.Prerequisite, "cannot install another OS's service manager")
	}
	if o.Role == "foundation" && os.Geteuid() != 0 {
		return domain.Fail(domain.Denied, "system service registration requires explicit OS administrator execution")
	}
	if o.Role == "foundation" && (o.IngressGroup == "" || len(o.IngressDirs) == 0) {
		return domain.Fail(domain.Prerequisite, "native Foundation installation requires its ingress-only supplementary group and writable socket directories prepared by the root account helper")
	}
	lookup := serviceAccountUID
	if o.Role == "foundation" && o.OS == "linux" {
		if runner == nil {
			return domain.Fail(domain.Prerequisite, "native Foundation registration requires an explicit OS runner")
		}
		system := &nativeAccountSystem{runner: runner}
		lookup = func(name string) (uint32, bool, error) {
			account, found, err := system.LookupUser(ctx, name)
			if err == nil && !found {
				err = domain.Fail(domain.Prerequisite, "prepare the dedicated unprivileged server account before registration")
			}
			return account.UID, found, err
		}
	}
	definition, err := renderService(o, lookup)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(definition.Path), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(definition.Path); err == nil {
		if !info.Mode().IsRegular() {
			return domain.Fail(domain.Denied, "refusing to replace a nonregular service definition")
		}
		if err := secure.CheckOwner(info); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(definition.Path), ".fdn-service-")
	if err != nil {
		return err
	}
	defer func() {
		if e := os.Remove(temp.Name()); e != nil && !os.IsNotExist(e) {
			err = errors.Join(err, e)
		}
	}()
	_, writeErr := temp.WriteString(definition.Content)
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), definition.Path); err != nil {
		return err
	}
	if err := secure.SyncDir(filepath.Dir(definition.Path)); err != nil {
		return err
	}
	for _, command := range definition.Commands {
		output := process.NewBuffer(8192)
		environment := process.Environment()
		if o.OS == "linux" && o.Role == "agent" {
			for _, name := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
				if value := os.Getenv(name); value != "" {
					environment = append(environment, name+"="+value)
				}
			}
		}
		if err := runner.Run(ctx, process.Spec{Path: command[0], Args: command[1:], Env: environment, Stdout: output, Stderr: output}); err != nil {
			return fmt.Errorf("native service registration failed at %s: %w", command[0], err)
		}
		if _, err := output.Bytes(); err != nil {
			return err
		}
	}
	return nil
}
