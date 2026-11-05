package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeRunReadReturnsExactRunWithoutChangingWork(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	task, err := f.store.CreateTask(ctx, f.founder.ID, domain.TaskRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, ProjectID: f.project.ID,
		Title: "Exact run link", Description: "Synthetic read-only run routing fixture.",
		AcceptanceCriteria: "Read a run by its own ID without changing ownership.",
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := []struct {
		actor string
		input domain.TaskChange
	}{
		{f.founder.ID, domain.TaskChange{Kind: "assign", AgentID: f.target.ID, Provider: "codex"}},
		{f.target.ID, domain.TaskChange{Kind: "workspace", WorkspaceRef: "workspace_fixture"}},
		{f.target.ID, domain.TaskChange{Kind: "status", Status: domain.Todo}},
		{f.target.ID, domain.TaskChange{Kind: "clarity"}},
		{f.founder.ID, domain.TaskChange{Kind: "approve"}},
	}
	for _, change := range changes {
		input := change.input
		input.Command = domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision}
		input.Reason = "Prepare the synthetic fixture"
		if input.Kind == "clarity" || input.Kind == "approve" {
			input.SpecGeneration = task.SpecGeneration
		}
		task, err = f.store.ChangeTask(ctx, change.actor, task.ID, input)
		if err != nil {
			t.Fatal(input.Kind, err)
		}
	}
	run, err := f.store.Claim(ctx, f.target.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, Provider: "codex", TokenHash: domain.Hash(domain.NewToken()),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Task(ctx, f.founder.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := messageFixtureClient(t, f.server, f.delegate.ID)
	got, err := Call[domain.Run](ctx, client, "GET", Path("runs", run.ID), nil)
	if err != nil || !reflect.DeepEqual(got, run) {
		t.Fatal("native run lookup changed the exact run", got, run, err)
	}
	after, err := f.store.Task(ctx, f.founder.ID, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("native run lookup changed work", before, after, err)
	}
	for _, item := range []struct {
		path string
		code domain.Code
	}{
		{Path("runs", domain.NewID("run")), domain.NotFound},
		{Path("runs", run.ID) + "?actor=" + f.target.ID, domain.Invalid},
	} {
		if _, err := Call[domain.Run](ctx, client, "GET", item.path, nil); domain.AsFault(err).Code != item.code {
			t.Fatal("invalid run lookup did not fail explicitly", item.path, err)
		}
	}
}
