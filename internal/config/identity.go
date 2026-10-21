package config

import (
	"github.com/thtpark/fdn/internal/domain"
	"path/filepath"
)

type IdentityService struct {
	StateDir  string `json:"state_dir"`
	Socket    string `json:"socket"`
	WorkerUID int    `json:"worker_uid"`
	WorkerGID int    `json:"worker_gid"`
	Worker    Worker `json:"worker"`
}

type ConnectionProfile struct {
	Identity   domain.AgentIdentity `json:"identity"`
	URL        string               `json:"url"`
	CA         string               `json:"ca"`
	SSHHost    string               `json:"ssh_host"`
	SSHPort    int                  `json:"ssh_port"`
	KnownHosts string               `json:"known_hosts"`
}

func (c IdentityService) Validate() error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) || c.WorkerUID < 0 || c.WorkerGID < 0 {
		return domain.Fail(domain.Invalid, "identity service needs absolute private state/socket paths and explicit worker UID/GID")
	}
	return c.Worker.Validate()
}
