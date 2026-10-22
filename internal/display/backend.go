package display

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

func dialBackend(ctx context.Context, address string) (net.Conn, []byte, error) {
	if err := config.Loopback(address); err != nil {
		return nil, nil, err
	}
	host, port, _ := net.SplitHostPort(address)
	number, _ := strconv.Atoi(port)
	if number == 0 {
		return nil, nil, domain.Fail(domain.Invalid, "VNC needs a nonzero loopback port")
	}
	addresses := []string{address}
	if host == "localhost" {
		// Never let DNS or an altered hosts file widen a localhost-only backend.
		addresses = []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port)}
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var conn net.Conn
	var err error
	for _, target := range addresses {
		conn, err = (&net.Dialer{}).DialContext(probeCtx, "tcp", target)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, nil, domain.Fail(domain.Prerequisite, "verified loopback VNC backend is not responding")
	}
	peer, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !peer.IP.IsLoopback() {
		conn.Close()
		return nil, nil, domain.Fail(domain.Denied, "VNC backend did not resolve to loopback")
	}
	deadline, _ := probeCtx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		conn.Close()
		return nil, nil, err
	}
	banner := make([]byte, 12)
	_, err = io.ReadFull(conn, banner)
	if err != nil || (string(banner) != "RFB 003.003\n" && string(banner) != "RFB 003.007\n" && string(banner) != "RFB 003.008\n" && string(banner) != "RFB 003.889\n") {
		conn.Close()
		return nil, nil, domain.Fail(domain.Prerequisite, "loopback endpoint did not provide a supported RFB version banner")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, banner, nil
}

func environment(profile config.Screen) ([]string, string, error) {
	if os.Getuid() != os.Geteuid() {
		return nil, "", domain.Fail(domain.Denied, "display helpers must run as the runner UID without a privilege transition")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil, "", domain.Fail(domain.Prerequisite, "display helpers need the runner's absolute HOME")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || secure.CheckOwner(info) != nil {
		return nil, "", domain.Fail(domain.Denied, "display HOME must be a real directory owned by the runner and not writable by other accounts")
	}
	account, err := user.Current()
	if err != nil {
		return nil, "", domain.Fail(domain.Prerequisite, "cannot resolve the current runner account")
	}
	env := []string{"HOME=" + home, "USER=" + account.Username, "LOGNAME=" + account.Username, "PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
	if value := os.Getenv("TMPDIR"); value != "" {
		env = append(env, "TMPDIR="+value)
	}
	if !strings.HasPrefix(profile.Display, "macos:") && profile.Display != "" {
		if !config.LocalDisplay(profile.Display) {
			return nil, "", domain.Fail(domain.Denied, "only explicitly selected local X11 displays are supported")
		}
		authority := "/dev/null"
		if profile.XAuthority != "" {
			if !filepath.IsAbs(profile.XAuthority) {
				return nil, "", domain.Fail(domain.Denied, "Xauthority must explicitly name an absolute runner-owned private file")
			}
			if err := config.CheckXAuthority(profile.XAuthority); err != nil {
				return nil, "", err
			}
			authority = profile.XAuthority
		}
		env = append(env, "DISPLAY="+profile.Display, "XAUTHORITY="+authority)
	}
	return env, home, nil
}

func executable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return "", domain.Fail(domain.Prerequisite, "required native display helper is not installed")
		}
		path, err = filepath.Abs(resolved)
		if err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", domain.Fail(domain.Prerequisite, "configured application or native helper is not installed")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", domain.Fail(domain.Prerequisite, "configured application or native helper is not accessible")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return "", domain.Fail(domain.Denied, "display executables must be regular, executable, runner/root-owned and not writable by other accounts or set-ID")
	}
	return resolved, nil
}

func runTool(ctx context.Context, profile config.Screen, tool string, args ...string) ([]byte, error) {
	path, err := executable(tool)
	if err != nil {
		return nil, err
	}
	env, home, err := environment(profile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output := process.NewBuffer(64 << 10)
	err = (process.Native{}).Run(ctx, process.Spec{Path: path, Args: args, Env: env, Dir: home, Stdout: output, Stderr: io.Discard})
	if err != nil {
		var cleanup *process.CleanupError
		if errors.As(err, &cleanup) {
			return nil, errors.Join(domain.Fail(domain.Prerequisite, "native display helper cleanup is uncertain"), cleanup)
		}
		return nil, domain.Fail(domain.Prerequisite, "existing display or native helper is unavailable; no display was created")
	}
	return output.Bytes()
}
