package local

import "golang.org/x/sys/unix"

func physicalMemory() (uint64, error) { return unix.SysctlUint64("hw.memsize") }
