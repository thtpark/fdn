package foundation

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func securityOwnerContext(t *testing.T, store *Store, identity domain.AgentIdentity) context.Context {
	t.Helper()
	owner := securityPublic(t, "ephemeral fixture owner")
	identity, err := store.SetIdentityOwner(context.Background(), identity.AgentID, owner.public.AuthKey, identity.Generation)
	if err != nil {
		t.Fatal(err)
	}
	return WithIdentityFence(context.Background(), securityCertificate(identity, "owner"))
}

func securityChangeTask(t *testing.T, store *Store, ctx context.Context, actor, id, kind string, set func(*domain.TaskChange, domain.Task)) domain.Task {
	t.Helper()
	task, err := store.Task(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.TaskChange{Command: op(), Kind: kind, Reason: "fenced fixture decision"}
	req.ExpectedRevision = task.Revision
	if set != nil {
		set(&req, task)
	}
	task, err = store.ChangeTask(ctx, actor, id, req)
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	return task
}

func TestSecurityFencedDistinctSubjectsAndSessionAccess(t *testing.T) {
	f := newSecurityFixture(t)
	delegate, _, _ := f.register(t, "delegate requester", 4101)
	target, _, _ := f.register(t, "target worker", 4102)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	delegateCtx := securityOwnerContext(t, f.s, delegate)
	targetCtx := securityOwnerContext(t, f.s, target)
	project, err := f.s.CreateProject(founderCtx, f.founder.AgentID, domain.ProjectRequest{Command: op(), Name: "fenced project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GrantCreation(founderCtx, f.founder.AgentID, domain.GrantRequest{Command: op(), AgentID: delegate.AgentID}); err != nil {
		t.Fatal("grant recipient was treated as the caller", err)
	}
	project, err = f.s.Delegate(founderCtx, f.founder.AgentID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: delegate.AgentID})
	if err != nil {
		t.Fatal("project delegate was treated as the caller", err)
	}
	targetAgent, err := f.s.Agent(founderCtx, f.founder.AgentID, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"manual", "ai"} {
		targetAgent, err = f.s.SetMode(founderCtx, f.founder.AgentID, target.AgentID, domain.ModeRequest{
			Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: targetAgent.Revision}, Mode: mode, Reason: "explicit fixture mode request"})
		if err != nil {
			t.Fatal("mode target was treated as the caller", err)
		}
	}
	task, err := f.s.CreateTask(delegateCtx, delegate.AgentID, domain.TaskRequest{Command: op(), ProjectID: project.ID,
		Title: "fenced assignment", Description: "Perform the fixture task", AcceptanceCriteria: "Return fixture evidence"})
	if err != nil {
		t.Fatal(err)
	}
	task = securityChangeTask(t, f.s, delegateCtx, delegate.AgentID, task.ID, "assign", func(req *domain.TaskChange, _ domain.Task) {
		req.AgentID, req.Provider = target.AgentID, "codex"
	})
	task = securityChangeTask(t, f.s, delegateCtx, delegate.AgentID, task.ID, "policy", func(req *domain.TaskChange, _ domain.Task) {
		req.Policy = &domain.ApprovalPolicy{RequiredActors: []string{f.founder.AgentID, delegate.AgentID}, IndependentReadiness: true}
	})
	task = securityChangeTask(t, f.s, targetCtx, target.AgentID, task.ID, "workspace", func(req *domain.TaskChange, _ domain.Task) {
		req.WorkspaceRef = "workspace_fenced_fixture"
	})
	task = securityChangeTask(t, f.s, targetCtx, target.AgentID, task.ID, "status", func(req *domain.TaskChange, _ domain.Task) {
		req.Status = domain.Todo
	})
	question, err := f.s.CreateEntry(targetCtx, target.AgentID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment",
		Body: "Confirm fixture scope. @{" + f.founder.AgentID + "} @{" + delegate.AgentID + "}"})
	if err != nil {
		t.Fatal(err)
	}
	task = securityChangeTask(t, f.s, targetCtx, target.AgentID, task.ID, "waiting", func(req *domain.TaskChange, current domain.Task) {
		req.SpecGeneration = current.SpecGeneration
		req.Waiting = &domain.Waiting{Kind: "clarification", Reason: "fixture scope confirmation", RequestedIDs: []string{f.founder.AgentID, delegate.AgentID}, QuestionIDs: []string{question.ID}}
	})
	answers := []string{}
	for _, respondent := range []struct {
		ctx context.Context
		id  string
	}{{founderCtx, f.founder.AgentID}, {delegateCtx, delegate.AgentID}} {
		answer, err := f.s.CreateEntry(respondent.ctx, respondent.id, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", ParentID: question.ID, Body: "The described fixture scope is correct."})
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, answer.ID)
	}
	task = securityChangeTask(t, f.s, targetCtx, target.AgentID, task.ID, "clarity", func(req *domain.TaskChange, current domain.Task) {
		req.SpecGeneration, req.AnswerIDs = current.SpecGeneration, answers
	})
	task = securityChangeTask(t, f.s, delegateCtx, delegate.AgentID, task.ID, "approve", func(req *domain.TaskChange, current domain.Task) {
		req.SpecGeneration = current.SpecGeneration
	})
	grant, err := f.s.CreateAssistance(founderCtx, f.founder.AgentID, domain.AssistanceGrantRequest{Command: op(),
		RequesterID: delegate.AgentID, TargetID: target.AgentID, ProjectID: project.ID, Mode: "tmux", Purpose: "terminal", Reason: "fixture-only distinct-party assistance"})
	if err != nil {
		t.Fatal("grant subjects were treated as callers", err)
	}
	session, err := f.s.RequestSession(delegateCtx, delegate.AgentID, domain.SessionRequest{Command: op(), GrantID: grant.ID,
		TargetID: target.AgentID, ProjectID: project.ID, TaskID: task.ID, Mode: "tmux", Purpose: "terminal", Reason: "fixture-only session", ExpiresAt: f.now.Unix() + 180})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(delegateCtx, delegate.AgentID, session.ID, domain.SessionChange{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(targetCtx, target.AgentID, session.ID, domain.SessionChange{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "active"})
	if err != nil {
		t.Fatal("distinct target could not activate its session", err)
	}
	for _, party := range []struct {
		ctx      context.Context
		id, side string
	}{{delegateCtx, delegate.AgentID, "requester"}, {targetCtx, target.AgentID, "target"}} {
		access, err := f.s.SessionAccess(party.ctx, party.id, session.ID, party.side)
		if err != nil || access.State != "active" {
			t.Fatal("fenced legitimate multi-party relay access failed", party.side, err)
		}
	}
	_, err = f.s.SessionAccess(founderCtx, delegate.AgentID, session.ID, "requester")
	assertCode(t, err, domain.Denied)
	_, err = f.s.SessionAccess(founderCtx, f.founder.AgentID, session.ID, "requester")
	assertCode(t, err, domain.Denied)
	session, err = f.s.ChangeSession(delegateCtx, delegate.AgentID, session.ID, domain.SessionChange{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "close"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Claim(targetCtx, target.AgentID, task.ID, domain.ClaimRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration,
		TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"}); err != nil {
		t.Fatal("fenced worker could not use a distinct delegate's current approval", err)
	}
	fresh := securityPublic(t, "explicitly rotated target")
	recovered, err := f.s.RecoverIdentity(founderCtx, f.founder.AgentID, target.AgentID, domain.RecoverIdentityRequest{
		Command: op(), Reason: "explicit fixture key rotation", Identity: fresh.public})
	if err != nil {
		t.Fatal(err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, target.UID)
	_, err = f.s.Session(targetCtx, target.AgentID, session.ID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.CreateEntry(targetCtx, target.AgentID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "stale pre-rotation caller"})
	assertCode(t, err, domain.Denied)
	currentCtx := securityOwnerContext(t, f.s, recovered)
	if _, err := f.s.Session(currentCtx, recovered.AgentID, session.ID); err != nil {
		t.Fatal("fresh caller lost legitimate retained session visibility", err)
	}
	if _, err := f.s.CreateEntry(currentCtx, recovered.AgentID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "fresh post-rotation caller"}); err != nil {
		t.Fatal("fresh caller could not mutate after explicit recovery", err)
	}
}

func TestSecurityRevocationRetiresRegistryAndPreservesWorkFence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	keys := securityPublic(t, "registered fixture worker")
	identity, err := f.s.RegisterIdentity(ctx, f.founder.ID, domain.RegisterIdentityRequest{Command: op(), ExistingAgentID: f.worker.ID, Identity: keys.public})
	if err != nil {
		t.Fatal(err)
	}
	identity, activation := securityActivate(t, f.s, identity, keys, 4101)
	owner := securityPublic(t, "worker owner")
	identity, err = f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	if err != nil {
		t.Fatal(err)
	}
	task := f.ready(t)
	run, err := f.s.Claim(ctx, f.worker.ID, task.ID, domain.ClaimRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: task.Revision}, SpecGeneration: task.SpecGeneration,
		TokenHash: domain.Hash(domain.NewToken()), Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChallengeIdentity(ctx, identity.UID); err != nil {
		t.Fatal(err)
	}
	req := domain.AgentRequest{Command: op()}
	for range 2 {
		agent, err := f.s.Revoke(ctx, f.founder.ID, identity.AgentID, req)
		if err != nil || agent.Active || agent.Banned {
			t.Fatal("regular revocation became a ban or did not revoke", agent, err)
		}
	}
	revoked, err := f.s.Identity(ctx, identity.AgentID)
	if err != nil || revoked.State != "revoked" || revoked.Generation != identity.Generation+1 || revoked.OwnerGeneration != identity.OwnerGeneration+1 || revoked.OwnerKey != "" || revoked.UID != identity.UID {
		t.Fatal("regular revocation failed to retire registry epochs and preserve account reservation", revoked, err)
	}
	managed, err := f.s.ManagedIdentities(ctx)
	if err != nil || len(managed) != 1 || managed[0] != revoked {
		t.Fatal("native reconciliation cannot observe revoked account", managed, err)
	}
	var retired, challenges int
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE agent_id=? AND retired=1", identity.AgentID).Scan(&retired); err != nil || retired != 2 {
		t.Fatal("revoked keys lost their unique tombstones", retired, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_challenges WHERE agent_id=?", identity.AgentID).Scan(&challenges); err != nil || challenges != 0 {
		t.Fatal("revocation retained activation challenges", challenges, err)
	}
	_, err = f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "device"), keys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "owner"), owner.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ChallengeIdentity(ctx, identity.UID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.ActivateIdentity(ctx, identity.UID, activation)
	assertCode(t, err, domain.Denied)
	_, err = f.s.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, identity.UID, identity.SSHFingerprint)
	assertCode(t, err, domain.Denied)
	_, err = f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	assertCode(t, err, domain.Denied)
	stopped, err := f.s.Task(ctx, f.founder.ID, task.ID)
	if err != nil || stopped.ActiveRun == nil || stopped.ActiveRun.ID != run.ID || !stopped.ActiveRun.StopRequested || stopped.Readiness != nil {
		t.Fatal("regular revocation released work ownership or retained approval", stopped.ActiveRun, err)
	}
	_, err = f.s.Revoke(ctx, f.founder.ID, f.founder.ID, domain.AgentRequest{Command: op()})
	assertCode(t, err, domain.Denied)
	fresh := securityPublic(t, "explicit revocation recovery")
	recovered, err := f.s.RecoverIdentity(ctx, f.founder.ID, identity.AgentID, domain.RecoverIdentityRequest{
		Command: op(), Reason: "explicit recovery of a revoked fixture worker", Identity: fresh.public})
	if err != nil || recovered.Generation != revoked.Generation+1 || recovered.State != "pending" || recovered.UID != -1 {
		t.Fatal("revocation recovery did not require fresh reprovisioning", recovered, err)
	}
	recovered, _ = securityActivate(t, f.s, recovered, fresh, identity.UID)
	if _, err := f.s.ValidateCertificate(ctx, recovered.UID, securityCertificate(recovered, "device"), fresh.auth.Public()); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "device"), keys.auth.Public())
	assertCode(t, err, domain.Denied)
	if _, err := f.s.Revoke(ctx, f.founder.ID, identity.AgentID, req); err != nil {
		t.Fatal("lost-ack revocation replay failed", err)
	}
	retained, err := f.s.Identity(ctx, identity.AgentID)
	if err != nil || retained != recovered {
		t.Fatal("old revocation replay retired freshly recovered keys", err)
	}
}

