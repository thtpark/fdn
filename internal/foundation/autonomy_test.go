package foundation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

func TestPrebaselineModeStateIsRejectedWithoutAdoption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "foundation.sqlite")
	db, err := database.Open(ctx, path, []string{schema}, 1)
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
	if store != nil {
		store.Close()
		t.Fatal("prebaseline state was adopted")
	}
	assertCode(t, err, domain.Prerequisite)
}

func TestPersistentAssistanceSurvivesStoreReopenAndSessionRetirement(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(root, "db.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	keys := securityPublic(t, "Founder")
	identity, err := store.BootstrapIdentity(ctx, keys.public, op().OperationID)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ = securityActivate(t, store, identity, keys, 4100)
	founder, err := store.Me(ctx, identity.AgentID)
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
