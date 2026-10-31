package local

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"runtime"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

type SystemCapabilities struct {
	System           domain.SystemEnvironment    `json:"system"`
	CPU              domain.CPUEnvironment       `json:"cpu"`
	MemoryTotalBytes *uint64                     `json:"memory_total_bytes,omitempty"`
	DiskTotalBytes   *uint64                     `json:"disk_total_bytes,omitempty"`
	Problems         []domain.EnvironmentProblem `json:"problems"`
}

func (s *State) SystemCapabilitiesInventory(ctx context.Context) (Inventory, error) {
	return s.systemInventory(ctx, false)
}

func collectSystemCapabilities(stateRoot string) SystemCapabilities {
	c := SystemCapabilities{
		System: domain.SystemEnvironment{OS: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: runtime.Version()},
		CPU:    domain.CPUEnvironment{LogicalCores: runtime.NumCPU()}, Problems: []domain.EnvironmentProblem{},
	}
	memory, err := physicalMemory()
	if err != nil {
		appendEnvironmentProblem(&c.Problems, "memory-total", "os-observation-unavailable")
	} else {
		c.MemoryTotalBytes = &memory
	}
	collectSystemDetails(&c)
	if stateRoot != "" {
		var disk unix.Statfs_t
		if err := unix.Statfs(stateRoot, &disk); err != nil {
			appendEnvironmentProblem(&c.Problems, "state-filesystem", "os-observation-unavailable")
		} else {
			total := uint64(disk.Blocks) * uint64(disk.Bsize)
			c.DiskTotalBytes = &total
		}
	}
	return c
}

func (c SystemCapabilities) environment() domain.EnvironmentObservation {
	c.CPU.PhysicalCores = copyResourceCount(c.CPU.PhysicalCores)
	return domain.EnvironmentObservation{
		Version: domain.EnvironmentVersion, ObservedAt: time.Now().UTC().Unix(),
		System: c.System, CPU: c.CPU,
		Memory:      domain.MemoryEnvironment{TotalBytes: copyResourceCount(c.MemoryTotalBytes)},
		Disk:        domain.DiskEnvironment{TotalBytes: copyResourceCount(c.DiskTotalBytes)},
		Executables: []domain.FoundationExecutable{}, Problems: append([]domain.EnvironmentProblem{}, c.Problems...),
	}
}

func copyResourceCount(count *uint64) *uint64 {
	if count == nil {
		return nil
	}
	value := *count
	return &value
}

func (s *State) systemInventory(ctx context.Context, embedded bool, overrides ...map[string]string) (Inventory, error) {
	inventory, err := s.Inventory(ctx)
	if err == nil && inventory.SystemCapabilities != nil {
		return inventory, domain.ValidateEnvironment(inventory.SystemCapabilities.environment())
	}
	if err != nil && domain.AsFault(err).Code != domain.Prerequisite {
		return inventory, err
	}
	capabilities := collectSystemCapabilities(s.Root)
	if err := domain.ValidateEnvironment(capabilities.environment()); err != nil {
		return inventory, err
	}
	candidate := inventory
	if err != nil {
		candidate = collectInventory(capabilities, embedded, overrides...)
		appendInventoryStorage(&candidate, s.Root)
	} else {
		candidate.SystemCapabilities = &capabilities
	}
	return s.persistSystemInventory(ctx, candidate)
}

func (s *State) persistSystemInventory(ctx context.Context, candidate Inventory) (Inventory, error) {
	var inventory Inventory
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inventory, err
	}
	defer tx.Rollback()
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE key='inventory'").Scan(&data)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &inventory); err != nil {
			return inventory, err
		}
		if inventory.SystemCapabilities != nil {
			if err := domain.ValidateEnvironment(inventory.SystemCapabilities.environment()); err != nil {
				return inventory, err
			}
			return inventory, tx.Commit()
		}
		// Enrichment keeps the historical review basis, including unknown counts.
		capabilities := *candidate.SystemCapabilities
		capabilities.System.OS, capabilities.System.Architecture = inventory.OS, inventory.Arch
		capabilities.CPU.LogicalCores = inventory.CPUs
		capabilities.MemoryTotalBytes = nil
		for _, observation := range inventory.Observations {
			if observation.Category == "hardware" && observation.Name == "physical-memory" {
				capabilities.MemoryTotalBytes = copyResourceCount(observation.Count)
				break
			}
		}
		if capabilities.MemoryTotalBytes == nil {
			appendEnvironmentProblem(&capabilities.Problems, "memory-total", "inventory-observation-unavailable")
		}
		inventory.SystemCapabilities = &capabilities
	case errors.Is(err, sql.ErrNoRows):
		inventory = candidate
	default:
		return inventory, err
	}
	if err := domain.ValidateEnvironment(inventory.SystemCapabilities.environment()); err != nil {
		return inventory, err
	}
	data, err = json.Marshal(inventory)
	if err != nil {
		return inventory, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO kv(key,value) VALUES('inventory',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", data); err != nil {
		return inventory, err
	}
	return inventory, tx.Commit()
}
