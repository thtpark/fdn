package config

import (
	"github.com/thtpark/fdn/internal/domain"
	"os"
	"path/filepath"
)

type IdentityService struct {
	StateDir  string `json:"state_dir"`
	Socket    string `json:"socket"`
	WorkerUID int    `json:"worker_uid,omitempty"`
	WorkerGID int    `json:"worker_gid,omitempty"`
	Worker    Worker `json:"worker"`
}

type ConnectionProfile = domain.ConnectionProfile

func (c IdentityService) WithCurrentUser() IdentityService {
	if c.WorkerUID == 0 {
		c.WorkerUID = os.Geteuid()
	}
	if c.WorkerGID == 0 {
		c.WorkerGID = os.Getegid()
	}
	return c
}

func (c IdentityService) Validate() error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) || c.WorkerUID < 0 || c.WorkerGID < 0 {
		return domain.Fail(domain.Invalid, "identity service needs absolute private state/socket paths and nonnegative optional current-user UID/GID")
	}
	return c.Worker.Validate()
}
