package foundation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestSharingSQL11PreservesSQL10RecordsAndRejectsRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "foundation.sqlite")
	old, err := database.Open(ctx, path, serverSchemas()[:10], 5)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"agent_retained","name":"Retained","mode":"ai","revision":7,"unrelated":"preserve exact bytes"}`
	if _, err := old.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", "agent_retained", payload, "retained-unused-hash"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := database.Open(ctx, path, serverSchemas(), 5)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var retained string
	if err := current.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 11 {
		t.Fatal("ordered sharing migration did not append SQL11", version, err)
	}
	if err := current.QueryRow("SELECT data FROM agents WHERE id='agent_retained'").Scan(&retained); err != nil || retained != payload {
		t.Fatal("sharing migration rewrote existing records", retained, err)
	}
	var table string
	if err := current.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='sharing'").Scan(&table); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	rollback, err := database.Open(ctx, path, serverSchemas()[:10], 5)
	if rollback != nil {
		rollback.Close()
		t.Fatal("SQL10 binary accepted SQL11 state")
	}
	assertCode(t, err, domain.Prerequisite)
}
