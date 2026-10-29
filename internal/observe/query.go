package observe

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

type Query struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Project  string `json:"project,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Status   string `json:"status,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	Urgent   bool   `json:"urgent,omitempty"`
	Folder   string `json:"folder,omitempty"`
	State    string `json:"state,omitempty"`
	Peer     string `json:"peer,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

var kinds = strings.Fields("version help status identity work agents agent projects project tasks task taskHistory runs discussion entry inbox message messageHistory thread threadPage messageDiagnostics mentions note noteHistory noteContext knowledge knowledgeItem knowledgeHistory workspaces inventory inventoryRefresh preferences provider sessions session grants grant screens devices diagnostics operation upgrade configuration guides guide doctor events")

func (q Query) Validate() error {
	if !slices.Contains(kinds, q.Kind) || q.Limit < 0 || q.Limit > 200 || len(q.Cursor) > 512 {
		return domain.Fail(domain.Invalid, "unsupported observer query or page budget")
	}
	for _, value := range []string{q.ID, q.Project, q.Assignee, q.Peer} {
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") || strings.HasPrefix(value, "-") {
			return domain.Fail(domain.Invalid, "invalid observer query target")
		}
	}
	return nil
}

func (q Query) Key() string {
	data, _ := json.Marshal(q)
	return domain.Hash(string(data))
}

func (q Query) Subscribable() bool {
	return q.Kind != "provider" && q.Kind != "inventoryRefresh" && q.Kind != "doctor"
}

func (q Query) SampleInterval() time.Duration {
	switch q.Kind {
	case "work", "knowledge", "knowledgeItem", "knowledgeHistory", "workspaces", "inventory", "preferences", "configuration", "screens", "upgrade", "diagnostics", "messageDiagnostics":
		return 5 * time.Second
	case "agents", "agent":
		return 30 * time.Second
	default:
		return 0
	}
}

func (q Query) Matches(u domain.Update, actor string) bool {
	switch q.Kind {
	case "status", "identity":
		return u.SourceID == actor && (u.Topic == "agent" || u.Topic == "agent-mode" || u.Topic == "agent-status")
	case "agents":
		return u.Topic == "agent" || u.Topic == "agent-mode" || u.Topic == "agent-status"
	case "agent", "diagnostics":
		return u.SourceID == q.ID
	case "projects":
		return u.Topic == "project" || u.Topic == "project-creation"
	case "project":
		return u.SourceID == q.ID && u.Topic == "project"
	case "tasks":
		return u.Topic == "task" && (q.Project == "" || q.Project == u.ProjectID)
	case "task", "taskHistory":
		return u.TaskID == q.ID && slices.Contains([]string{"task", "run", "comment", "worklog", "attention"}, u.Topic)
	case "runs":
		return u.TaskID == q.ID && (u.Topic == "run" || u.Topic == "task")
	case "discussion":
		return u.TaskID == q.ID && (u.Topic == "comment" || u.Topic == "worklog")
	case "entry":
		return u.SourceID == q.ID && (u.Topic == "comment" || u.Topic == "worklog")
	case "inbox", "messageHistory", "thread", "threadPage":
		return u.Topic == "message"
	case "message", "messageDiagnostics", "operation":
		return u.SourceID == q.ID
	case "mentions":
		return u.Topic == "attention"
	case "note", "noteHistory":
		id, err := domain.NoteID(q.Scope, q.ID)
		return err == nil && u.SourceID == id && u.Topic == "note"
	case "noteContext":
		return u.Topic == "note"
	case "sessions", "session":
		return u.Topic == "session" && (q.ID == "" || u.SourceID == q.ID)
	case "grants", "grant":
		return u.Topic == "assistance" && (q.ID == "" || u.SourceID == q.ID)
	case "events":
		return (q.Project == "" || q.Project == u.ProjectID) && (q.ID == "" || q.ID == u.TaskID)
	case "work":
		return u.SourceID == actor || u.Topic == "session" || u.Topic == "agent-operation"
	default:
		return false
	}
}

func Filters(queries []Query, actor string) (domain.UpdateFilter, domain.UpdateFilter, error) {
	private := domain.UpdateFilter{Self: true}
	var public domain.UpdateFilter
	allPublic := false
	for _, q := range queries {
		switch q.Kind {
		case "status", "identity", "work":
		case "inbox", "messageHistory", "thread", "threadPage":
			private.Topics = append(private.Topics, "message")
		case "message", "messageDiagnostics", "operation":
			private.Sources = append(private.Sources, q.ID)
		case "sessions", "session":
			private.Topics = append(private.Topics, "session")
		case "grants", "grant":
			private.Topics = append(private.Topics, "assistance")
		case "mentions":
			private.Topics = append(private.Topics, "attention")
		case "task", "taskHistory", "discussion", "runs":
			public.Tasks = append(public.Tasks, q.ID)
		case "entry":
			public.Sources = append(public.Sources, q.ID)
		case "tasks":
			if q.Project != "" {
				public.Projects = append(public.Projects, q.Project)
			} else {
				public.Topics = append(public.Topics, "task")
			}
		case "agents":
			public.Topics = append(public.Topics, "agent", "agent-mode", "agent-status")
		case "agent", "diagnostics":
			public.Sources = append(public.Sources, q.ID)
		case "projects":
			public.Topics = append(public.Topics, "project", "project-creation")
		case "project":
			public.Projects = append(public.Projects, q.ID)
		case "note", "noteHistory":
			id, err := domain.NoteID(q.Scope, q.ID)
			if err != nil {
				return private, public, err
			}
			public.Sources = append(public.Sources, id)
		case "noteContext":
			public.Topics = append(public.Topics, "note")
		case "events":
			if q.Project != "" {
				public.Projects = append(public.Projects, q.Project)
			} else if q.ID != "" {
				public.Tasks = append(public.Tasks, q.ID)
			} else {
				allPublic = true
			}
		}
	}
	var err error
	private, err = private.Normalize()
	if err != nil {
		return private, public, err
	}
	if allPublic {
		// An explicit public activity browser requests every public topic.
		public = domain.UpdateFilter{Topics: strings.Fields("task run comment worklog project agent agent-mode agent-status note project-creation")}
	}
	public, err = public.Normalize()
	return private, public, err
}
