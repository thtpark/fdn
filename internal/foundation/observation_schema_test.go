package foundation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/database"
)

func TestSQL8ObservationIndexesPreserveSQL7MessageBytesAndOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "foundation.sqlite")
	old, err := database.Open(ctx, path, []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema, notesSchema, devicesSchema}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent_sender", "agent_recipient"} {
		if _, err := old.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", id, `{"id":"`+id+`","active":true}`, id); err != nil {
			t.Fatal(err)
		}
	}
	payload := `{"id":"message_retained","sender_id":"agent_sender","recipient_id":"agent_recipient","body":"PRIVATE retained bytes","kind":"direct","state":"handled","created_at":17,"read_at":23,"revision":4}`
	if _, err := old.Exec("INSERT INTO messages(rowid,id,data,sender_id,recipient_id,state,urgent) VALUES(91,'message_retained',?,'agent_sender','agent_recipient','handled',0)", payload); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var row, version int
	var retained string
	if err := store.db.QueryRow("SELECT rowid,data FROM messages WHERE id='message_retained'").Scan(&row, &retained); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 8 || row != 91 || retained != payload {
		t.Fatal("index migration changed data or insertion order", row, version, err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
