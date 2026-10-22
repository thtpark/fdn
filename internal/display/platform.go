package display

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

var desktopPattern = regexp.MustCompile(`(?m)^Screen [0-9]+:.* current ([0-9]+) x ([0-9]+),`)
var outputPattern = regexp.MustCompile(`(?m)^\S+ connected (primary )?([0-9]+)x([0-9]+)([+-][0-9]+)([+-][0-9]+)(?: |$)`)

type monitor struct {
	rectangle
	primary bool
}

func probe(ctx context.Context, profile config.Screen) (rectangle, bool, error) {
	if strings.HasPrefix(profile.Display, "macos:") {
		return probeMac(ctx, profile)
	}
	if !config.LocalDisplay(profile.Display) {
		return rectangle{}, false, domain.Fail(domain.Prerequisite, "no explicit existing local display is available for this backend")
	}
	data, err := runTool(ctx, profile, "xrandr", "--display", profile.Display, "--current")
	if err != nil {
		return rectangle{}, false, err
	}
	desktop, monitors := parseX11(data)
	if len(monitors) == 0 || desktop.Width == 0 || desktop.Height == 0 {
		return rectangle{}, false, nil
	}
	if profile.Scope == "desktop" {
		if profile.ID != LegacyDesktopID && (desktop.Width != profile.Width || desktop.Height != profile.Height) {
			return rectangle{}, false, domain.Fail(domain.Prerequisite, "existing desktop geometry does not match the verified fallback profile")
		}
		return desktop, true, nil
	}
	target := rectangle{profile.X, profile.Y, profile.Width, profile.Height}
	var selected *monitor
	for _, item := range monitors {
		if item.rectangle == target {
			if selected != nil {
				return rectangle{}, false, domain.Fail(domain.Prerequisite, "configured geometry identifies more than one active monitor")
			}
			copy := item
			selected = &copy
		}
	}
	if selected == nil {
		return rectangle{}, false, domain.Fail(domain.Prerequisite, "selected monitor geometry is not present on the existing local display")
	}
	return selected.rectangle, selected.primary, nil
}

func parseX11(data []byte) (rectangle, []monitor) {
	match := desktopPattern.FindSubmatch(data)
	if len(match) != 3 {
		return rectangle{}, nil
	}
	width, _ := strconv.Atoi(string(match[1]))
	height, _ := strconv.Atoi(string(match[2]))
	if width < 1 || width > 32768 || height < 1 || height > 32768 {
		return rectangle{}, nil
	}
	result := []monitor{}
	for _, match := range outputPattern.FindAllSubmatch(data, 33) {
		w, _ := strconv.Atoi(string(match[2]))
		h, _ := strconv.Atoi(string(match[3]))
		x, _ := strconv.Atoi(string(match[4]))
		y, _ := strconv.Atoi(string(match[5]))
		if len(result) >= 32 || w < 1 || w > 32768 || h < 1 || h > 32768 || x < -131072 || x > 131072 || y < -131072 || y > 131072 {
			return rectangle{}, nil
		}
		result = append(result, monitor{rectangle{x, y, w, h}, len(match[1]) != 0})
	}
	return rectangle{Width: width, Height: height}, result
}

func placementReady(ctx context.Context, item available) error {
	if strings.HasPrefix(item.profile.Display, "macos:") {
		data, err := runTool(ctx, item.profile, "/usr/bin/osascript", "-l", "JavaScript", "-e", macPermissionScript)
		if err != nil || strings.TrimSpace(string(data)) != "true" {
			return errors.Join(domain.Fail(domain.Prerequisite, "macOS Accessibility permission for the native placement helper is required; no permission prompt or application was started"), err)
		}
		return nil
	}
	_, err := executable("xdotool")
	return err
}

func place(ctx context.Context, item available, pid int) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// Leave room for window-manager decorations without accepting placement on
	// another monitor. Returned client/window bounds are still checked below.
	marginX, marginY := min(32, item.rect.Width/8), min(32, item.rect.Height/8)
	desired := rectangle{item.rect.X + marginX, item.rect.Y + marginY, item.rect.Width - 2*marginX, item.rect.Height - 2*marginY}
	if strings.HasPrefix(item.profile.Display, "macos:") {
		data, err := runTool(ctx, item.profile, "/usr/bin/osascript", "-l", "JavaScript", "-e", macPlacementScript,
			strconv.Itoa(pid), strconv.Itoa(desired.X), strconv.Itoa(desired.Y), strconv.Itoa(desired.Width), strconv.Itoa(desired.Height))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) != "placed" {
			return domain.Fail(domain.Prerequisite, "macOS native placement was not proved; check Accessibility permission and a foreground non-reusing application")
		}
		return nil
	}
	data, err := runTool(ctx, item.profile, "xdotool", "search", "--sync", "--onlyvisible", "--pid", strconv.Itoa(pid))
	if err != nil {
		return err
	}
	ids := strings.Fields(string(data))
	if len(ids) == 0 || len(ids) > 32 {
		return domain.Fail(domain.Prerequisite, "application has no bounded set of visible windows owned by its launched process")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		value, err := strconv.ParseUint(id, 10, 64)
		if err != nil || value == 0 || seen[id] {
			return domain.Fail(domain.Prerequisite, "native placement did not identify unique application-owned windows")
		}
		seen[id] = true
		data, err = runTool(ctx, item.profile, "xdotool", "windowsize", "--sync", id, strconv.Itoa(desired.Width), strconv.Itoa(desired.Height),
			"windowmove", "--sync", "--", id, strconv.Itoa(desired.X), strconv.Itoa(desired.Y), "getwindowgeometry", "--shell", id)
		if err != nil {
			return err
		}
		actual, ok := parseWindow(data)
		if !ok || actual.X < item.rect.X || actual.Y < item.rect.Y || actual.Width > item.rect.Width || actual.Height > item.rect.Height ||
			actual.X+actual.Width > item.rect.X+item.rect.Width || actual.Y+actual.Height > item.rect.Y+item.rect.Height {
			return domain.Fail(domain.Prerequisite, "window manager did not prove application placement within the selected screen")
		}
	}
	return nil
}

