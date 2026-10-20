package local

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

type Observation struct {
	Category     string  `json:"category"`
	Name         string  `json:"name"`
	Present      bool    `json:"present"`
	Availability string  `json:"availability"`
	Permission   string  `json:"permission"`
	Count        *uint64 `json:"count,omitempty"`
	Unit         string  `json:"unit,omitempty"`
	ErrorCode    string  `json:"error_code,omitempty"`
}

type Inventory struct {
	ID           string        `json:"id"`
	CollectedAt  int64         `json:"collected_at"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	CPUs         int           `json:"cpus"`
	Observations []Observation `json:"observations"`
}

func Collect(overrides ...map[string]string) Inventory {
	inv := Inventory{ID: domain.NewID("inventory"), CollectedAt: time.Now().UTC().Unix(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Observations: []Observation{}}
	for _, name := range []string{"git", "ssh", "tmux", "node", "npm", "codex", "copilot"} {
		_, err := exec.LookPath(name)
		if len(overrides) > 0 && overrides[0][name] != "" {
			info, statErr := os.Stat(overrides[0][name])
			err = statErr
			if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0) {
				err = os.ErrPermission
			}
		}
		inv.Observations = append(inv.Observations, Observation{Category: "tool", Name: name, Present: err == nil, Availability: "unknown", Permission: "not-checked"})
	}
	memory, memoryErr := physicalMemory()
	observation := Observation{Category: "hardware", Name: "physical-memory", Present: true, Availability: "available", Permission: "read-only", Count: &memory, Unit: "bytes"}
	if memoryErr != nil {
		observation.Count = nil
		observation.Availability = "unknown"
		observation.ErrorCode = "os-enumeration-failed"
	}
	inv.Observations = append(inv.Observations, observation)
	interfaces, networkErr := net.Interfaces()
	up := uint64(0)
	for _, device := range interfaces {
		if device.Flags&net.FlagUp != 0 && device.Flags&net.FlagLoopback == 0 {
			up++
		}
	}
	observation = Observation{Category: "network", Name: "up-non-loopback-interfaces", Present: up > 0, Availability: "unknown", Permission: "not-probed", Count: &up, Unit: "interfaces"}
	if networkErr != nil {
		observation.Count = nil
		observation.ErrorCode = "os-enumeration-failed"
	}
	inv.Observations = append(inv.Observations, observation)
	return inv
}

func (s *State) RefreshInventory(ctx context.Context, overrides ...map[string]string) (Inventory, error) {
	inv := Collect(overrides...)
	var stats unix.Statfs_t
	err := unix.Statfs(s.Root, &stats)
	observation := Observation{Category: "filesystem", Name: "private-state-free-space", Present: true, Availability: "available", Permission: "account-state-access", Unit: "bytes"}
	if err != nil {
		observation.Availability = "unknown"
		observation.ErrorCode = "os-enumeration-failed"
	} else {
		bytes := uint64(stats.Bavail) * uint64(stats.Bsize)
		observation.Count = &bytes
	}
	inv.Observations = append(inv.Observations, observation)
	return inv, s.Put(ctx, "inventory", inv)
}

func (s *State) Inventory(ctx context.Context) (Inventory, error) {
	var inv Inventory
	found, err := s.Get(ctx, "inventory", &inv)
	if err != nil {
		return inv, err
	}
	if !found {
		return inv, domain.Fail(domain.Prerequisite, "collect local capabilities before the initial AI review")
	}
	return inv, nil
}

func (i Inventory) Hash() string { data, _ := json.Marshal(i); return domain.Hash(string(data)) }

func (i Inventory) Summary() []Observation {
	return append([]Observation{}, i.Observations...)
}
