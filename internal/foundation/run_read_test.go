package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestRunReadReturnsExactRunWithoutChangingTaskOrOwnership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command:        domain.Command{OperationID: domain.NewID("claim"), ExpectedRevision: task.Revision},
		SpecGeneration: task.SpecGeneration, TokenHash: domain.Hash(domain.NewToken()), Provider: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{f.founder.ID, f.worker.ID, f.other.ID} {
		got, err := f.s.Run(ctx, actor, run.ID)
		if err != nil || !reflect.DeepEqual(got, run) {
			t.Fatal("exact shared run lookup failed", got, err)
		}
	}
	after, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("run lookup changed task or active ownership", before, after, err)
	}
	if _, err := f.s.Run(ctx, f.founder.ID, domain.NewID("run")); domain.AsFault(err).Code != domain.NotFound {
		t.Fatal("unknown run did not fail explicitly", err)
	}
	if _, err := f.s.Run(ctx, "agent_unknown", run.ID); err == nil {
		t.Fatal("unauthenticated actor read a run")
	}
}
