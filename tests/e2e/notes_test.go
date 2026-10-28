//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/work"
)

func TestNativeScopedNotesPersistAndReachBothProviderTaskPrompts(t *testing.T) {
	for _, provider := range []string{"codex", "copilot"} {
		t.Run(provider, func(t *testing.T) {
			l := newLab(t, provider)
			task := l.approved(t, "success")
			set := func(w *worker, scope, target, content string, revision int64) domain.Note {
				t.Helper()
				raw := l.cli(t, w, []string{"note", "set", "--input", "-"}, domain.NoteRequest{
					Command: domain.Command{ExpectedRevision: revision}, Scope: scope, TargetID: target,
					Content: &content, Reason: "Publish intentional scoped task guidance",
				})
				var note domain.Note
				if err := json.Unmarshal(raw, &note); err != nil {
					t.Fatal(err)
				}
				return note
			}
			company := set(l.founder, domain.CompanyNotes, "", "# Mission\nDeliver reliable software.", 0)
			project := set(l.founder, domain.ProjectNotes, l.project.ID, "# Goals\n[File](docs/checks.md) [Website](https://example.com) [Repo](https://github.com/example/repo)", 0)
			agent := set(l.worker, domain.AgentNotes, "", "# Skills\nCheck exact output bytes.", 0)
			private := local.KnowledgeChange{Kind: "skill", Name: "local-checks", Content: "PRIVATE_LOCAL_SKILL", Selected: true}
			l.cli(t, l.worker, []string{"knowledge", "put", "--input", "-"}, private)
			contextNotes := must[[]domain.Note](t, "agent-cli", []string{"--config", l.worker.configFile, "note", "context", "--project", l.project.ID}, nil)
			if !reflect.DeepEqual(contextNotes, []domain.Note{company, project, agent}) {
				t.Fatal("compiled CLI omitted a note or published private knowledge", contextNotes)
			}
			deniedText := "A worker cannot replace the company mission"
			envelope, code, _ := run(t, "agent-cli", []string{"--config", l.worker.configFile, "note", "set", "--input", "-"}, domain.NoteRequest{
				Command: domain.Command{ExpectedRevision: company.Revision}, Scope: domain.CompanyNotes,
				Content: &deniedText, Reason: "Attempt an unauthorized company edit",
			})
			if code != 4 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Denied {
				t.Fatal("compiled CLI bypassed Founder-only company notes", code, envelope)
			}
			for range 2 {
				must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			}
			updatedCompany := set(l.founder, domain.CompanyNotes, "", "# Updated mission\nRetain exact verification evidence.", company.Revision)
			must[any](t, "agent-cli", []string{"--config", l.worker.configFile, "work", "run"}, nil)
			if current := l.task(t, task.ID); current.Status != domain.Completed || current.Result == nil {
				t.Fatal("notes did not preserve execution versus verification", current.Status)
			}
			history := must[[]domain.History](t, "agent-cli", []string{"--config", l.worker.configFile, "note", "history", "company"}, nil)
			if len(history) != 2 || history[0].ActorID != l.founder.agent.ID {
				t.Fatal("notes did not persist across separate CLI processes", history)
			}
			state, err := local.Open(context.Background(), l.worker.config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			journals, err := state.Journals(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			phases := map[string]bool{}
			for _, journal := range journals {
				if journal.Kind != "onboarding" && journal.TaskID != task.ID {
					continue
				}
				var launch work.Launch
				if err := json.Unmarshal(journal.Context, &launch); err != nil {
					t.Fatal(err)
				}
				want := []domain.Note{company, project, agent}
				if launch.Phase == "onboarding" {
					want = []domain.Note{company, agent}
				} else if launch.Phase == domain.ExecutionRun {
					want = []domain.Note{updatedCompany, project, agent}
				}
				if !reflect.DeepEqual(launch.SharedNotes, want) || len(launch.Knowledge) != 1 || launch.Knowledge[0].Content != private.Content {
					t.Fatal("actual provider prompt lost its scoped snapshot or private knowledge separation", launch.Phase, launch.SharedNotes)
				}
				phases[launch.Phase] = true
			}
			if !phases["onboarding"] || !phases["preflight"] || !phases[domain.ExecutionRun] {
				t.Fatal("compiled provider path did not exercise the expected task calls", phases)
			}
		})
	}
}
