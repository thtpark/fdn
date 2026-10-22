package display

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

type rfbFixture struct {
	address  string
	accepted atomic.Int64
	received atomic.Int64
}

func rfb(t *testing.T, banner string) *rfbFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &rfbFixture{address: listener.Addr().String()}
	var mu sync.Mutex
	var connections []net.Conn
	closing := false
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.accepted.Add(1)
			mu.Lock()
			if closing {
				mu.Unlock()
				conn.Close()
				return
			}
			connections = append(connections, conn)
			mu.Unlock()
			done.Add(1)
			go func() {
				defer done.Done()
				defer conn.Close()
				if _, err := io.WriteString(conn, banner); err != nil {
					return
				}
				buffer := make([]byte, 4096)
				for {
					n, err := conn.Read(buffer)
					fixture.received.Add(int64(n))
					if n != 0 {
						conn.Write(buffer[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		closing = true
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		done.Wait()
	})
	return fixture
}

const fixtureMonitors = "Screen 0: minimum 8 x 8, current 2560 x 720, maximum 32768 x 32768\nLEFT connected primary 1280x720+0+0 (normal)\nRIGHT connected 1280x720+1280+0 (normal)\nUNPLUGGED disconnected (normal)\n"

type screenFixture struct {
	config config.Worker
	home   string
	bin    string
	left   *rfbFixture
	right  *rfbFixture
}

func writeHelper(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

func screenFixtureFor(t *testing.T) *screenFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &screenFixture{home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), left: rfb(t, "RFB 003.008\n"), right: rfb(t, "RFB 003.008\n")}
	for _, path := range []string{fixture.home, fixture.bin} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", fixture.home)
	t.Setenv("PATH", fixture.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", "borrowed-account.invalid:99")
	t.Setenv("XAUTHORITY", filepath.Join(root, "must-not-read"))
	t.Setenv("SSH_AUTH_SOCK", "must-not-copy")
	t.Setenv("CODEX_HOME", "must-not-copy")
	t.Setenv("FDN_FAKE_SECRET", "must-not-copy")
	fixture.config = config.DefaultWorker(filepath.Join(root, "state"))
	writeHelper(t, filepath.Join(fixture.bin, "xrandr"), "printf '%s\\n' \"$@\" >> xrandr.calls\ncat <<'EOF'\n"+fixtureMonitors+"EOF\n")
	writeHelper(t, filepath.Join(fixture.bin, "xdotool"), `printf '%s\n' "$@" >> placement.calls
case "$1" in
search)
  for n in 1 2 3 4 5 6 7 8 9 10; do [ -s app.pid ] && break; /bin/sleep 0.01; done
  printf '101\n';;
windowsize) printf 'X=%s\nY=%s\nWIDTH=%s\nHEIGHT=%s\n' "${10}" "${11}" "$4" "$5";;
*) exit 90;;
esac
`)
	app := filepath.Join(fixture.bin, "browser")
	writeHelper(t, app, `printf '%s\n' "$@" > app.args
/usr/bin/env > app.env
/usr/bin/id -u > app.uid
printf '%s\n' "$$" > app.pid
printf 'launch\n' >> app.launches
exec /bin/sleep 60
`)
	fixture.config.Screens = []config.Screen{
		{ID: "left", Name: "Left screen", Scope: "screen", Width: 1280, Height: 720, Primary: true, Display: ":41.0", VNCAddress: fixture.left.address, VNCVerified: true,
			Applications: []config.ScreenApplication{{ID: "left-only", Executable: app}}},
		{ID: "right", Name: "Right screen", Scope: "screen", Width: 1280, Height: 720, X: 1280, Display: ":41.0", VNCAddress: fixture.right.address, VNCVerified: true,
			Applications: []config.ScreenApplication{{ID: "browser", Executable: app, Arguments: []string{"--new-window", "two words", "$(touch not-created)", "; touch not-created"}}}},
	}
	for _, name := range []string{"Xorg", "Xvfb", "Xtigervnc", "x11vnc"} {
		writeHelper(t, filepath.Join(fixture.bin, name), "printf 'forbidden\\n' > display-created\nexit 92\n")
	}
	return fixture
}

func validSession(screen string) domain.Session {
	return domain.Session{ID: "session_fixture", Mode: "vnc", ScreenID: screen, State: "consented", OpenRequested: true, ExpiresAt: time.Now().Unix() + 60}
}

func TestInventoryRequiresActualDisplayAndRFBWithoutCreatingAnything(t *testing.T) {
	f := screenFixtureFor(t)
	c := config.DefaultWorker(f.config.StateDir)
	got, err := Inventory(context.Background(), c)
	if err != nil || len(got) != 0 {
		t.Fatal("headless worker fabricated a display", got, err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "xrandr.calls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("headless inventory invoked a native display tool", err)
	}
	c.Screens = slices.Clone(f.config.Screens)
	for i := range c.Screens {
		c.Screens[i].VNCVerified = false
	}
	got, err = Inventory(context.Background(), c)
	if err != nil || len(got) != 0 || f.left.accepted.Load() != 0 || f.right.accepted.Load() != 0 {
		t.Fatal("unverified configured booleans became available screens", got, err)
	}
	writeHelper(t, filepath.Join(f.bin, "xrandr"), "printf 'Screen 0: minimum 8 x 8, current 2560 x 720, maximum 32768 x 32768\\nRIGHT disconnected\\n'\n")
	got, err = Inventory(context.Background(), f.config)
	if err != nil || len(got) != 0 {
		t.Fatal("RFB banner alone fabricated a display", got, err)
	}
	if err := Launch(context.Background(), f.config, "right", "browser"); err == nil {
		t.Fatal("headless launch accepted")
	}
	for _, name := range []string{"display-created", "app.pid"} {
		if _, err := os.Stat(filepath.Join(f.home, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("headless probe launched a display/application", name, err)
		}
	}
	if f.left.received.Load() != 0 || f.right.received.Load() != 0 {
		t.Fatal("inventory sent authentication or UI requests to RFB")
	}
}

func TestInventoryReportsOnlyVerifiedGeometryAndPublicMetadata(t *testing.T) {
	f := screenFixtureFor(t)
	got, err := Inventory(context.Background(), f.config)
	if err != nil || len(got) != 2 || got[1].ID != "right" || got[1].Width != 1280 || got[1].Primary || !got[0].Primary ||
		!slices.Equal(got[1].Applications, []string{"browser"}) {
		t.Fatal(got, err)
	}
	data, err := json.Marshal(got)
	if err != nil || strings.Contains(string(data), f.home) || strings.Contains(string(data), f.right.address) || strings.Contains(string(data), f.bin) || strings.Contains(string(data), ":41") {
		t.Fatal("private backend configuration escaped inventory", err)
	}
	f.config.Screens[1].X = 900
	got, err = Inventory(context.Background(), f.config)
	if err == nil || len(got) != 1 || got[0].ID != "left" {
		t.Fatal("mismatched monitor geometry was published", got, err)
	}
	if f.left.received.Load() != 0 || f.right.received.Load() != 0 {
		t.Fatal("inventory captured UI or attempted authentication")
	}
}

func TestInventoryRejectsNonRFBAndUnavailableBackends(t *testing.T) {
	f := screenFixtureFor(t)
	bad := rfb(t, "HTTP/1.1 200")
	f.config.Screens[0].VNCAddress = bad.address
	f.config.Screens = f.config.Screens[:1]
	if got, err := Inventory(context.Background(), f.config); err != nil || len(got) != 0 {
		t.Fatal("non-RFB endpoint became a screen", got, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.config.Screens[0].VNCAddress = listener.Addr().String()
	listener.Close()
	if got, err := Inventory(context.Background(), f.config); err != nil || len(got) != 0 {
		t.Fatal("absent backend became a screen", got, err)
	}
	for _, address := range []string{"0.0.0.0:5900", "other.invalid:5900", "192.0.2.1:5900"} {
		f.config.Screens[0].VNCAddress = address
		if _, err := Inventory(context.Background(), f.config); err == nil {
			t.Fatal("nonloopback backend accepted", address)
		}
	}
}

func TestOpenBindsChosenScreenApplicationAndFallbackExactly(t *testing.T) {
	f := screenFixtureFor(t)
	for _, session := range []domain.Session{
		validSession(""), validSession("unknown"),
		func() domain.Session { s := validSession("right"); s.ApplicationID = "left-only"; return s }(),
		func() domain.Session { s := validSession("right"); s.DesktopFallback = true; return s }(),
		func() domain.Session { s := validSession("right"); s.OpenRequested = false; return s }(),
		func() domain.Session { s := validSession("right"); s.State = "requested"; return s }(),
		func() domain.Session { s := validSession("right"); s.ExpiresAt = time.Now().Unix() - 1; return s }(),
	} {
		if endpoint, err := Open(context.Background(), f.config, session); err == nil || endpoint != nil {
			t.Fatal("unbound or unauthorized screen session accepted", session, err)
		}
	}
	endpoint, err := Open(context.Background(), f.config, validSession("right"))
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	banner := make([]byte, 12)
	if _, err := io.ReadFull(endpoint, banner); err != nil || string(banner) != "RFB 003.008\n" {
		t.Fatal("probe consumed the viewer's RFB banner", string(banner), err)
	}
	if _, err := endpoint.Write([]byte("chosen")); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 6)
	if _, err := io.ReadFull(endpoint, payload); err != nil || string(payload) != "chosen" || f.left.received.Load() != 0 || f.right.received.Load() != 6 {
		t.Fatal("VNC routed to a different screen", string(payload), err)
	}
	fallback := rfb(t, "RFB 003.008\n")
	c := f.config
	c.Screens = []config.Screen{{ID: "desktop", Name: "Whole desktop", Scope: "desktop", Width: 2560, Height: 720, Display: ":41.0", VNCAddress: fallback.address, VNCVerified: true}}
	session := validSession("desktop")
	if _, err := Open(context.Background(), c, session); err == nil {
		t.Fatal("desktop capture was silently treated as screen isolation")
	}
	session.DesktopFallback = true
	desktop, err := Open(context.Background(), c, session)
	if err != nil {
		t.Fatal("explicitly labelled desktop fallback unavailable", err)
	}
	if err := desktop.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyDesktopNeedsOwnDisplayEvidenceAndExplicitScope(t *testing.T) {
	f := screenFixtureFor(t)
	c := config.DefaultWorker(f.config.StateDir)
	c.VNCAddress, c.VNCVerified = f.left.address, true
	t.Setenv("DISPLAY", "")
	got, err := Inventory(context.Background(), c)
	if len(got) != 0 {
		t.Fatal("legacy VNC configuration alone fabricated a desktop", got, err)
	}
	t.Setenv("DISPLAY", ":41.0")
	t.Setenv("XAUTHORITY", "")
	got, err = Inventory(context.Background(), c)
	if err != nil || len(got) != 1 || got[0].ID != LegacyDesktopID || got[0].Scope != "desktop" || got[0].Width != 2560 || len(got[0].Applications) != 0 {
		t.Fatal(got, err)
	}
	session := validSession(LegacyDesktopID)
	if _, err := Open(context.Background(), c, session); err == nil {
		t.Fatal("legacy whole desktop opened without explicit fallback scope")
	}
	session.DesktopFallback = true
	endpoint, err := Open(context.Background(), c, session)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Close()
	session.ScreenID = ""
	if _, err := Open(context.Background(), c, session); err == nil {
		t.Fatal("blank legacy session inferred screen authorization")
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("owned fixture did not reach expected state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func applicationPID(t *testing.T, f *screenFixture) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, "app.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 {
		t.Fatal("invalid owned fixture PID", err)
	}
	return pid
}

func TestLaunchPreservesRunnerEnvironmentArgumentsAndReplay(t *testing.T) {
	f := screenFixtureFor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Launch(ctx, f.config, "right", "browser"); err != nil {
		t.Fatal(err)
	}
	pid := applicationPID(t, f)
	if err := Launch(ctx, f.config, "right", "browser"); err != nil {
		t.Fatal("identical live replay did not reuse owned application", err)
	}
	args, err := os.ReadFile(filepath.Join(f.home, "app.args"))
	if err != nil || string(args) != "--new-window\ntwo words\n$(touch not-created)\n; touch not-created\n" {
		t.Fatal("arguments were not passed literally", string(args), err)
	}
	env, err := os.ReadFile(filepath.Join(f.home, "app.env"))
	if err != nil || !strings.Contains(string(env), "HOME="+f.home+"\n") || !strings.Contains(string(env), "DISPLAY=:41.0\n") ||
		!strings.Contains(string(env), "XAUTHORITY=/dev/null\n") || strings.Contains(string(env), "must-not-copy") || strings.Contains(string(env), "must-not-read") {
		t.Fatal("application borrowed another account or sensitive environment", err)
	}
	uid, err := os.ReadFile(filepath.Join(f.home, "app.uid"))
	if err != nil || strings.TrimSpace(string(uid)) != strconv.Itoa(os.Geteuid()) {
		t.Fatal("application changed runner UID", err)
	}
	launches, err := os.ReadFile(filepath.Join(f.home, "app.launches"))
	if err != nil || string(launches) != "launch\n" {
		t.Fatal("replay duplicated the application", string(launches), err)
	}
	placement, err := os.ReadFile(filepath.Join(f.home, "placement.calls"))
	if err != nil || !strings.Contains(string(placement), "windowmove\n--sync\n--\n101\n1312\n32\n") {
		t.Fatal("selected screen placement was not requested", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "not-created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("application arguments were evaluated as shell commands", err)
	}
	cancel()
	waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
}

func TestOpenCloseAndCancellationStopOwnedApplication(t *testing.T) {
	for _, cancelSession := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelSession), func(t *testing.T) {
			f := screenFixtureFor(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session := validSession("right")
			session.ApplicationID = "browser"
			endpoint, err := Open(ctx, f.config, session)
			if err != nil {
				t.Fatal(err)
			}
			defer endpoint.Close()
			pid := applicationPID(t, f)
			if cancelSession {
				cancel()
			} else if err := endpoint.Close(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
			if err := endpoint.Close(); err != nil {
				t.Fatal("owned cleanup is not idempotent", err)
			}
		})
	}
}

func TestFailedPlacementCleansOnlyOwnedProcessAndProvesStartupFailure(t *testing.T) {
	f := screenFixtureFor(t)
	writeHelper(t, filepath.Join(f.bin, "xdotool"), `case "$1" in
search) for n in 1 2 3 4 5 6 7 8 9 10; do [ -s app.pid ] && break; /bin/sleep 0.01; done; printf '101\n';;
windowsize) printf 'X=0\nY=0\nWIDTH=1280\nHEIGHT=720\n';;
esac
`)
	session := validSession("right")
	session.ApplicationID = "browser"
	endpoint, err := Open(context.Background(), f.config, session)
	var proof interface{ CleanupProven() bool }
	if endpoint != nil || err == nil || !errors.As(err, &proof) || !proof.CleanupProven() {
		t.Fatal("safe failed placement retained no-process uncertainty", err)
	}
	pid := applicationPID(t, f)
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatal("failed screen preparation leaked the owned application")
	}
	applications.Lock()
	defer applications.Unlock()
	if applications.owned[filepath.Join(f.config.StateDir, "right")] != nil {
		t.Fatal("proven failed startup remained in the owned-process registry")
	}
}

func TestExecutableAndXAuthorityPermissionsAreCheckedBeforeLaunch(t *testing.T) {
	f := screenFixtureFor(t)
	path := f.config.Screens[1].Applications[0].Executable
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if err := Launch(context.Background(), f.config, "right", "browser"); err == nil {
		t.Fatal("other-writable executable launched")
	}
	if _, err := os.Stat(filepath.Join(f.home, "app.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("permission failure started an application", err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(f.home, "authority")
	if err := os.WriteFile(authority, []byte("synthetic cookie"), 0644); err != nil {
		t.Fatal(err)
	}
	f.config.Screens[1].XAuthority = authority
	if err := Launch(context.Background(), f.config, "right", "browser"); err == nil {
		t.Fatal("public Xauthority was read")
	}
	if err := os.Chmod(authority, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Launch(ctx, f.config, "right", "browser"); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(f.home, "app.env"))
	if err != nil || !strings.Contains(string(env), "XAUTHORITY="+authority+"\n") {
		t.Fatal("explicit runner-owned authority not selected", err)
	}
	pid := applicationPID(t, f)
	cancel()
	waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
}
