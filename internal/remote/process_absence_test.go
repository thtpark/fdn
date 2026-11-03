package remote

import (
	"errors"
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
			if probed != 1 {
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
