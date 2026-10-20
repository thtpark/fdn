package work

import (
	"context"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
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
			if message.Kind != "direct" || !message.ResponseRequest {
				continue
			}
			launch, err := e.launch(ctx, "message", nil)
			if err != nil {
				return false, err
			}
			launch.Message = &message
			_, err = e.text(ctx, c, launch, manual)
			return true, err
		}
		cursor = page.NextCursor
		if cursor == "" {
			return false, nil
		}
	}
}

func (e *Engine) attention(ctx context.Context, c config.Worker, manual bool) (bool, error) {
	page, err := api.Call[domain.Page[domain.Attention]](ctx, e.Client, "GET", "/v1/attention?limit=1", nil)
	if err != nil {
		return false, err
	}
	if len(page.Items) == 0 {
		return false, nil
	}
	item := page.Items[0]
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
	return true, err
}

func (e *Engine) research(ctx context.Context, c config.Worker, tasks []domain.Task, manual bool) (bool, error) {
	var last int64
	found, err := e.State.Get(ctx, "last-research", &last)
	if err != nil {
		return false, err
	}
	if found && e.Now().Sub(time.Unix(last, 0)) < time.Duration(c.ResearchIntervalHours)*time.Hour {
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
