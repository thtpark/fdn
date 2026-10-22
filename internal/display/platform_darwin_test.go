package display

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/config"
)

// These are pure AX/CF value conversions and the helper's own PID, not display,
// window, permission, screenshot or login probes.
func TestMacAXBridgeUsesSafeOwnedReferencesAndValueBuffers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	script := `ObjC.import('ApplicationServices'); ObjC.import('Foundation');` + macAXBindings + `
function run() {
  var ownPID = Number($.NSProcessInfo.processInfo.processIdentifier), app = $.AXUIElementCreateApplication(ownPID);
  var array = $.CFArrayCreateMutable(null, 0, null);
  $.CFArrayAppendValue(array, app);
  var arraySlot = Ref('void *'); arraySlot[0] = array;
  if (Number($.CFArrayGetCount(arraySlot[0])) !== 1) throw Error('opaque array slot failed');
  var element = $.CFArrayGetValueAtIndex(arraySlot[0], 0), pid = Ref('int');
  if ($.AXUIElementGetPid(element, pid) !== 0 || Number(pid[0]) !== ownPID) throw Error('owned reference failed');
  var point = $.NSValue.valueWithPoint($.NSMakePoint(1, 2)), size = $.NSValue.valueWithSize($.NSMakeSize(3, 4));
  var pBytes = $.NSMutableData.dataWithLength(16), sBytes = $.NSMutableData.dataWithLength(16);
  point.getValue(pBytes.mutableBytes); size.getValue(sBytes.mutableBytes);
  var p = $.AXValueCreate(1, pBytes.bytes), s = $.AXValueCreate(2, sBytes.bytes);
  var outP = $.NSMutableData.dataWithLength(16), outS = $.NSMutableData.dataWithLength(16);
  if (!$.AXValueGetValue(p, 1, outP.mutableBytes) || !$.AXValueGetValue(s, 2, outS.mutableBytes)) throw Error('value buffer failed');
  var actualP = $.NSValue.valueWithBytesObjCType(outP.bytes, point.objCType).pointValue;
  var actualS = $.NSValue.valueWithBytesObjCType(outS.bytes, size.objCType).sizeValue;
  return JSON.stringify({x:actualP.x, y:actualP.y, width:actualS.width, height:actualS.height});
}`
	data, err := runTool(context.Background(), config.Screen{Display: "macos:1"}, "/usr/bin/osascript", "-l", "JavaScript", "-e", script)
	if err != nil {
		t.Fatal("pure-data native AX bridge failed", err)
	}
	var result struct{ X, Y, Width, Height int }
	if json.Unmarshal(data, &result) != nil || result.X != 1 || result.Y != 2 || result.Width != 3 || result.Height != 4 {
		t.Fatal("AX geometry conversion was not proved")
	}
}
