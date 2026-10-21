package config

import (
	"path/filepath"
	"testing"
)

func TestSecureServerConfigurationCannotMixUnixIngressAndTCP(t *testing.T) {
	root := t.TempDir()
	base := Server{StateDir: root, CertFile: filepath.Join(root, "server.pem"), KeyFile: filepath.Join(root, "server-key.pem"), IngressSocket: filepath.Join(root, "api.sock"), JoinSocket: filepath.Join(root, "join.sock"), IngressGroup: -1}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name   string
		change func(*Server)
	}{
		{"loopback-tcp", func(c *Server) { c.Listen = "127.0.0.1:8443" }},
		{"public-tcp", func(c *Server) { c.Listen = "0.0.0.0:8443" }},
		{"missing-join", func(c *Server) { c.JoinSocket = "" }},
		{"same-socket", func(c *Server) { c.JoinSocket = c.IngressSocket }},
		{"relative-api", func(c *Server) { c.IngressSocket = "api.sock" }},
		{"relative-provisioner", func(c *Server) { c.AccountSocket = "accounts.sock" }},
		{"invalid-group", func(c *Server) { c.IngressGroup = -2 }},
	} {
		t.Run(item.name, func(t *testing.T) {
			c := base
			item.change(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("insecure mixed configuration accepted")
			}
		})
	}
}

func TestWorkerIdentitySocketMustBeAbsolute(t *testing.T) {
	worker := DefaultWorker(t.TempDir())
	worker.IdentitySocket = "relative.sock"
	if err := worker.Validate(); err == nil {
		t.Fatal("relative credential service socket accepted")
	}
	worker.IdentitySocket = filepath.Join(worker.StateDir, "identity.sock")
	if err := worker.Validate(); err != nil {
		t.Fatal(err)
	}
}
