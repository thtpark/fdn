package remote

import (
	"os"
	"testing"
)

func TestBrowserInstalledNoVNCModuleClosure(t *testing.T) {
	directory := os.Getenv("FOUNDATION_NOVNC_ASSETS")
	if directory == "" {
		t.Skip("set FOUNDATION_NOVNC_ASSETS to an explicitly built app's pinned noVNC directory")
	}
	assets, err := loadBrowserAssets(directory)
	if err != nil {
		t.Fatal("bundled noVNC is incompatible with the protected CLI viewer", err)
	}
	for _, name := range []string{"LICENSE.txt", "core/rfb.js", "core/util/logging.js"} {
		if len(assets[name]) == 0 {
			t.Errorf("bundled viewer module closure lacks %s", name)
		}
	}
	if len(assets) < 10 {
		t.Fatal("bundled viewer did not resolve the actual noVNC module closure")
	}
}
