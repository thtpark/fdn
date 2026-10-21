package domain

import "testing"

func TestEligibleApproverUsesCurrentRoleAndNamedIndependentPolicy(t *testing.T) {
	founder := Agent{ID: "founder", Founder: true, Active: true}
	delegate := Agent{ID: "delegate", Active: true}
	worker := Agent{ID: "worker", Active: true}
	p := Project{Delegates: []string{delegate.ID}}
	task := Task{AssigneeID: worker.ID}
	if !EligibleApprover(founder, p, task, true) || !EligibleApprover(delegate, p, task, true) || EligibleApprover(worker, p, task, true) {
		t.Fatal("current project authority was not required")
	}
	task.Policy.RequiredActors = []string{worker.ID}
	if EligibleApprover(worker, p, task, true) || EligibleApprover(delegate, p, task, true) {
		t.Fatal("named actors granted or widened project authority")
	}
	task.Policy.RequiredActors = []string{delegate.ID}
	task.AssigneeID = delegate.ID
	task.Policy.IndependentVerification = true
	if EligibleApprover(delegate, p, task, true) || !EligibleApprover(delegate, p, task, false) {
		t.Fatal("readiness and verification independence were conflated")
	}
	task.Policy.IndependentReadiness = true
	if EligibleApprover(delegate, p, task, false) {
		t.Fatal("independent readiness allowed the assignee")
	}
	task.AssigneeID = worker.ID
	p.Archived = true
	if EligibleApprover(delegate, p, task, true) {
		t.Fatal("archived project retained approval authority")
	}
	p.Archived = false
	delegate.Active = false
	if EligibleApprover(delegate, p, task, true) {
		t.Fatal("revoked actor retained approval authority")
	}
}
