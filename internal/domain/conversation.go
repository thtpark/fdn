package domain

import "slices"

func RelevantComment(entry Entry, task Task, actor string) bool {
	return entry.Kind == "comment" && entry.AuthorID != actor &&
		(task.AssigneeID == actor || slices.Contains(entry.Mentions, actor))
}