func TestSecurityRevocationRollsBackAllIdentityStateOnStorageFailure(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	identity, keys, _ := f.register(t, "worker", 4101)
	owner := securityPublic(t, "worker owner")
	identity, err := f.s.SetIdentityOwner(ctx, identity.AgentID, owner.public.AuthKey, identity.Generation)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := f.s.ChallengeIdentity(ctx, identity.UID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`CREATE TRIGGER fixture_refuse_revocation BEFORE UPDATE ON agent_identities
WHEN json_extract(NEW.data,'$.state')='revoked' BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`); err != nil {
		t.Fatal(err)
	}
	req := domain.AgentRequest{Command: op()}
	if _, err := f.s.Revoke(ctx, f.founder.AgentID, identity.AgentID, req); err == nil {
		t.Fatal("injected registry write failure was ignored")
	}
	retained, err := f.s.Identity(ctx, identity.AgentID)
	if err != nil || retained != identity {
		t.Fatal("failed revocation changed registry", retained, err)
	}
	if _, err := f.s.ValidateCertificate(ctx, identity.UID, securityCertificate(identity, "device"), keys.auth.Public()); err != nil {
		t.Fatal("failed revocation deactivated the agent row", err)
	}
	var retired int
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE agent_id=? AND retired=1", identity.AgentID).Scan(&retired); err != nil || retired != 0 {
		t.Fatal("failed revocation retired keys", retired, err)
	}
	var nonceHash string
	if err := f.s.db.QueryRow("SELECT nonce_hash FROM identity_challenges WHERE agent_id=?", identity.AgentID).Scan(&nonceHash); err != nil || nonceHash != domain.Hash(challenge.Nonce) {
		t.Fatal("failed revocation consumed activation challenge", err)
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_refuse_revocation"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Revoke(ctx, f.founder.AgentID, identity.AgentID, req); err != nil {
		t.Fatal("exact failed revocation could not be retried", err)
	}
}

