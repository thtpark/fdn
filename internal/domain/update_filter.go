package domain

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
)

// UpdateFilter selects a bounded union of topics/entities, never a principal.
type UpdateFilter struct {
	Topics   []string `json:"topics,omitempty"`
	Sources  []string `json:"sources,omitempty"`
	Tasks    []string `json:"tasks,omitempty"`
	Projects []string `json:"projects,omitempty"`
	Self     bool     `json:"self,omitempty"`
}

func ParseUpdateFilter(value string) (UpdateFilter, error) {
	var filter UpdateFilter
	if value == "" {
		return filter, nil
	}
	if len(value) > 512 {
		return filter, Fail(Invalid, "update filter exceeds 512 bytes")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&filter); err != nil {
		return filter, Fail(Invalid, "invalid update filter")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return filter, Fail(Invalid, "update filter requires one JSON object")
	}
	return filter.Normalize()
}

func (f UpdateFilter) Normalize() (UpdateFilter, error) {
	total := 0
	for _, list := range []*[]string{&f.Topics, &f.Sources, &f.Tasks, &f.Projects} {
		values := slices.Clone(*list)
		slices.Sort(values)
		values = slices.Compact(values)
		for _, value := range values {
			if !ValidOperation(value) {
				return f, Fail(Invalid, "update selectors require bounded nonempty IDs/topics")
			}
		}
		*list = values
		total += len(values)
	}
	if total > 16 {
		return f, Fail(Invalid, "at most sixteen update selectors are supported")
	}
	data, err := json.Marshal(f)
	if err != nil || len(data) > 512 {
		return f, Fail(Invalid, "update filter exceeds its bounded selector budget")
	}
	return f, nil
}

func (f UpdateFilter) Empty() bool {
	return !f.Self && len(f.Topics)+len(f.Sources)+len(f.Tasks)+len(f.Projects) == 0
}

func (f UpdateFilter) Fingerprint() string {
	if f.Empty() {
		return ""
	}
	data, _ := json.Marshal(f)
	return Hash(string(data))
}

func (f UpdateFilter) Matches(update Update, actor string) bool {
	if f.Empty() {
		return true
	}
	return (f.Self && update.SourceID == actor && (update.Topic == "agent" || update.Topic == "agent-mode" || update.Topic == "agent-status")) ||
		slices.Contains(f.Topics, update.Topic) || slices.Contains(f.Sources, update.SourceID) ||
		(update.TaskID != "" && slices.Contains(f.Tasks, update.TaskID)) ||
		(update.ProjectID != "" && slices.Contains(f.Projects, update.ProjectID))
}
