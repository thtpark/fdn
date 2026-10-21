package identityservice

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

func TestStoppedRecoveryUsesNewPrivateStateWithoutDeletingOldKeysOrRunnerJournal(t *testing.T) {
	f := newFixture(t)
	before, err := secure.Read(filepath.Join(f.config.StateDir, deviceFile), domain.MaxBody)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(f.root, "runner-journal")
	if err := os.WriteFile(journal, []byte("retained fenced work journal"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(f.config.StateDir, "recovered-state")
	output := filepath.Join(f.root, "public", "recovery.json")
	public, err := Prepare(replacement, f.public.Name, output, f.options)
	if err != nil || public.RequestID == f.public.RequestID || public.SSHKey == f.public.SSHKey || public.AuthKey == f.public.AuthKey {
		t.Fatal("explicit new private StateDir did not prepare a fresh recovery request", err)
	}
	after, err := secure.Read(filepath.Join(f.config.StateDir, deviceFile), domain.MaxBody)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recovery altered the old identity key evidence", err)
	}
	data, err := os.ReadFile(journal)
	if err != nil || string(data) != "retained fenced work journal" {
		t.Fatal("private recovery reset runner work evidence", err)
	}
	if _, err := Prepare(f.config.StateDir, f.public.Name, filepath.Join(f.root, "public", "old-retry.json"), f.options); err != nil || f.crypto.generated.Load() != 2 {
		t.Fatal("normal old-state retry rotated retained keys", err)
	}
	if _, err := Prepare(replacement, f.public.Name, output, f.options); err != nil || f.crypto.generated.Load() != 2 {
		t.Fatal("recovery preparation did not remain an exact retry", err)
	}
}
