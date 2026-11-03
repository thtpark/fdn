package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestSharingLiveOutputIsVisibleBeforeCompletionAndKeepsTheExactBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	output, err := newInvocationOutput(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := output.Write([]byte("live")); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	visible, err := os.ReadFile(path)
	if err != nil || string(visible) != "live" {
		t.Fatal("sharing used an empty or completion-only output file", string(visible), err)
	}
	if _, err := output.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	data, err := output.finish()
	if err == nil || domain.AsFault(err).Code != domain.Provider || len(data) != 0 {
		t.Fatal("stdout overflow became a valid provider result", data, err)
	}
	retained, readErr := os.ReadFile(path)
	if readErr != nil || string(retained) != "live1234" {
		t.Fatal("live log exceeded or lost its exact byte limit", string(retained), readErr)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("live output escaped owner-only permissions", err)
	}
	if _, err := newInvocationOutput(path, 8); !errors.Is(err, os.ErrExist) {
		t.Fatal("invocation output was overwritten", err)
	}
}
