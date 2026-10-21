package foundation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestAutonomyMigrationPreservesIdentityAndRejectsUnapprovedModeWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "foundation.sqlite")
	db, err := database.Open(ctx, path, []string{schema})
	if err != nil {
		t.Fatal(err)
	}
	token := domain.NewToken()
	agent := domain.Agent{ID: domain.NewID("agent"), Name: "previous worker", Active: true, CreatedAt: 100, LastSeen: 101}
	data, err := encode(agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO agents(id,data,credential_hash,active) VALUES(?,?,?,1)", agent.ID, data, domain.Hash(token)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Authenticate(ctx, token)
	if err != nil || got.ID != agent.ID || got.Mode != "ai" || got.Revision != 1 || got.CreatedAt != 100 {
		t.Fatal("migration replaced identity or invented manual authority", got, err)
	}
	req := domain.ModeRequest{Command: op(), Mode: "manual", Reason: "self pause"}
	req.ExpectedRevision = got.Revision
	if _, err := store.SetMode(ctx, got.ID, got.ID, req); err == nil {
		t.Fatal("non-Founder requested mode")
	}
}

func TestPersistentAssistanceSurvivesStoreReopenAndSessionRetirement(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(root, "db.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, token := domain.NewToken(), domain.NewToken()
	if _, err := store.Bootstrap(ctx, domain.Hash(bootstrap), time.Now().Unix()+900); err != nil {
		t.Fatal(err)
	}
	founder, err := store.Enroll(ctx, domain.EnrollRequest{Command: op(), InvitationToken: bootstrap, CredentialHash: domain.Hash(token), Name: "Founder"})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateAssistance(ctx, founder.ID, domain.AssistanceGrantRequest{Command: op(), RequesterID: founder.ID, TargetID: founder.ID, Mode: "tmux", Purpose: "authentication", Reason: "authorize once"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RetireSessions(ctx); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Assistance(ctx, founder.ID, grant.ID)
	if err != nil || !retained.Active || retained.Revision != 1 {
		t.Fatal("persistent permission did not survive process/state reopen", retained, err)
	}
	if _, err := store.RequestSession(ctx, founder.ID, domain.SessionRequest{Command: op(), GrantID: grant.ID, TargetID: founder.ID, Mode: "tmux", Purpose: "authentication", Provider: "codex", Reason: "reuse after restart", ExpiresAt: time.Now().Unix() + 90}); err != nil {
		t.Fatal(err)
	}
}
