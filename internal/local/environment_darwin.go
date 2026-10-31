package local

import (
	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

func collectSystemDetails(o *SystemCapabilities) {
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"kern.osproductversion", &o.System.OSVersion}, {"kern.osrelease", &o.System.Kernel},
		{"machdep.cpu.brand_string", &o.CPU.Model},
	} {
		value, err := unix.Sysctl(field.name)
		if err != nil {
			appendEnvironmentProblem(&o.Problems, field.name, "os-observation-unavailable")
		} else {
			*field.target = value
		}
	}
	physical, err := unix.SysctlUint32("hw.physicalcpu")
	if err != nil {
		appendEnvironmentProblem(&o.Problems, "physical-cpu", "os-observation-unavailable")
	} else {
		value := uint64(physical)
		o.CPU.PhysicalCores = &value
	}
}

func collectSystemUsage(o *domain.EnvironmentObservation) {
	free, err := unix.SysctlUint32("vm.page_free_count")
	page, pageErr := unix.SysctlUint32("vm.pagesize")
	if err != nil || pageErr != nil {
		environmentProblem(o, "memory-free", "os-observation-unavailable")
	} else {
		value := uint64(free) * uint64(page)
		o.Memory.FreeBytes = &value
	}
}
