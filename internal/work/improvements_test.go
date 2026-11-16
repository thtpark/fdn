package work

import (
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

func TestImprovementReservedCapacityUsesExactServerWindow(t *testing.T) {
	usage := domain.MaintenanceUsage{WindowStart: 3600, ServerObservedAt: 3600,
		Policy: domain.ImprovementPolicy{Enabled: true, WindowSeconds: 3600, SliceSeconds: 300, PerAgentSeconds: 600, FleetSeconds: 1800}}
	if !reserveDue(usage, usage.ServerObservedAt) {
		t.Fatal("bounded reserve could starve behind continuously ready ordinary work")
	}
	usage.AgentSeconds, usage.FleetSeconds = 300, 300
	if reserveDue(usage, 5399) || !reserveDue(usage, 5400) {
		t.Fatal("reserve pacing does not honor its exact allowance/window threshold")
	}
	usage.AgentSeconds = 600
	if reserveDue(usage, 7199) {
		t.Fatal("exhausted per-agent allowance was ignored")
	}
	usage.AgentSeconds, usage.FleetSeconds = 0, 1800
	if reserveDue(usage, 7199) {
		t.Fatal("adding another actor ignored the aggregate budget")
	}
	launch := Launch{Maintenance: &domain.MaintenanceReservation{Seconds: 30}}
	if improvementTimeout(launch, time.Hour) != 30*time.Second {
		t.Fatal("native/provider timeout outlived the admitted improvement slice")
	}
}

func TestImprovementTriageHasItsOwnStructuredSourceFence(t *testing.T) {
	reply := provider.Reply{Outcome: "reviewed", Improvement: &provider.ImprovementChoice{Decision: "defer"}}
	first := Launch{Phase: "improvement-triage", Improvement: &domain.Improvement{ID: "improvement_first", Revision: 1}}
	second := Launch{Phase: "improvement-triage", Improvement: &domain.Improvement{ID: "improvement_second", Revision: 1}}
	if err := validateTextPhase(first, reply); err != nil {
		t.Fatal("dedicated triage shape was rejected", err)
	}
	if textActionHash(first) == textActionHash(second) {
		t.Fatal("one invalid request response fenced all other improvement intake")
	}
	first.Maintenance = &domain.MaintenanceReservation{ID: "maintenance_new", Seconds: 30}
	again := first
	again.Maintenance = &domain.MaintenanceReservation{ID: "maintenance_another", Seconds: 30}
	if textActionHash(first) != textActionHash(again) {
		t.Fatal("new reservations reset the semantic source retry fence")
	}
	if err := validateTextPhase(Launch{Phase: "scope"}, reply); err == nil {
		t.Fatal("triage fields escaped into ordinary task preparation")
	}
	if improvementContent(domain.Task{}) != "" {
		t.Fatal("legacy ordinary-task preparation hashes were changed")
	}
}
