package work

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func (e *Engine) messages(ctx context.Context, c config.Worker, urgent, manual bool) (bool, error) {
	cursor := ""
	for {
		path := "/v1/messages?limit=200"
		if urgent {
			path += "&urgent=true"
		}
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page, err := api.Call[domain.Page[domain.Message]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			return false, err
		}
		for _, message := range page.Items {
			if message.Request != nil {
				if handled, err := e.reconcileMessageAction(ctx, message); handled || err != nil {
					return handled, err
				}
				if message.Request.Action.Kind != "work" || message.Request.State != "queued" {
					continue
				}
				var task *domain.Task
				if message.Request.Action.TaskID != "" {
					value, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", message.Request.Action.TaskID), nil)
					if err != nil {
						return false, err
					}
					task = &value
				}
				launch, err := e.launch(ctx, "request", task)
				if err != nil {
					return false, err
				}
				launch.Message = &message
				if message.ReplyToID != "" {
					parent, err := api.Call[domain.Message](ctx, e.Client, "GET", api.Path("messages", message.ReplyToID), nil)
					if err != nil {
						return false, err
					}
					launch.RequestParent = &parent
				}
				_, err = e.text(ctx, c, launch, manual)
				var fenced textResponseFenced
				if errors.As(err, &fenced) {
					continue
				}
				return true, err
			}
			if message.Kind != "direct" || !message.ResponseRequest {
				continue
			}
			launch, err := e.launch(ctx, "message", nil)
			if err != nil {
				return false, err
			}
			launch.Message = &message
			_, err = e.text(ctx, c, launch, manual)
			var fenced textResponseFenced
			if errors.As(err, &fenced) {
				continue
			}
			return true, err
		}
		cursor = page.NextCursor
		if cursor == "" {
			return false, nil
		}
	}
}

func (e *Engine) attention(ctx context.Context, c config.Worker, manual bool) (bool, error) {
	cursor := ""
	for {
		path := "/v1/attention?limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page, err := api.Call[domain.Page[domain.Attention]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			return false, err
		}
		for _, item := range page.Items {
			task, err := api.Call[domain.Task](ctx, e.Client, "GET", api.Path("tasks", item.TaskID), nil)
			if err != nil {
				return false, err
			}
			launch, err := e.launch(ctx, "attention", &task)
			if err != nil {
				return false, err
			}
			launch.Attention = &item
			_, err = e.text(ctx, c, launch, manual)
			var fenced textResponseFenced
			if errors.As(err, &fenced) {
				continue
			}
			return true, err
		}
		cursor = page.NextCursor
		if cursor == "" {
			return false, nil
		}
	}
}

func (e *Engine) acknowledgeReviewFeedback(ctx context.Context, j *local.Journal, launch Launch, task domain.Task) error {
	if task.Result == nil {
		return nil
	}
	acknowledge := func(entry domain.Entry) error {
		_, err := commit[domain.Attention](ctx, e, "POST", api.Path("attention", entry.ID), domain.AttentionRequest{Command: domain.Command{OperationID: j.ID + ".feedback." + entry.ID}, Generation: entry.Generation, State: "handled"})
		return err
	}
	pending := map[string]domain.Entry{}
	for _, entry := range launch.Discussion {
		if entry.Review == nil || entry.Review.Decision != "rework" || entry.Review.ResultGeneration != task.ResultGeneration || entry.Review.SpecGeneration != task.Result.SpecGeneration || !slices.Contains(entry.Mentions, e.Client.Credential.AgentID) {
			continue
		}
		var previous proposal
		found, err := e.State.Get(ctx, "proposal:"+j.ID+".feedback."+entry.ID, &previous)
		if err != nil {
			return err
		}
		if found {
			if err := acknowledge(entry); err != nil {
				return err
			}
		} else {
			pending[entry.ID] = entry
		}
	}
	cursor := ""
	for len(pending) != 0 {
		path := "/v1/attention?limit=200"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page, err := api.Call[domain.Page[domain.Attention]](ctx, e.Client, "GET", path, nil)
		if err != nil {
			return err
		}
		for _, attention := range page.Items {
			if entry, found := pending[attention.EntryID]; found {
				if attention.Generation != entry.Generation {
					return domain.Fail(domain.Conflict, "review feedback changed after clarity")
				}
				if err := acknowledge(entry); err != nil {
					return err
				}
				delete(pending, entry.ID)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return nil
}

func (e *Engine) research(ctx context.Context, c config.Worker, tasks []domain.Task, manual bool) (bool, error) {
	if !slices.ContainsFunc(tasks, func(task domain.Task) bool { return task.Status == domain.Backlogged && !task.Repetitive }) {
		return false, nil
	}
	var last int64
	found, err := e.State.Get(ctx, "last-research", &last)
	if err != nil {
		return false, err
	}
	if found && e.Now().Sub(time.Unix(last, 0)) < time.Duration(c.ResearchIntervalHours)*time.Hour {
		deferWorkUntil(ctx, time.Unix(last, 0).Add(time.Duration(c.ResearchIntervalHours)*time.Hour))
		return false, nil
	}
	for _, task := range tasks {
		if task.Status != domain.Backlogged || task.Repetitive {
			continue
		}
		launch, err := e.launch(ctx, "research", &task)
		if err != nil {
			return false, err
		}
		_, err = e.text(ctx, c, launch, manual)
		return true, err
	}
	return false, nil
}

func neutralMentions(value string) string { return strings.ReplaceAll(value, "@{", "@(") }
