//go:build stress

package stress_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
)

func op() domain.Command { return domain.Command{OperationID: domain.NewID("stress")} }

func TestThousandTasksContentionPaginationAndThreeHistoryBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := time.Now()
	store, err := foundation.Open(ctx, filepath.Join(t.TempDir(), "state", "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := domain.NewToken()
	if _, err := store.Bootstrap(ctx, domain.Hash(token), time.Now().Unix()+600); err != nil {
		t.Fatal(err)
	}
	actor, err := store.Enroll(ctx, domain.EnrollRequest{Command: op(), InvitationToken: token, Name: "stress Founder", CredentialHash: domain.Hash(domain.NewToken())})
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actor.ID, domain.ProjectRequest{Command: op(), Name: "measured local workload"})
	if err != nil {
		t.Fatal(err)
	}
	var target domain.Task
	const records = 1000
	for i := range records {
		task, err := store.CreateTask(ctx, actor.ID, domain.TaskRequest{Command: op(), ProjectID: project.ID, Title: fmt.Sprintf("task %04d", i), Description: "bounded test specification", AcceptanceCriteria: "measured local checks"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			target = task
		}
	}
	count := 0
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := store.Tasks(ctx, actor.ID, domain.ListQuery{ProjectID: project.ID, Limit: 137, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Items {
			if seen[task.ID] {
				t.Fatal("duplicate paginated task")
			}
			seen[task.ID] = true
			count++
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if count != records {
		t.Fatal("missing tasks", count)
	}
	const writers, updates = 24, 8
	var wg sync.WaitGroup
	var accepted, conflicts atomic.Int64
	for writer := range writers {
		wg.Go(func() {
			for iteration := range updates {
				for {
					current, err := store.Task(ctx, actor.ID, target.ID)
					if err != nil {
						t.Error(err)
						return
					}
					title := fmt.Sprintf("writer %d update %d", writer, iteration)
					req := domain.TaskChange{Command: op(), Kind: "edit", Reason: "optimistic concurrent edit", Title: &title}
					req.ExpectedRevision = current.Revision
					if _, err := store.ChangeTask(ctx, actor.ID, target.ID, req); err != nil {
						if domain.AsFault(err).Code == domain.Conflict {
							conflicts.Add(1)
							continue
						}
						t.Error(err)
						return
					}
					accepted.Add(1)
					break
				}
			}
		})
	}
	wg.Wait()
	if accepted.Load() != writers*updates {
		t.Fatal("lost accepted writes", accepted.Load())
	}
	final, err := store.Task(ctx, actor.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Revision != 1+writers*updates {
		t.Fatal("revision count does not match durable writes", final.Revision)
	}
	history, err := store.Histories(ctx, actor.ID, target.ID)
	if err != nil || len(history) != domain.HistoryLimit {
		t.Fatal("history bound", len(history), err)
	}
	if err := store.Check(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("tasks=%d concurrent_writers=%d accepted_edits=%d conflicts=%d retained_histories=%d elapsed=%s", records, writers, accepted.Load(), conflicts.Load(), len(history), time.Since(start).Round(time.Millisecond))
}
