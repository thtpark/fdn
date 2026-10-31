package local

import (
	"github.com/thtpark/fdn/internal/domain"
	"golang.org/x/sys/unix"
)

func collectEnvironmentSystem(o *domain.EnvironmentObservation) {
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"kern.osproductversion", &o.System.OSVersion}, {"kern.osrelease", &o.System.Kernel},
		{"machdep.cpu.brand_string", &o.CPU.Model},
	} {
		value, err := unix.Sysctl(field.name)
		if err != nil {
			environmentProblem(o, field.name, "os-observation-unavailable")
		} else {
			*field.target = value
		}
	}
	physical, err := unix.SysctlUint32("hw.physicalcpu")
	if err != nil {
		environmentProblem(o, "physical-cpu", "os-observation-unavailable")
	} else {
		value := uint64(physical)
		o.CPU.PhysicalCores = &value
	}
	free, err := unix.SysctlUint64("vm.page_free_count")
	page, pageErr := unix.SysctlUint64("vm.pagesize")
	if err != nil || pageErr != nil {
		environmentProblem(o, "memory-free", "os-observation-unavailable")
	} else {
		value := free * page
		o.Memory.FreeBytes = &value
	}
}
