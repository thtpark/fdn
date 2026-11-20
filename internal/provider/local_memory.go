package provider

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

type LocalMemory struct {
	Source     string `json:"source"`
	TotalBytes int64  `json:"total_bytes"`
	GPUCount   int    `json:"gpu_count"`
}

func (m *Manager) ProbeLocalMemory(ctx context.Context) (LocalMemory, error) {
	if err := ctx.Err(); err != nil {
		return LocalMemory{}, err
	}
	if m.MemoryProbe != nil {
		return m.MemoryProbe(ctx)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := process.NewBuffer(16 << 10)
	err := m.Runner.Run(probeCtx, process.Spec{
		Path:   "nvidia-smi",
		Args:   []string{"--query-gpu=memory.total", "--format=csv,noheader,nounits"},
		Env:    process.Environment(),
		Stdout: output,
		Stderr: process.NewBuffer(4 << 10),
	})
	if err == nil {
		if data, readErr := output.Bytes(); readErr == nil {
			var totalMiB int64
			var gpus int
			valid := true
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				mib, parseErr := strconv.ParseInt(line, 10, 64)
				if parseErr != nil || mib <= 0 || mib > 1<<24 {
					valid = false
					break
				}
				totalMiB += mib
				gpus++
			}
			if valid && gpus > 0 && gpus <= 64 {
				return LocalMemory{
					Source:     "nvidia-vram",
					TotalBytes: totalMiB * (1 << 20),
					GPUCount:   gpus,
				}, nil
			}
		}
	}
	ram, err := localPhysicalMemory()
	if err != nil || ram == 0 {
		return LocalMemory{}, domain.Fail(domain.Prerequisite, "cannot determine GPU VRAM or system memory capacity for local model initialization")
	}
	return LocalMemory{
		Source:     "system-ram",
		TotalBytes: int64(ram),
		GPUCount:   0,
	}, nil
}
