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
