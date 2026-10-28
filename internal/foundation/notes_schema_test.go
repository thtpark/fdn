package foundation

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestNotesMigrationPreservesSQL5AndRetainsDocumentsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(securityDirectory(t), "private", "notes.sqlite")
	previous := []string{schema, autonomousSchema, identitySecuritySchema, updatesSchema, messageRequestsSchema}
	db, err := database.Open(ctx, path, previous, 5)
	if err != nil {
		t.Fatal(err)
	}
	founder := domain.Agent{ID: domain.NewID("agent"), Name: "Retained Founder", Founder: true, Active: true, Mode: "manual", Revision: 17}
	data, _ := encode(founder)
	if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", founder.ID, data, domain.Hash(domain.NewToken())); err != nil {
		t.Fatal(err)
	}
	seedUpdateIdentity(t, db, founder)
	project := domain.Project{ID: domain.NewID("project"), Name: "Retained project", OwnerID: founder.ID, Delegates: []string{founder.ID}, Epoch: 7, Revision: 4}
	data, _ = encode(project)
	if _, err := db.Exec("INSERT INTO projects(id,data) VALUES(?,?)", project.ID, data); err != nil {
		t.Fatal(err)
	}
	message := domain.Message{ID: domain.NewID("message"), SenderID: founder.ID, RecipientID: founder.ID,
		Body: "RETAINED_PRIVATE_MESSAGE", State: "handled", ReadAt: 123, Revision: 2, Kind: "direct", Origin: "human"}
	data, _ = encode(message)
	if _, err := db.Exec("INSERT INTO messages(id,data,sender_id,recipient_id,state,urgent) VALUES(?,?,?,?,?,0)", message.ID, data, founder.ID, founder.ID, message.State); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO operations(actor_id,operation_id,fingerprint,result) VALUES(?,?,?,?)", founder.ID, "retained-operation", domain.Hash("exact retained request"), []byte(`{"retained":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM update_changes"); err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for _, table := range []string{"agents", "agent_identities", "projects", "tasks", "runs", "messages", "histories", "operations", "settings", "events", "update_feed", "update_recipients", "update_state"} {
		hashes[table] = securityTableHash(t, db, table)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		s.Close()
		t.Fatal("SQL5 did not advance through the additive notes migration", version, err)
	}
	for table, hash := range hashes {
		if got := securityTableHash(t, s.db, table); got != hash {
			s.Close()
			t.Fatal("notes migration rewrote existing state", table)
		}
	}
	notes, err := s.NoteContext(ctx, founder.ID, project.ID)
	if err != nil || len(notes) != 3 {
		s.Close()
		t.Fatal("fresh notes context was not readable", notes, err)
	}
	for _, note := range notes {
		if note.Revision != 0 || note.Content != "" {
			s.Close()
			t.Fatal("migration invented shared notes", note)
		}
	}
	content := "# Mission\nKeep current identity and state intact."
	req := domain.NoteRequest{Command: op(), Scope: domain.CompanyNotes, Content: &content, Reason: "Explicit post-migration publication"}
	written, err := s.SetNote(ctx, founder.ID, req)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Check(ctx); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if old, err := database.Open(ctx, path, previous, 5); err == nil {
		old.Close()
		t.Fatal("a SQL5 binary accepted a SQL6 database")
	} else {
		assertCode(t, err, domain.Prerequisite)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	shown, err := reopened.Note(ctx, founder.ID, domain.CompanyNotes, "")
	if err != nil || !reflect.DeepEqual(shown, written) {
		t.Fatal("reopen lost the current document", shown, err)
	}
	replayed, err := reopened.SetNote(ctx, founder.ID, req)
	if err != nil || !reflect.DeepEqual(replayed, written) {
		t.Fatal("reopen lost the immutable operation binding", replayed, err)
	}
	histories, err := reopened.Histories(ctx, founder.ID, written.ID)
	if err != nil || len(histories) != 1 {
		t.Fatal("reopen/replay duplicated the notes history", histories, err)
	}
	assertUpdateChangesDrained(t, reopened)
}
