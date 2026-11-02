package config

import (
	"path/filepath"
	"testing"
)

func TestConfigAndLoopback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	c := DefaultWorker(root)
	path := filepath.Join(root, "agent.json")
	if err := Write(path, c); err != nil {
		t.Fatal(err)
	}

	got, err := LoadWorker(path)
	if err != nil || got.Mode != "ai" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"http://localhost:8443", "https://example.com", "https://user@localhost", "https://localhost/a", "https://localhost?q=x"} {
		if URL(bad) == nil {
			t.Fatal(bad)
		}
	}
	for _, bad := range []string{":8443", "0.0.0.0:8443", "example.com:443"} {
		if Loopback(bad) == nil {
			t.Fatal(bad)
		}
	}
	if Loopback("[::1]:443") != nil || URL("https://127.0.0.1:8443") != nil {
		t.Fatal("valid loopback")
	}
	if Decode([]byte(`{"unknown":true}`), &Worker{}) == nil || Decode([]byte(`{} {}`), &Worker{}) == nil {
		t.Fatal("unknown/trailing fields accepted")
	}
	c.Mode = "bad"
	if c.Validate() == nil {
		t.Fatal("invalid mode")
	}
}

func TestServerFileDirectoryIsSeparateFromPrivateState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "server")
	c := Server{StateDir: state, CertFile: filepath.Join(state, "server.pem"),
		KeyFile: filepath.Join(state, "server-key.pem"), IngressSocket: "/run/fdn/api.sock",
		JoinSocket: "/run/fdn/join.sock"}
	if err := c.Validate(); err != nil || c.FileDirectory() != state+"-files" {
		t.Fatal("default content directory is not outside private state", c.FileDirectory(), err)
	}
	for _, invalid := range []string{state, filepath.Join(state, "files"), filepath.Dir(state), "/", "relative", state + "/../escape", state + "-files\n"} {
		c.FileDir = invalid
		if c.Validate() == nil {
			t.Fatal("unsafe or overlapping file_dir was accepted", invalid)
		}
	}
	c.FileDir = filepath.Join(filepath.Dir(state), "explicit-content")
	if err := c.Validate(); err != nil || c.FileDirectory() != c.FileDir {
		t.Fatal("explicit existing content path was not retained", err)
	}
}
