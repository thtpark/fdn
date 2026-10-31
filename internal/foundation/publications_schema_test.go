package foundation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestSQL9PublicationMigrationPreservesSQL8AndRejectsRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "foundation.sqlite")
	shipped := []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, notesSchema, devicesSchema, observationSchema}
	old, err := database.Open(ctx, path, shipped, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent_sender", "agent_receiver"} {
		if _, err := old.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", id, `{"id":"`+id+`","active":true}`, id); err != nil {
			t.Fatal(err)
		}
	}
	payload := `{"id":"message_retained","body":"PRIVATE exact bytes","read_at":23,"revision":7}`
	if _, err := old.Exec("INSERT INTO messages(rowid,id,data,sender_id,recipient_id,state,urgent) VALUES(91,'message_retained',?,'agent_sender','agent_receiver','handled',0)", payload); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version, row int
	var retained string
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 9 {
		t.Fatal("ordered migration did not advance to SQL9", version, err)
	}
	if err := store.db.QueryRow("SELECT rowid,data FROM messages WHERE id='message_retained'").Scan(&row, &retained); err != nil || row != 91 || retained != payload {
		t.Fatal("SQL9 rewrote retained message order/bytes/receipts", row, retained, err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = database.Open(ctx, path, shipped, 5)
	assertCode(t, err, domain.Prerequisite)
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
