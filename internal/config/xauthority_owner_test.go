package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type xauthorityMetadata struct {
	mode os.FileMode
	size int64
	uid  uint32
	stat bool
}

func (i xauthorityMetadata) Name() string       { return "synthetic-authority" }
func (i xauthorityMetadata) Size() int64        { return i.size }
func (i xauthorityMetadata) Mode() os.FileMode  { return i.mode }
func (i xauthorityMetadata) ModTime() time.Time { return time.Time{} }
func (i xauthorityMetadata) IsDir() bool        { return i.mode.IsDir() }
func (i xauthorityMetadata) Sys() any {
	if !i.stat {
		return nil
	}
	return &syscall.Stat_t{Uid: i.uid}
}

func TestXAuthorityMetadataRequiresExactExpectedRunnerUID(t *testing.T) {
	runner := xauthorityMetadata{mode: 0600, uid: 1004, stat: true}
	if err := checkXAuthorityMetadata(runner, 1004); err != nil {
		t.Fatal("authorized native inspector rejected runner-owned 0600 metadata", err)
	}
	if err := checkXAuthorityMetadata(runner, 0); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("metadata validation substituted the root inspector for its runner", err)
	}
	for _, item := range []struct {
		name string
		info xauthorityMetadata
	}{
		{"wrong-owner", xauthorityMetadata{mode: 0600, uid: 1005, stat: true}},
		{"root-owned", xauthorityMetadata{mode: 0600, uid: 0, stat: true}},
		{"directory", xauthorityMetadata{mode: os.ModeDir | 0700, uid: 1004, stat: true}},
		{"symlink", xauthorityMetadata{mode: os.ModeSymlink | 0600, uid: 1004, stat: true}},
		{"pipe", xauthorityMetadata{mode: os.ModeNamedPipe | 0600, uid: 1004, stat: true}},
		{"socket", xauthorityMetadata{mode: os.ModeSocket | 0600, uid: 1004, stat: true}},
		{"public", xauthorityMetadata{mode: 0644, uid: 1004, stat: true}},
		{"oversized", xauthorityMetadata{mode: 0600, uid: 1004, size: 1<<20 + 1, stat: true}},
		{"unknown-owner", xauthorityMetadata{mode: 0600}},
	} {
		t.Run(item.name, func(t *testing.T) {
			if err := checkXAuthorityMetadata(item.info, 1004); domain.AsFault(err).Code != domain.Denied {
				t.Fatal("unsafe Xauthority metadata was accepted", err)
			}
		})
	}
	own := runner
	own.uid = uint32(os.Geteuid())
	if err := checkXAuthorityMetadata(own, uint32(os.Geteuid())); err != nil {
		t.Fatal("runtime rejected its actual EUID", err)
	}
	own.uid++
	if err := checkXAuthorityMetadata(own, uint32(os.Geteuid())); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("runtime accepted another account's metadata", err)
	}
}

func TestWorkerOwnerValidationDoesNotChangeRuntimeOwnership(t *testing.T) {
	c := screenWorker(t)
	path := filepath.Join(c.StateDir, "authority")
	if err := os.WriteFile(path, []byte("synthetic cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Screens[0].XAuthority = path
	owner := uint32(os.Geteuid())
	if err := c.ValidateForOwner(owner); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateForOwner(owner + 1); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("explicit native owner was ignored", err)
	}
	for _, validate := range []func() error{c.Validate, c.ValidateScreens, func() error { return CheckXAuthority(path) }} {
		if err := validate(); err != nil {
			t.Fatal("native inspection changed the actual runtime owner", err)
		}
	}
	if err := Decode([]byte(`{"owner_uid":1004}`), new(Worker)); err == nil {
		t.Fatal("expected owner became an untrusted worker configuration override")
	}
	link := filepath.Join(c.StateDir, "authority-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, authority := range []string{link, c.StateDir} {
		c.Screens[0].XAuthority = authority
		if err := c.ValidateForOwner(owner); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("native owner checking accepted nonregular/symlink authority", err)
		}
		if err := c.Validate(); domain.AsFault(err).Code != domain.Denied {
			t.Fatal("runtime owner checking accepted nonregular/symlink authority", err)
		}
	}
}