func TestSecurityFencedSessionDeniesRevokedReferencedParty(t *testing.T) {
	f := newSecurityFixture(t)
	requester, _, _ := f.register(t, "requester", 4101)
	target, _, _ := f.register(t, "target", 4102)
	founderCtx := securityOwnerContext(t, f.s, f.founder)
	requesterCtx := securityOwnerContext(t, f.s, requester)
	grant, err := f.s.CreateAssistance(founderCtx, f.founder.AgentID, domain.AssistanceGrantRequest{Command: op(),
		RequesterID: requester.AgentID, TargetID: target.AgentID, Mode: "tmux", Purpose: "terminal", Reason: "fixture assistance"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.s.RequestSession(requesterCtx, requester.AgentID, domain.SessionRequest{Command: op(), GrantID: grant.ID,
		TargetID: target.AgentID, Mode: "tmux", Purpose: "terminal", Reason: "fixture session", ExpiresAt: f.now.Unix() + 180})
	if err != nil {
		t.Fatal(err)
	}
	session, err = f.s.ChangeSession(requesterCtx, requester.AgentID, session.ID, domain.SessionChange{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SessionAccess(requesterCtx, requester.AgentID, session.ID, "requester"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Revoke(founderCtx, f.founder.AgentID, target.AgentID, domain.AgentRequest{Command: op()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Me(requesterCtx, requester.AgentID); err != nil {
		t.Fatal("subject revocation invalidated the unrelated caller", err)
	}
	_, err = f.s.SessionAccess(requesterCtx, requester.AgentID, session.ID, "requester")
	assertCode(t, err, domain.Denied)
	_, err = f.s.CreateAssistance(founderCtx, f.founder.AgentID, domain.AssistanceGrantRequest{Command: op(),
		RequesterID: requester.AgentID, TargetID: target.AgentID, Mode: "tmux", Purpose: "terminal", Reason: "must reject revoked subject"})
	assertCode(t, err, domain.Denied)
}
