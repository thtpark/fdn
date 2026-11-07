package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicRecipientReusesTheExactRetainedBackupIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "backup.agekey")
	recipient, err := Keygen(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := PublicRecipient(path)
	if err != nil || retained != recipient {
		t.Fatal("private backup identity did not retain its public recipient", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("public recipient lookup replaced a private recovery identity", err)
	}
	if err := os.WriteFile(path, []byte("not an age identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublicRecipient(path); err == nil {
		t.Fatal("invalid recovery identity became a default recipient")
	}
}
