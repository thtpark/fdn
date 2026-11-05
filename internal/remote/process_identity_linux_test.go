package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type changedProcfsOwner struct{ os.FileInfo }

func (info changedProcfsOwner) Sys() any {
	stat := *info.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid = uint32(os.Geteuid()) + 1
	return &stat
}

func TestLinuxProcessIdentityUsesKernelEffectiveUIDInsteadOfProcfsOwner(t *testing.T) {
	pid := os.Getpid()
	process, _, err := inspectProcProcess(pid, func(path string) (os.FileInfo, error) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		return changedProcfsOwner{info}, nil
	}, os.ReadFile)
	if err != nil || process == nil || process.UID != uint32(os.Geteuid()) {
		t.Fatal("procfs ownership change was mistaken for a different process", process, err)
	}
}

func TestLinuxProcessIdentityRejectsReplacementBetweenUIDAndStartRechecks(t *testing.T) {
	reads := 0
	process, _, err := inspectProcProcess(os.Getpid(), os.Stat, func(path string) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) != "stat" {
			return data, nil
		}
		reads++
		if reads == 1 {
			return data, nil
		}
		end := strings.LastIndexByte(string(data), ')')
		fields := strings.Fields(string(data[end+1:]))
		fields[19] = "replaced_start"
		return []byte(string(data[:end+1]) + " " + strings.Join(fields, " ")), nil
	})
	if err == nil || process != nil {
		t.Fatal("PID replacement during effective-UID inspection was adopted", process, err)
	}
}

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
