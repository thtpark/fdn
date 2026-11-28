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
var conversationFileID = regexp.MustCompile(`\bfile_[0-9a-f]{32}\b`)

func (e *Engine) conversationContext(ctx context.Context, launch *Launch) error {
	body, author := "", ""
	if source := launch.Message; source != nil {
		body, author = source.Body, source.SenderID
		if source.Announcement != nil {
			authorized, err := api.Call[map[string]bool](ctx, e.Client, "GET", api.Path("messages", source.ID, "announcement-authority"), nil)
			if err != nil {
				return err
			}
			if !authorized["authorized"] {
				return domain.Fail(domain.Denied, "announcement authority is no longer current")
			}
			if source.Announcement.ProjectID != "" {
				project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", source.Announcement.ProjectID), nil)
				if err != nil {
					return err
				}
				launch.ReferencedProjects = append(launch.ReferencedProjects, project)
			}
		}
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
		size, err := messageContextSize(root)
		if err != nil {
			return err
		}
		seen := map[string]bool{root.ID: true}
		if source.ID != root.ID {
			launch.Thread = append(launch.Thread, *source)
			sourceSize, err := messageContextSize(*source)
			if err != nil {
				return err
			}
			seen[source.ID], size = true, size+sourceSize
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
			ancestorSize, err := messageContextSize(ancestor)
			if err != nil {
				return err
			}
			seen[ancestor.ID], size = true, size+ancestorSize
			launch.Thread = append(launch.Thread, ancestor)
		}
		for _, message := range page.Items {
			if seen[message.ID] {
				continue
			}
			messageSize, err := messageContextSize(message)
			if err != nil {
				return err
			}
			if size+messageSize > 128<<10 {
				launch.ContextIncomplete = true
				continue
			}
			seen[message.ID], size = true, size+messageSize
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
	if source := launch.RoomMessage; source != nil {
		body, author = source.Body, source.SenderID
		room, err := api.Call[domain.MessageRoom](ctx, e.Client, "GET", api.Path("rooms", source.RoomID), nil)
		if err != nil {
			return err
		}
		launch.Room = &room
		if room.ProjectID != "" {
			project, err := api.Call[domain.Project](ctx, e.Client, "GET", api.Path("projects", room.ProjectID), nil)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(launch.ReferencedProjects, func(value domain.Project) bool { return value.ID == project.ID }) {
				launch.ReferencedProjects = append(launch.ReferencedProjects, project)
			}
		}
		agents, err := api.Call[[]domain.Agent](ctx, e.Client, "GET", "/v1/agents", nil)
		if err != nil {
			return err
		}
		members := make([]domain.Agent, 0, len(room.MemberIDs))
		for _, agent := range agents {
			if slices.Contains(room.MemberIDs, agent.ID) {
				members = append(members, agent)
			}
		}
		launch.RoomMembers = members
		page, err := api.Call[domain.Page[domain.RoomMessage]](ctx, e.Client, "GET", api.Path("rooms", room.ID, "messages")+"?limit=30", nil)
		if err != nil {
			return err
		}
		history := []domain.RoomMessage{*source}
		size, err := roomMessageContextSize(*source)
		if err != nil {
			return err
		}
		seen := map[string]bool{source.ID: true}
		if source.ReplyToID != "" && !seen[source.ReplyToID] {
			var quoted domain.RoomMessage
			cached := false
			for _, item := range page.Items {
				if item.ID == source.ReplyToID {
					quoted, cached = item, true
					break
				}
			}
			if !cached {
				quoted, err = api.Call[domain.RoomMessage](ctx, e.Client, "GET", api.Path("room-messages", source.ReplyToID), nil)
				if err != nil {
					return err
				}
			}
			qSize, err := roomMessageContextSize(quoted)
			if err != nil {
				return err
			}
			seen[quoted.ID], size = true, size+qSize
			history = append(history, quoted)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				continue
			}
			mSize, err := roomMessageContextSize(item)
			if err != nil {
				return err
			}
			if size+mSize > 128<<10 {
				launch.ContextIncomplete = true
				continue
			}
			seen[item.ID], size = true, size+mSize
			history = append(history, item)
		}
		launch.ContextIncomplete = launch.ContextIncomplete || page.NextCursor != ""
		slices.SortStableFunc(history, func(a, b domain.RoomMessage) int {
			if a.CreatedAt < b.CreatedAt {
				return -1
			}
			if a.CreatedAt > b.CreatedAt {
				return 1
			}
			return strings.Compare(a.ID, b.ID)
		})
		launch.RoomHistory, launch.RoomHistoryCursor = history, page.NextCursor
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
		size, err := entryContextSize(source)
		if err != nil {
			return err
		}
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
			entrySize, err := entryContextSize(entry)
			if err != nil {
				return err
			}
			if size+entrySize > 128<<10 {
				launch.ContextIncomplete = true
				break
			}
			size += entrySize
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
	contextBodies := []string{body}
	for _, message := range launch.Thread {
		contextBodies = append(contextBodies, message.Body)
	}
	for _, message := range launch.RoomHistory {
		contextBodies = append(contextBodies, message.Body)
	}
	for _, id := range conversationTaskID.FindAllString(strings.Join(contextBodies, "\n"), -1) {
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

func (e *Engine) sendRoomConversationReply(ctx context.Context, operation string, source domain.RoomMessage, reply provider.Reply) error {
	var retained proposal
	found, err := e.State.Get(ctx, "proposal:"+operation, &retained)
	if err != nil {
		return err
	}
	respReq := reply.RequestsResponse()
	request := domain.RoomMessageRequest{
		Command:         domain.Command{OperationID: operation},
		Origin:          "ai",
		ReplyToID:       source.ID,
		ResponseRequest: &respReq,
	}
	targetPath := api.Path("rooms", source.RoomID, "messages")
	if found {
		if retained.Method != "POST" || retained.Path != targetPath {
			return domain.Fail(domain.Conflict, "retained room conversation operation belongs to another command")
		}
		if err := json.Unmarshal(retained.Body, &request); err != nil {
			return err
		}
		if request.OperationID != operation || request.ReplyToID != source.ID {
			return domain.Fail(domain.Conflict, "retained room reply belongs to another source")
		}
	} else {
		request.Body, err = conversationBody(reply)
		if err != nil {
			return err
		}
		actor := e.Client.Credential.AgentID
		var attachments []string
		for _, id := range conversationFileID.FindAllString(request.Body, -1) {
			if slices.Contains(attachments, id) || len(attachments) >= domain.MaxAttachments {
				continue
			}
			file, fileErr := api.Call[domain.SharedFile](ctx, e.Client, "GET", api.Path("files", id), nil)
			if fileErr == nil && file.State == "ready" && (file.OwnerID == actor || file.TeamReadable) && file.TransferTo == "" {
				attachments = append(attachments, id)
			}
		}
		if len(attachments) != 0 {
			request.AttachmentIDs = attachments
		}
	}
	_, err = commit[domain.RoomMessage](ctx, e, "POST", targetPath, request)
	return err
}