func parseWindow(data []byte) (rectangle, bool) {
	values := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "X" && key != "Y" && key != "WIDTH" && key != "HEIGHT") {
			continue
		}
		if _, exists := values[key]; exists {
			return rectangle{}, false
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < -131072 || n > 131072 {
			return rectangle{}, false
		}
		values[key] = n
	}
	return rectangle{values["X"], values["Y"], values["WIDTH"], values["HEIGHT"]}, len(values) == 4 && values["WIDTH"] > 0 && values["HEIGHT"] > 0
}

type macMonitor struct {
	ID                  string `json:"id"`
	X, Y, Width, Height int
	PixelWidth          int `json:"pixel_width"`
	PixelHeight         int `json:"pixel_height"`
	Primary             bool
}

func probeMac(ctx context.Context, profile config.Screen) (rectangle, bool, error) {
	if runtime.GOOS != "darwin" {
		return rectangle{}, false, domain.Fail(domain.Prerequisite, "native macOS display profiles require macOS")
	}
	console, err := os.Stat("/dev/console")
	if err != nil || os.Geteuid() == 0 || secure.CheckOwner(console) != nil {
		return rectangle{}, false, domain.Fail(domain.Prerequisite, "macOS needs an existing logged-in graphical session owned by the runner UID")
	}
	data, err := runTool(ctx, profile, "/usr/bin/osascript", "-l", "JavaScript", "-e", macInventoryScript)
	if err != nil {
		return rectangle{}, false, err
	}
	var screens []macMonitor
	if json.Unmarshal(data, &screens) != nil || len(screens) > 32 {
		return rectangle{}, false, domain.Fail(domain.Prerequisite, "native macOS display metadata was not available")
	}
	return macGeometry(profile, screens)
}

func macGeometry(profile config.Screen, screens []macMonitor) (rectangle, bool, error) {
	var minX, minY, maxX, maxY int
	present := false
	for i, screen := range screens {
		if screen.Width < 1 || screen.Width > 32768 || screen.Height < 1 || screen.Height > 32768 ||
			screen.PixelWidth < 1 || screen.PixelWidth > 32768 || screen.PixelHeight < 1 || screen.PixelHeight > 32768 ||
			screen.X < -131072 || screen.X > 131072 || screen.Y < -131072 || screen.Y > 131072 {
			return rectangle{}, false, domain.Fail(domain.Prerequisite, "native display geometry is unsupported")
		}
		if i == 0 {
			minX, minY, maxX, maxY = screen.X, screen.Y, screen.X+screen.Width, screen.Y+screen.Height
		} else {
			minX, minY = min(minX, screen.X), min(minY, screen.Y)
			maxX, maxY = max(maxX, screen.X+screen.Width), max(maxY, screen.Y+screen.Height)
		}
		if profile.Display != "macos:"+screen.ID {
			continue
		}
		present = true
		if profile.Scope != "desktop" {
			if profile.Width != screen.PixelWidth || profile.Height != screen.PixelHeight || profile.X != screen.X || profile.Y != screen.Y {
				return rectangle{}, false, domain.Fail(domain.Prerequisite, "native macOS screen geometry changed; reverify the selected backend")
			}
			return rectangle{screen.X, screen.Y, screen.Width, screen.Height}, screen.Primary, nil
		}
	}
	if profile.Scope == "desktop" && present {
		desktop := rectangle{minX, minY, maxX - minX, maxY - minY}
		if desktop.Width > 32768 || desktop.Height > 32768 || (profile.ID != LegacyDesktopID && (profile.Width != desktop.Width || profile.Height != desktop.Height)) {
			return rectangle{}, false, domain.Fail(domain.Prerequisite, "native desktop dimensions do not match the verified whole-desktop backend")
		}
		return desktop, true, nil
	}
	return rectangle{}, false, nil
}

