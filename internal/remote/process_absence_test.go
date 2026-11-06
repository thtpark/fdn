package remote

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestOwnedProcessGroupAbsenceDoesNotAdoptAChangedPID(t *testing.T) {
	original := ownedProcess{PID: 12345, Group: 12345, UID: uint32(os.Geteuid()), Started: "original_start"}
	changed := original
	changed.Started = "different_start"
	for _, groupErr := range []error{syscall.ESRCH, nil, syscall.EPERM, syscall.EIO} {
		label := "present"
		if groupErr != nil {
			label = groupErr.Error()
		}
		t.Run(label, func(t *testing.T) {
			inspected, probed := 0, 0
			stopped, err := original.stoppedWithProbe(func(pid int) (*ownedProcess, error) {
				inspected++
				if pid != original.PID {
					t.Fatal("inspection left the exact recorded identity")
				}
				return &changed, nil
			}, func(group int) error {
				probed++
				if group != original.Group {
					t.Fatal("probe left the exact recorded process group")
				}
				return groupErr
			})
			expectedProbes := 1
			if groupErr == nil || errors.Is(groupErr, syscall.EPERM) {
				expectedProbes = 2
			}
			if probed != expectedProbes {
				t.Fatal("cleanup did not probe exact kernel group existence", probed)
			}
			switch {
			case errors.Is(groupErr, syscall.ESRCH):
				if !stopped || err != nil || inspected != 0 {
					t.Fatal("a proved absent old group was stranded by an unrelated PID", stopped, err, inspected)
				}
			case errors.Is(groupErr, syscall.EIO):
				if stopped || !errors.Is(err, syscall.EIO) {
					t.Fatal("kernel inspection failure was mistaken for proof", stopped, err)
				}
			default:
				if stopped || err == nil || domain.AsFault(err).Code != domain.Conflict || inspected != 1 {
					t.Fatal("changed live identity or permission failure was adopted as owned cleanup", stopped, err, inspected)
				}
			}
		})
	}
}

func TestOwnedProcessGroupDisappearanceDuringInspectionRequiresFreshKernelProof(t *testing.T) {
	original := ownedProcess{PID: 12345, Group: 12345, UID: uint32(os.Geteuid()), Started: "original_start"}
	changedStart, changedGroup, changedUID := original, original, original
	changedStart.Started = "different_start"
	changedGroup.Group++
	changedUID.UID++
	inspectionErr := domain.Fail(domain.Conflict, "kernel process changed during ownership inspection")
	for _, inspection := range []struct {
		name    string
		current *ownedProcess
		err     error
	}{
		{"inspection-race", nil, inspectionErr},
		{"changed-start", &changedStart, nil},
		{"changed-group", &changedGroup, nil},
		{"changed-owner", &changedUID, nil},
	} {
		for _, initialErr := range []error{nil, syscall.EPERM} {
			for _, afterErr := range []error{syscall.ESRCH, nil, syscall.EPERM, syscall.EIO} {
				t.Run(fmt.Sprintf("%s/initial-%v/after-%v", inspection.name, initialErr, afterErr), func(t *testing.T) {
					inspected, probed := 0, 0
					stopped, err := original.stoppedWithProbe(func(pid int) (*ownedProcess, error) {
						inspected++
						if pid != original.PID {
							t.Fatal("inspection left the original recorded PID")
						}
						return inspection.current, inspection.err
					}, func(group int) error {
						probed++
						if group != original.Group {
							t.Fatal("inspection adopted a different process group")
						}
						if probed == 1 {
							return initialErr
						}
						return afterErr
					})
					if inspected != 1 || probed != 2 {
						t.Fatal("cleanup did not recheck the exact old group after the snapshot race", inspected, probed)
					}
					if errors.Is(afterErr, syscall.ESRCH) {
						if !stopped || err != nil {
							t.Fatal("proved old-group disappearance remained fenced", stopped, err)
						}
						return
					}
					if stopped || err == nil || domain.AsFault(err).Code != domain.Conflict {
						t.Fatal("changed live identity was adopted without exact kernel disappearance", stopped, err)
					}
					for _, cause := range []error{initialErr, afterErr, inspection.err} {
						if cause != nil && !errors.Is(err, cause) {
							t.Fatal("cleanup concealed an inspection or permission failure", cause, err)
						}
					}
				})
			}
		}
	}
}
