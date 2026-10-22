package display

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
)

func TestX11PlacementReservesNativeDecorationsAndRejectsEscapingWindows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		offset    int
		wantError bool
	}{
		{name: "native-openbox-decoration", offset: 40},
		{name: "still-outside-selected-screen", offset: 128, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := screenFixtureFor(t)
			writeHelper(t, filepath.Join(f.bin, "xdotool"), fmt.Sprintf(`case "$1" in
search) printf '101\n';;
windowsize) printf 'X=%%s\nY=%%s\nWIDTH=%%s\nHEIGHT=%%s\n' "$(( ${10}+2 ))" "$(( ${11}+%d ))" "$4" "$5";;
*) exit 90;;
esac
`, tc.offset))
			item := available{profile: f.config.Screens[0], rect: rectangle{Width: 1440, Height: 900}}
			err := place(context.Background(), item, 1001)
			if (err != nil) != tc.wantError {
				t.Fatal("native decoration reservation weakened or rejected measured screen bounds", err)
			}
		})
	}
}

func TestMacGeometryUsesExactNativeDisplayAndPhysicalPixels(t *testing.T) {
	screens := []macMonitor{
		{ID: "7", Width: 1920, Height: 1080, PixelWidth: 3840, PixelHeight: 2160, Primary: true},
		{ID: "8", X: -1280, Width: 1280, Height: 720, PixelWidth: 1280, PixelHeight: 720},
	}
	profile := config.Screen{ID: "retina", Scope: "screen", Display: "macos:7", Width: 3840, Height: 2160}
	got, primary, err := macGeometry(profile, screens)
	if err != nil || !primary || got != (rectangle{Width: 1920, Height: 1080}) {
		t.Fatal("native pixel geometry was confused with placement coordinates", got, primary, err)
	}
	profile.Width = 1920
	if _, _, err := macGeometry(profile, screens); err == nil {
		t.Fatal("changed native screen dimensions were accepted")
	}
	profile.Scope, profile.Width, profile.Height = "desktop", 3200, 1080
	got, _, err = macGeometry(profile, screens)
	if err != nil || got != (rectangle{X: -1280, Width: 3200, Height: 1080}) {
		t.Fatal("whole-desktop fallback omitted negative-origin monitor", got, err)
	}
	profile.Display = "macos:9"
	if got, _, err := macGeometry(profile, screens); err != nil || got.Width != 0 {
		t.Fatal("missing selected native display was replaced with another desktop", got, err)
	}
	if got, _, err := macGeometry(profile, nil); err != nil || got.Width != 0 {
		t.Fatal("headless native metadata fabricated a display", got, err)
	}
	if !strings.Contains(macPermissionScript, "AXIsProcessTrusted()") || strings.Contains(macPermissionScript, "WithOptions") || strings.Contains(macPlacementScript, "System Events") {
		t.Fatal("native placement may prompt or use unbounded AppleEvents automation")
	}
}

func TestInventoryBackendProbeIsBoundedAndNoninteractive(t *testing.T) {
	f := screenFixtureFor(t)
	hung := rfb(t, "")
	f.config.Screens = f.config.Screens[:1]
	f.config.Screens[0].VNCAddress = hung.address
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, err := Inventory(ctx, f.config)
	if len(got) != 0 || time.Since(start) > time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("hung backend was reported available or ignored the context bound", got, err)
	}
	if hung.received.Load() != 0 {
		t.Fatal("readiness probe sent a login or framebuffer request")
	}
}

func TestSessionExpiryStopsCaptureAndOwnedApplication(t *testing.T) {
	f := screenFixtureFor(t)
	session := validSession("right")
	session.ApplicationID = "browser"
	session.ExpiresAt = time.Now().Unix() + 2
	endpoint, err := Open(context.Background(), f.config, session)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	banner := make([]byte, 12)
	if _, err := io.ReadFull(endpoint, banner); err != nil {
		t.Fatal(err)
	}
	pid := applicationPID(t, f)
	waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
	if _, err := endpoint.Read(make([]byte, 1)); err == nil {
		t.Fatal("expired session retained capture transport")
	}
}

func TestUnavailableOtherMonitorDoesNotRedirectOrBlockChosenScreen(t *testing.T) {
	f := screenFixtureFor(t)
	f.config.Screens[1].X = 500
	endpoint, err := Open(context.Background(), f.config, validSession("left"))
	if err != nil {
		t.Fatal("unavailable unrelated monitor blocked the available chosen monitor", err)
	}
	defer endpoint.Close()
	if _, err := os.Stat(filepath.Join(f.home, "app.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capture-only session implicitly launched an application", err)
	}
}
