package provider

import "golang.org/x/sys/unix"

func localPhysicalMemory() (uint64, error) {
	return unix.SysctlUint64("hw.memsize")
}
