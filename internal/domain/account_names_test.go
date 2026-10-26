package domain

import (
	"strings"
	"testing"
)

func TestAgentUsernameUsesAnOSCompatibleAgentName(t *testing.T) {
	for _, test := range []struct{ name, want string }{
		{"Agent1", "agent1"},
		{"Founder", "founder"},
		{" Agent One ", "agent-one"},
		{"Agent.One / West", "agent-one-west"},
		{"_Agent-1", "_agent-1"},
		{strings.Repeat("A", 32), strings.Repeat("a", 32)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AgentUsername(test.name)
			if err != nil || got != test.want || !ValidAccountUsername(got) {
				t.Fatal(got, err)
			}
		})
	}
	for _, name := range []string{"", " ", "ROOT", "---root---", "123", "-1agent", "...", "東京", "Agent\nOne", "Agent\rOne", "agent\x00one", strings.Repeat("a", 33)} {
		t.Run(name, func(t *testing.T) {
			if _, err := AgentUsername(name); err == nil || AsFault(err).Code != Invalid {
				t.Fatal("accepted an unusable account name", err)
			}
		})
	}
}
