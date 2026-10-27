package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func screenWorker(t *testing.T) Worker {
	t.Helper()
	c := DefaultWorker(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.Screens = []Screen{{ID: "right", Name: "Right screen", Scope: "screen", Width: 1280, Height: 720, X: 1280,
		Display: ":0.0", VNCAddress: "127.0.0.1:5901", VNCVerified: true,
		Applications: []ScreenApplication{{ID: "browser", Executable: "/usr/bin/browser", Arguments: []string{"--new-window", "literal ; argument"}}}}}
	return c
}

func TestScreenProfilesAreBoundedLocalAndExplicit(t *testing.T) {
	for _, item := range []struct {
		name   string
		change func(*Worker)
	}{
		{"duplicate-screen", func(c *Worker) { c.Screens = append(c.Screens, c.Screens[0]) }},
		{"path-id", func(c *Worker) { c.Screens[0].ID = "../screen" }},
		{"oversized-id", func(c *Worker) { c.Screens[0].ID = strings.Repeat("a", 65) }},
		{"private-label", func(c *Worker) { c.Screens[0].Name = "/home/runner" }},
		{"control-label", func(c *Worker) { c.Screens[0].Name = "screen\nlabel" }},
		{"oversized-label", func(c *Worker) { c.Screens[0].Name = strings.Repeat("a", 129) }},
		{"empty-label", func(c *Worker) { c.Screens[0].Name = "" }},
		{"scope", func(c *Worker) { c.Screens[0].Scope = "isolated" }},
		{"geometry", func(c *Worker) { c.Screens[0].Width = 0 }},
		{"offset", func(c *Worker) { c.Screens[0].X = 131073 }},
		{"remote-x11", func(c *Worker) { c.Screens[0].Display = "other-account.example:0" }},
		{"invalid-x11", func(c *Worker) { c.Screens[0].Display = ":0 -auth stolen" }},
		{"absent-display", func(c *Worker) { c.Screens[0].Display = "" }},
		{"public-vnc", func(c *Worker) { c.Screens[0].VNCAddress = "0.0.0.0:5901" }},
		{"hostname-vnc", func(c *Worker) { c.Screens[0].VNCAddress = "remote.invalid:5901" }},
		{"zero-port", func(c *Worker) { c.Screens[0].VNCAddress = "127.0.0.1:0" }},
		{"verified-without-address", func(c *Worker) { c.Screens[0].VNCAddress = "" }},
		{"whole-desktop-reused", func(c *Worker) { c.VNCAddress, c.VNCVerified = c.Screens[0].VNCAddress, true }},
		{"duplicate-app", func(c *Worker) {
			c.Screens[0].Applications = append(c.Screens[0].Applications, c.Screens[0].Applications[0])
		}},
		{"unsafe-app-id", func(c *Worker) { c.Screens[0].Applications[0].ID = "app/other" }},
		{"relative-app", func(c *Worker) { c.Screens[0].Applications[0].Executable = "browser" }},
		{"nul-argument", func(c *Worker) { c.Screens[0].Applications[0].Arguments = []string{"a\x00b"} }},
		{"oversized-argument", func(c *Worker) { c.Screens[0].Applications[0].Arguments = []string{strings.Repeat("a", 4097)} }},
		{"too-many-arguments", func(c *Worker) { c.Screens[0].Applications[0].Arguments = make([]string, 65) }},
		{"relative-xauthority", func(c *Worker) { c.Screens[0].XAuthority = "another-home/.Xauthority" }},
		{"missing-xauthority", func(c *Worker) { c.Screens[0].XAuthority = filepath.Join(c.StateDir, "absent") }},
	} {
		t.Run(item.name, func(t *testing.T) {
			c := screenWorker(t)
			item.change(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("unsafe screen profile accepted")
			}
		})
	}
	c := screenWorker(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].Display = "macos:7"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].Scope, c.Screens[0].X = "desktop", 0
	if err := c.Validate(); err != nil {
		t.Fatal("explicitly labelled desktop fallback is valid", err)
	}
	c.Screens[0].VNCVerified = false
	if err := c.Validate(); err != nil {
		t.Fatal("unverified configuration must remain storable without advertising availability", err)
	}
}

func TestScreenXAuthorityMustBePrivateOwnedAndNotASymlink(t *testing.T) {
	c := screenWorker(t)
	path := filepath.Join(c.StateDir, "authority")
	if err := os.WriteFile(path, []byte("synthetic owner cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].XAuthority = path
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("public authorization file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.StateDir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].XAuthority = link
	if err := c.Validate(); err == nil {
		t.Fatal("symlink authority accepted")
	}
	c.Screens[0].XAuthority = c.StateDir
	if err := c.Validate(); err == nil {
		t.Fatal("directory authority accepted")
	}
	pipe := filepath.Join(c.StateDir, "authority-pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].XAuthority = pipe
	start := time.Now()
	if err := c.Validate(); err == nil || time.Since(start) > time.Second {
		t.Fatal("Xauthority validation accepted or blocked on a pipe", err)
	}
}

func TestScreenConfigRoundtripRetainsLocalSourceOnly(t *testing.T) {
	c := screenWorker(t)
	path := filepath.Join(c.StateDir, "agent.json")
	if err := Write(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWorker(path)
	if err != nil || got.SourcePath() != path || got.Screens[0].Applications[0].Arguments[1] != "literal ; argument" {
		t.Fatal(got, err)
	}
	data, err := json.Marshal(got)
	if err != nil || strings.Contains(string(data), "sourcePath") || strings.Contains(string(data), path) {
		t.Fatal("local configuration source leaked into JSON", err)
	}
	if Decode([]byte(`{"screens":[{"applications":[{"id":"browser","executable":"/bin/app","arguments":"--shell string"}]}]}`), &Worker{}) == nil {
		t.Fatal("shell-style argument string accepted")
	}
	if Decode([]byte(`{"screens":[{"unknown":true}]}`), &Worker{}) == nil {
		t.Fatal("unknown screen configuration accepted")
	}
	legacy := DefaultWorker(t.TempDir())
	legacy.VNCAddress, legacy.VNCVerified = "localhost:5900", true
	if err := legacy.Validate(); err == nil {
		t.Fatal("removed per-worker VNC fields were accepted")
	}
}

func TestScreenCountsAndPrimaryAreBounded(t *testing.T) {
	c := screenWorker(t)
	for i := 0; i < 16; i++ {
		screen := c.Screens[0]
		screen.ID, screen.VNCAddress = string(rune('a'+i)), ""
		screen.VNCVerified = false
		c.Screens = append(c.Screens, screen)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("too many screen profiles accepted")
	}
	c.Screens = c.Screens[:2]
	c.Screens[0].Primary, c.Screens[1].Primary = true, true
	if err := c.Validate(); err == nil {
		t.Fatal("multiple primary profiles accepted")
	}
	c.Screens = c.Screens[:1]
	for i := 0; i < 33; i++ {
		c.Screens[0].Applications = append(c.Screens[0].Applications, ScreenApplication{ID: "app-" + string(rune('a'+i)), Executable: "/bin/app"})
	}
	if err := c.Validate(); err == nil {
		t.Fatal("unbounded applications accepted")
	}
}
