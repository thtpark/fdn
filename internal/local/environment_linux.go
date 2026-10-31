package local

import (
	"os"
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

func collectEnvironmentSystem(o *domain.EnvironmentObservation) {
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		environmentProblem(o, "os-version", "os-observation-unavailable")
	} else {
		for _, line := range strings.Split(string(release), "\n") {
			if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				o.System.OSVersion = strings.Trim(value, `"'`)
				break
			}
		}
	}
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		environmentProblem(o, "kernel", "os-observation-unavailable")
	} else {
		o.System.Kernel = unix.ByteSliceToString(info.Release[:])
	}
	cpu, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		environmentProblem(o, "cpu-model", "os-observation-unavailable")
	} else {
		cores := map[string]bool{}
		for _, block := range strings.Split(string(cpu), "\n\n") {
			physical, core := "", ""
			for _, line := range strings.Split(block, "\n") {
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				switch strings.TrimSpace(key) {
				case "physical id":
					physical = strings.TrimSpace(value)
				case "core id":
					core = strings.TrimSpace(value)
				}
			}
			if physical != "" && core != "" {
				cores[physical+":"+core] = true
			}
		}
		if len(cores) != 0 {
			count := uint64(len(cores))
			o.CPU.PhysicalCores = &count
		}
		for _, line := range strings.Split(string(cpu), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok && (strings.TrimSpace(key) == "model name" || strings.TrimSpace(key) == "Hardware") {
				o.CPU.Model = strings.TrimSpace(value)
				break
			}
		}
	}
	memory, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		environmentProblem(o, "memory-available", "os-observation-unavailable")
		return
	}
	for _, line := range strings.Split(string(memory), "\n") {
		if value, ok := strings.CutPrefix(line, "MemFree:"); ok {
			fields := strings.Fields(value)
			if len(fields) == 2 && fields[1] == "kB" {
				count, err := strconv.ParseUint(fields[0], 10, 64)
				if err == nil {
					bytes := count * 1024
					o.Memory.FreeBytes = &bytes
				}
			}
		}
	}
	for _, line := range strings.Split(string(memory), "\n") {
		if value, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			fields := strings.Fields(value)
			if len(fields) == 2 && fields[1] == "kB" {
				count, err := strconv.ParseUint(fields[0], 10, 64)
				if err == nil {
					bytes := count * 1024
					o.Memory.AvailableBytes = &bytes
					return
				}
			}
		}
	}
	environmentProblem(o, "memory-available", "os-observation-unavailable")
}
