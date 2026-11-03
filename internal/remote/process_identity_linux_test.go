package remote

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestLinuxProcessInspectionHandlesExitDuringProcReadWithoutHidingOtherErrors(t *testing.T) {
	for _, stage := range []string{"stat", "read"} {
		for _, cause := range []error{os.ErrNotExist, syscall.ESRCH, syscall.EPERM, syscall.EIO} {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				stat := os.Stat
				readFile := os.ReadFile
				failure := &os.PathError{Op: stage, Path: "/proc/fixture/stat", Err: cause}
				if stage == "stat" {
					stat = func(string) (os.FileInfo, error) { return nil, failure }
				} else {
					readFile = func(string) ([]byte, error) { return nil, failure }
				}
				process, parent, err := inspectProcProcess(os.Getpid(), stat, readFile)
				if errors.Is(cause, os.ErrNotExist) || errors.Is(cause, syscall.ESRCH) {
					if process != nil || parent != 0 || err != nil {
						t.Fatal("an exited process was not reported absent", process, parent, err)
					}
				} else if process != nil || parent != 0 || !errors.Is(err, cause) {
					t.Fatal("a kernel inspection failure was hidden", process, parent, err)
				}
			})
		}
	}
}
