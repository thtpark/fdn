package domain

import (
	"strings"
	"testing"
)

func TestNoteIDsBindExactScopesAndTargets(t *testing.T) {
	for _, item := range []struct {
		scope, target, id string
	}{
		{CompanyNotes, "", "note_company"},
		{ProjectNotes, "project_fixture", "note_project_fixture"},
		{AgentNotes, "agent_fixture", "note_agent_fixture"},
		{"", "", ""},
		{"organization", "", ""},
		{CompanyNotes, "agent_fixture", ""},
		{ProjectNotes, "", ""},
		{ProjectNotes, "agent_fixture", ""},
		{AgentNotes, "project_fixture", ""},
		{AgentNotes, "agent_", ""},
		{AgentNotes, "agent_../other", ""},
		{AgentNotes, "agent_" + strings.Repeat("x", 123), ""},
	} {
		t.Run(item.scope+"/"+item.target, func(t *testing.T) {
			id, err := NoteID(item.scope, item.target)
			if item.id == "" {
				if err == nil || AsFault(err).Code != Invalid {
					t.Fatal("invalid note scope/target accepted", id, err)
				}
			} else if err != nil || id != item.id {
				t.Fatal("note identity changed", id, err)
			}
		})
	}
}
