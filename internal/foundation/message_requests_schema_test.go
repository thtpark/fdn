package foundation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestMessageRequestMigrationFivePreservesSQLFourRecordsAndUpdates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "sql-four.sqlite")
	migrations := []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema}
	db, err := database.Open(ctx, path, migrations)
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{ID: domain.NewID("agent"), Name: "Retained Founder", Founder: true, Active: true, Mode: "manual", Revision: 12}
	data, _ := encode(agent)
	if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, domain.Hash(domain.NewToken())); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO settings(key,value) VALUES('instance_id','instance_retained')"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"message_z_first", "message_a_second"} {
		message := domain.Message{ID: id, SenderID: agent.ID, RecipientID: agent.ID,
			Body: "Retained private message", ReadAt: 110, CreatedAt: 103, State: "handled", Revision: 2, Origin: "human", Kind: "direct"}
		data, _ := encode(message)
		if _, err := db.Exec("INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,0)", id, data, agent.ID, agent.ID, message.State); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO operations(actor_id,operation_id,fingerprint,result) VALUES(?,?,?,?)",
		agent.ID, "retained.operation", domain.Hash("retained exact proposal"), []byte(`{"retained":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO update_feed(sequence,topic,source_id,task_id,project_id,urgent,created_at)
VALUES(17,'message','message_z_first','','',0,103)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO update_recipients(actor_id,key_generation,sequence) VALUES(?,0,17)", agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM update_changes"); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, table := range []string{"agents", "messages", "settings", "operations", "update_feed", "update_recipients", "update_state"} {
		before[table] = securityTableHash(t, db, table)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("ordered mailbox migration did not advance to SQL5", version, err)
	}
	for table, hash := range before {
		if got := securityTableHash(t, s.db, table); got != hash {
			t.Fatal("mailbox migration rewrote retained state", table)
		}
	}
	var first string
	if err := s.db.QueryRow("SELECT id FROM messages ORDER BY rowid LIMIT 1").Scan(&first); err != nil || first != "message_z_first" {
		t.Fatal("mailbox migration changed conversation insertion order", first, err)
	}
	for _, parties := range [][2]string{{"agent_unknown", agent.ID}, {agent.ID, "agent_unknown"}, {"instance_retained", agent.ID}, {agent.ID, "instance_retained"}} {
		message := domain.Message{ID: domain.NewID("message"), SenderID: parties[0], RecipientID: parties[1], Kind: "direct", State: "pending"}
		data, _ := encode(message)
		if _, err := s.db.Exec("INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,0)",
			message.ID, data, message.SenderID, message.RecipientID, message.State); err == nil {
			t.Fatal("mailbox migration accepted an unknown party or ordinary Foundation message", parties)
		}
	}
	since := updateWatermark(t, s, agent.ID)
	sent, err := s.SendMessage(ctx, agent.ID, domain.MessageRequest{
		Command: op(), RecipientID: agent.ID, Body: "A new post-migration message.", Kind: "direct", Origin: "human"})
	if err != nil || !updateContains(updateReplay(t, s, agent.ID, since), "message", sent.ID) {
		t.Fatal("mailbox migration lost transactional private update triggers", sent, err)
	}
	assertUpdateChangesDrained(t, s)
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if old, err := database.Open(ctx, path, migrations); err == nil {
		old.Close()
		t.Fatal("an older SQL4 binary accepted the migrated mailbox database")
	} else {
		assertCode(t, err, domain.Prerequisite)
	}
}
