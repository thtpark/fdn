package work

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

var conversationTaskID = regexp.MustCompile(`\btask_[0-9a-f]{32}\b`)

func (e *Engine) conversationContext(ctx context.Context, launch *Launch) error {
	body, author := "", ""
	if source := launch.Message; source != nil {
		body, author = source.Body, source.SenderID
		page, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, "GET", api.Path("messages", source.ID, "thread-page")+"?limit=20", nil)
		if err != nil {
			return err
		}
		root := *source
		if source.ThreadID != "" && source.ThreadID != source.ID {
			root, err = api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", source.ThreadID), nil)
			if err != nil {
				return err
			}
		}
		launch.Thread, launch.ThreadCursor = []domain.Message{root}, page.NextCursor
		seen, size := map[string]bool{root.ID: true}, len(root.Body)
		if source.ID != root.ID {
			launch.Thread = append(launch.Thread, *source)
			seen[source.ID], size = true, size+len(source.Body)
		}
		parent := source.ReplyToID
		if parent != "" && !seen[parent] {
			var ancestor domain.Message
			cached := false
			for _, message := range page.Items {
				if message.ID == parent {
					ancestor, cached = message, true
					break
				}
			}
			if !cached {
				ancestor, err = api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", parent), nil)
				if err != nil {
					return err
				}
			}
			seen[ancestor.ID], size = true, size+len(ancestor.Body)
			launch.Thread = append(launch.Thread, ancestor)
		}
		for _, message := range page.Items {
			if seen[message.ID] {
				continue
			}
			if size+len(message.Body) > 128<<10 {
				launch.ContextIncomplete = true
				continue
			}
			seen[message.ID], size = true, size+len(message.Body)
			launch.Thread = append(launch.Thread, message)
		}
		launch.ContextIncomplete = launch.ContextIncomplete || page.NextCursor != ""
		slices.SortStableFunc(launch.Thread, func(a, b domain.Message) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
	}
	if item := launch.Attention; item != nil {
		source, err := api.Call[domain.Entry](ctx, e.Client, "GET", api.Path("entries", item.EntryID), nil)
		if err != nil {
			return err
		}
		if launch.Task == nil || source.Generation != item.Generation || !domain.RelevantComment(source, *launch.Task, launch.AgentID) {
			return domain.Fail(domain.Conflict, "comment no longer targets this assignee or mentioned actor")
		}
		body, author = source.Body, source.AuthorID
		if !slices.ContainsFunc(launch.Discussion, func(entry domain.Entry) bool { return entry.ID == source.ID }) {
			launch.Discussion = append(launch.Discussion, source)
		}
		parent := source.ParentID
		size := len(source.Body)
		for depth := 0; parent != ""; depth++ {
			if depth == 8 {
				launch.ContextIncomplete = true
				break
			}
			entry, err := api.Call[domain.Entry](ctx, e.Client, "GET", api.Path("entries", parent), nil)
			if err != nil {
				return err
			}
			if entry.TaskID != source.TaskID {
				return domain.Fail(domain.Conflict, "comment ancestor belongs to another task")
			}
			if size+len(entry.Body) > 128<<10 {
				launch.ContextIncomplete = true
				break
			}
			size += len(entry.Body)
			if !slices.ContainsFunc(launch.Discussion, func(value domain.Entry) bool { return value.ID == entry.ID }) {
				launch.Discussion = append(launch.Discussion, entry)
			}
			parent = entry.ParentID
		}
	}
	if author != "" {
		requester, err := api.Call[domain.Agent](ctx, e.Client, "GET", api.Path("agents", author), nil)
		if err != nil {
			return err
		}
		launch.Requester = &requester
	}
	for _, id := range conversationTaskID.FindAllString(body, -1) {
		if (launch.Task != nil && launch.Task.ID == id) || slices.ContainsFunc(launch.ReferencedTasks, func(task domain.Task) bool { return task.ID == id }) {
			continue
		}
		if len(launch.ReferencedTasks) == 4 {
			launch.ContextIncomplete = true
			break
		}
		task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", id), nil)
		if err != nil {
			return err
		}
		project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", task.ProjectID), nil)
		if err != nil {
			return err
		}
		launch.ReferencedTasks = append(launch.ReferencedTasks, task)
		if !slices.ContainsFunc(launch.ReferencedProjects, func(value domain.Project) bool { return value.ID == project.ID }) {
			launch.ReferencedProjects = append(launch.ReferencedProjects, project)
		}
	}
	return nil
}

func conversationBody(reply provider.Reply) (string, error) {
	body := reply.Summary
	if len(reply.Evidence) != 0 {
		body += "\n\nContext and checks:\n- " + strings.Join(reply.Evidence, "\n- ")
	}
	for _, question := range reply.Questions {
		body += "\n\n" + question.Body
	}
	if err := domain.CheckText(body, "conversation result", 32768, true); err != nil {
		return "", err
	}
	return body, nil
}

func (e *Engine) sendConversationReply(ctx context.Context, operation string, source domain.Message, reply provider.Reply) error {
	var retained proposal
	found, err := e.State.Get(ctx, "proposal:"+operation, &retained)
	if err != nil {
		return err
	}
	request := domain.MessageRequest{
		Command: domain.Command{OperationID: operation}, RecipientID: source.SenderID,
		Origin: "ai", Kind: "direct", ReplyToID: source.ID, ResponseRequest: reply.RequestsResponse(),
	}
	if found {
		if retained.Method != "POST" || retained.Path != "/v1/messages" {
			return domain.Fail(domain.Conflict, "retained conversation operation belongs to another command")
		}
		if err := json.Unmarshal(retained.Body, &request); err != nil {
			return err
		}
		if request.OperationID != operation || request.RecipientID != source.SenderID || request.ReplyToID != source.ID {
			return domain.Fail(domain.Conflict, "retained reply belongs to another source or recipient")
		}
	} else {
		request.Body, err = conversationBody(reply)
		if err != nil {
			return err
		}
	}
	_, err = commit[domain.Message](ctx, e, "POST", "/v1/messages", request)
	return err
}