const macInventoryScript = `ObjC.import('AppKit'); ObjC.import('CoreGraphics');
function run() {
  var result = [], screens = $.NSScreen.screens;
  for (var i = 0; i < screens.count; i++) {
    var screen = screens.objectAtIndex(i);
    var id = Number(ObjC.unwrap(screen.deviceDescription.objectForKey('NSScreenNumber')));
    if (!$.CGDisplayIsActive(id) || !$.CGDisplayIsOnline(id)) continue;
    var b = $.CGDisplayBounds(id);
    result.push({id:String(id), X:b.origin.x, Y:b.origin.y, Width:b.size.width, Height:b.size.height,
      pixel_width:Number($.CGDisplayPixelsWide(id)), pixel_height:Number($.CGDisplayPixelsHigh(id)), Primary:Boolean($.CGDisplayIsMain(id))});
  }
  return JSON.stringify(result);
}`

const macPermissionScript = `ObjC.import('ApplicationServices'); function run() { return $.AXIsProcessTrusted() ? 'true' : 'false'; }`

// Explicit opaque-pointer bindings avoid JXA's incompatible CF/AX typedef refs.
const macAXBindings = `
ObjC.bindFunction('AXUIElementCopyAttributeValue', ['int', ['void *', 'id', 'void *']]);
ObjC.bindFunction('AXUIElementSetAttributeValue', ['int', ['void *', 'id', 'void *']]);
ObjC.bindFunction('AXUIElementGetPid', ['int', ['void *', 'int *']]);
ObjC.bindFunction('AXValueGetValue', ['bool', ['void *', 'int', 'void *']]);
ObjC.bindFunction('CFArrayGetCount', ['long', ['void *']]);
ObjC.bindFunction('CFArrayGetValueAtIndex', ['void *', ['void *', 'long']]);
`

const macPlacementScript = `ObjC.import('ApplicationServices'); ObjC.import('Foundation');` + macAXBindings + `
function run(argv) {
  if (!$.AXIsProcessTrusted()) return 'permission-required';
  var pid = Number(argv[0]), app = $.AXUIElementCreateApplication(pid), windows = Ref('void *'), count = 0;
  for (var attempt = 0; attempt < 20; attempt++) {
    if ($.AXUIElementCopyAttributeValue(app, $('AXWindows'), windows) === 0) {
      count = Number($.CFArrayGetCount(windows[0]));
      if (count > 0) break;
    }
    $.NSThread.sleepForTimeInterval(0.1);
  }
  if (count < 1 || count > 32) return 'no-window';
  var point = $.NSValue.valueWithPoint($.NSMakePoint(Number(argv[1]), Number(argv[2])));
  var size = $.NSValue.valueWithSize($.NSMakeSize(Number(argv[3]), Number(argv[4])));
  var pointBytes = $.NSMutableData.dataWithLength(16), sizeBytes = $.NSMutableData.dataWithLength(16);
  point.getValue(pointBytes.mutableBytes); size.getValue(sizeBytes.mutableBytes);
  var position = $.AXValueCreate(1, pointBytes.bytes), dimensions = $.AXValueCreate(2, sizeBytes.bytes);
  for (var i = 0; i < count; i++) {
    var window = $.CFArrayGetValueAtIndex(windows[0], i), actualPID = Ref('int');
    if ($.AXUIElementGetPid(window, actualPID) !== 0 || Number(actualPID[0]) !== pid) return 'wrong-owner';
    if ($.AXUIElementSetAttributeValue(window, $('AXSize'), dimensions) !== 0 || $.AXUIElementSetAttributeValue(window, $('AXPosition'), position) !== 0) return 'placement-failed';
    var p = Ref('void *'), s = Ref('void *'), actualPointBytes = $.NSMutableData.dataWithLength(16), actualSizeBytes = $.NSMutableData.dataWithLength(16);
    if ($.AXUIElementCopyAttributeValue(window, $('AXPosition'), p) !== 0 || $.AXUIElementCopyAttributeValue(window, $('AXSize'), s) !== 0 ||
        !$.AXValueGetValue(p[0], 1, actualPointBytes.mutableBytes) || !$.AXValueGetValue(s[0], 2, actualSizeBytes.mutableBytes)) return 'placement-unproved';
    var actualPoint = $.NSValue.valueWithBytesObjCType(actualPointBytes.bytes, point.objCType).pointValue;
    var actualSize = $.NSValue.valueWithBytesObjCType(actualSizeBytes.bytes, size.objCType).sizeValue;
    var x = Number(argv[1]), y = Number(argv[2]), w = Number(argv[3]), h = Number(argv[4]);
    if (actualPoint.x < x || actualPoint.y < y || actualSize.width < 1 || actualSize.height < 1 ||
        actualPoint.x + actualSize.width > x + w || actualPoint.y + actualSize.height > y + h) return 'placement-unproved';
  }
  return 'placed';
}`
