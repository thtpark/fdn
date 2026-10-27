package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserDefaultsStayUnderFoundationRegardlessOfXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := DefaultRoot()
	if err != nil || root != filepath.Join(home, ".foundation") {
		t.Fatal(root, err)
	}
	path, err := DefaultConfig()
	if err != nil || path != filepath.Join(root, "config", "agent.json") {
		t.Fatal(path, err)
	}
	server, err := DefaultServerConfig()
	if err != nil || server != filepath.Join(root, "config", "server.json") {
		t.Fatal(server, err)
	}
	worker := DefaultWorker(filepath.Join(root, "state"))
	if worker.WorkspaceRoot != filepath.Join(root, "workspaces") {
		t.Fatal("workspaces escaped the Foundation directory", worker.WorkspaceRoot)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("looking up defaults created state", err)
	}
}

func TestDefaultsIgnoreOtherConfigurationsAndPreserveExplicitPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".config", "fdn", "agent.json")
	if err := Write(legacy, DefaultWorker(filepath.Join(home, "retained-state"))); err != nil {
		t.Fatal(err)
	}
	if path, err := DefaultConfig(); err != nil || path != filepath.Join(home, ".foundation", "config", "agent.json") {
		t.Fatal("unrelated configuration changed the current default", path, err)
	}
	if _, err := LoadWorker(legacy); err != nil {
		t.Fatal("explicit existing configuration no longer works", err)
	}
	if err := Write(filepath.Join(home, ".foundation", "config", "agent.json"), DefaultWorker(filepath.Join(home, ".foundation", "state"))); err != nil {
		t.Fatal(err)
	}
	if path, err := DefaultConfig(); err != nil || path != filepath.Join(home, ".foundation", "config", "agent.json") {
		t.Fatal("explicitly prepared new layout was not selected", path, err)
	}
}
