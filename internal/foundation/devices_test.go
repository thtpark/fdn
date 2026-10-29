package foundation

import (
	"context"
	"reflect"
	"testing"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
)

func founderDevice(t *testing.T, f *securityFixture, name string, uid int64) (domain.AgentIdentity, securityKeys) {
	t.Helper()
	keys := securityPublic(t, name)
	req := domain.RegisterIdentityRequest{Command: op(), Identity: keys.public}
	identity, err := f.s.RegisterFounderDevice(context.Background(), f.founder.AgentID, req)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.RegisterFounderDevice(context.Background(), f.founder.AgentID, req)
	if err != nil || replayed != identity {
		t.Fatal("device registration lost its exact binding", replayed, err)
	}
	identity, _ = securityActivate(t, f.s, identity, keys, uid)
	return identity, keys
}

func TestFounderDevicesUseIndependentKeysAndOneActor(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	mac, macKeys := founderDevice(t, f, "Founder Mac", 4101)
	phone, phoneKeys := founderDevice(t, f, "Founder Phone", 4102)
	if mac.AgentID != f.founder.AgentID || phone.AgentID != mac.AgentID || mac.DeviceID == "" || phone.DeviceID == mac.DeviceID ||
		mac.UID == phone.UID || mac.SSHUser == phone.SSHUser || mac.SSHKey == phone.SSHKey || mac.AuthKey == phone.AuthKey {
		t.Fatal("Founder computers did not retain independent account/key bindings")
	}
	agents, err := f.s.Agents(ctx, mac.AgentID)
	if err != nil || len(agents) != 1 || !agents[0].Founder || agents[0].Mode != "manual" {
		t.Fatal("enrolling devices created another actor or changed automation", agents, err)
	}
	devices, err := f.s.FounderDevices(ctx, mac.AgentID)
	if err != nil || len(devices) != 3 {
		t.Fatal("original computer and additional devices are not listed", devices, err)
	}
	for _, entry := range []struct {
		identity domain.AgentIdentity
		keys     securityKeys
	}{{mac, macKeys}, {phone, phoneKeys}} {
		cert := securityCertificate(entry.identity, "device")
		if _, err := f.s.ValidateCertificate(ctx, entry.identity.UID, cert, entry.keys.auth.Public()); err != nil {
			t.Fatal("independent device authentication denied", err)
		}
		changed := cert
		changed.DeviceID = ""
		_, err := f.s.ValidateCertificate(ctx, entry.identity.UID, changed, entry.keys.auth.Public())
		assertCode(t, err, domain.Denied)
		_, err = f.s.ValidateCertificate(ctx, f.founder.UID, cert, entry.keys.auth.Public())
		assertCode(t, err, domain.Denied)
		fenced := WithIdentityFence(ctx, cert)
		current, err := f.s.ConnectionIdentity(fenced, mac.AgentID)
		if err != nil || current.BindingID() != entry.identity.BindingID() {
			t.Fatal("status selected another computer's enrollment", current, err)
		}
	}
	macChallenge, err := f.s.ChallengeIdentity(ctx, mac.UID)
	if err != nil {
		t.Fatal(err)
	}
	phoneChallenge, err := f.s.ChallengeIdentity(ctx, phone.UID)
	if err != nil || macChallenge.DeviceID != mac.DeviceID || phoneChallenge.DeviceID != phone.DeviceID {
		t.Fatal("activation challenges lost the device binding", err)
	}
	macProof, err := device.SignChallenge(macKeys.auth, macChallenge)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ActivateIdentity(ctx, phone.UID, domain.ActivateIdentityRequest{Command: op(), Nonce: macChallenge.Nonce, Proof: macProof})
	assertCode(t, err, domain.Denied)
	phoneProof, err := device.SignChallenge(phoneKeys.auth, phoneChallenge)
	if err != nil {
		t.Fatal(err)
	}
	for _, activation := range []struct {
		uid       int64
		challenge domain.IdentityChallenge
		proof     string
	}{{mac.UID, macChallenge, macProof}, {phone.UID, phoneChallenge, phoneProof}} {
		if _, err := f.s.ActivateIdentity(ctx, activation.uid, domain.ActivateIdentityRequest{Command: op(), Nonce: activation.challenge.Nonce, Proof: activation.proof}); err != nil {
			t.Fatal("another device displaced an unconsumed activation challenge", err)
		}
	}
	worker, _, _ := f.register(t, "unattended worker", 4103)
	_, err = f.s.RegisterFounderDevice(ctx, worker.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: securityPublic(t, "not a Founder device").public})
	assertCode(t, err, domain.Denied)
	_, err = f.s.FounderDevices(ctx, worker.AgentID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RevokeFounderDevice(ctx, worker.AgentID, phone.DeviceID, domain.RevokeDeviceRequest{Command: op(), Reason: "unauthorized"})
	assertCode(t, err, domain.Denied)
	collision := domain.RegisterIdentityRequest{Command: op(), Identity: macKeys.public}
	_, err = f.s.RegisterIdentity(ctx, f.founder.AgentID, collision)
	assertCode(t, err, domain.Conflict)
	collision.Identity.AuthProof = "invalid"
	_, err = f.s.RegisterFounderDevice(ctx, f.founder.AgentID, collision)
	assertCode(t, err, domain.Conflict)
	if err := f.s.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFounderOwnersCoexistAndDeviceRevocationIsScoped(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	phone, phoneKeys := founderDevice(t, f, "Founder Phone", 4101)
	primaryBefore, err := f.s.Identity(ctx, f.founder.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	primaryCtx := securityOwnerContext(t, f.s, primaryBefore)
	var phoneOwners []domain.AgentIdentity
	var phoneContexts []context.Context
	for range 2 {
		owner := securityPublic(t, "independent phone connection")
		identity, err := f.s.SetIdentityOwner(ctx, phone.BindingID(), owner.public.AuthKey, phone.Generation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ValidateCertificate(ctx, phone.UID, securityCertificate(identity, "owner"), owner.auth.Public()); err != nil {
			t.Fatal(err)
		}
		phoneOwners = append(phoneOwners, identity)
		phoneContexts = append(phoneContexts, WithIdentityFence(ctx, securityCertificate(identity, "owner")))
	}
	if phoneOwners[0].OwnerGeneration == phoneOwners[1].OwnerGeneration {
		t.Fatal("different Founder connections shared an owner generation")
	}
	for _, ownerCtx := range phoneContexts {
		if _, err := f.s.CreateProject(ownerCtx, phone.AgentID, domain.ProjectRequest{Command: op(), Name: "simultaneous Founder connection"}); err != nil {
			t.Fatal("later Founder admission fenced an existing connection", err)
		}
	}
	renewed, err := f.s.IdentityOwner(ctx, phone.DeviceID, phoneOwners[0].OwnerGeneration)
	if err != nil || renewed.OwnerKey != phoneOwners[0].OwnerKey {
		t.Fatal("renewal selected another Founder connection", renewed, err)
	}
	if err := f.s.BanDuplicateIdentity(ctx, f.founder.AgentID, f.founder.Generation, 1, "Founder multi-connection is permitted"); err == nil {
		t.Fatal("duplicate-owner policy banned Founder")
	} else {
		assertCode(t, err, domain.Denied)
	}
	phoneSelf := domain.RevokeDeviceRequest{Command: op(), Reason: "must use another computer"}
	_, err = f.s.RevokeFounderDevice(phoneContexts[0], phone.AgentID, phone.DeviceID, phoneSelf)
	assertCode(t, err, domain.Prerequisite)
	req := domain.RevokeDeviceRequest{Command: op(), Reason: "Lost phone; retain all other device keys"}
	revoked, err := f.s.RevokeFounderDevice(primaryCtx, phone.AgentID, phone.DeviceID, req)
	if err != nil || revoked.State != "revoked" || revoked.Generation != phone.Generation+1 {
		t.Fatal("device revocation did not retire the exact enrollment", revoked, err)
	}
	replayed, err := f.s.RevokeFounderDevice(primaryCtx, phone.AgentID, phone.DeviceID, req)
	if err != nil || replayed != revoked {
		t.Fatal("revocation replay changed the device generation", replayed, err)
	}
	req.Reason = "changed exact proposal"
	_, err = f.s.RevokeFounderDevice(primaryCtx, phone.AgentID, phone.DeviceID, req)
	assertCode(t, err, domain.Conflict)
	for _, ownerCtx := range phoneContexts {
		_, err := f.s.CreateProject(ownerCtx, phone.AgentID, domain.ProjectRequest{Command: op(), Name: "revoked phone"})
		assertCode(t, err, domain.Denied)
	}
	_, err = f.s.ValidateCertificate(ctx, phone.UID, securityCertificate(phone, "device"), phoneKeys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.ChallengeIdentity(ctx, phone.UID)
	assertCode(t, err, domain.Denied)
	_, err = f.s.RegisterFounderDevice(primaryCtx, phone.AgentID, domain.RegisterIdentityRequest{Command: op(), Identity: phoneKeys.public})
	assertCode(t, err, domain.Denied)
	current, err := f.s.Identity(ctx, f.founder.AgentID)
	if err != nil || current.Generation != primaryBefore.Generation || current.SSHKey != primaryBefore.SSHKey || current.AuthKey != primaryBefore.AuthKey {
		t.Fatal("revoking a phone changed the original computer's keys", err)
	}
	if _, err := f.s.Me(primaryCtx, phone.AgentID); err != nil {
		t.Fatal("revoking one device disconnected another", err)
	}
	var retainedOwners, retiredKeys int
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_owners WHERE identity_id=?", phone.DeviceID).Scan(&retainedOwners); err != nil || retainedOwners != 0 {
		t.Fatal("revoked owner rows retained authority", retainedOwners, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM identity_keys WHERE device_id=? AND retired=1", phone.DeviceID).Scan(&retiredKeys); err != nil || retiredKeys != 2 {
		t.Fatal("device key tombstones were not retained", retiredKeys, err)
	}
}

func TestFounderOriginalComputerRevocationAndOfflineRecovery(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	_, err := f.s.RevokeFounderDevice(ctx, f.founder.AgentID, f.founder.AgentID, domain.RevokeDeviceRequest{Command: op(), Reason: "last device"})
	assertCode(t, err, domain.Prerequisite)
	phone, keys := founderDevice(t, f, "Founder Phone", 4101)
	phoneCtx := securityOwnerContext(t, f.s, phone)
	original, err := f.s.RevokeFounderDevice(phoneCtx, phone.AgentID, f.founder.AgentID, domain.RevokeDeviceRequest{Command: op(), Reason: "Lost original computer"})
	if err != nil || original.State != "revoked" {
		t.Fatal("another Founder computer could not revoke the original", err)
	}
	if _, err := f.s.ValidateCertificate(ctx, phone.UID, securityCertificate(phone, "device"), keys.auth.Public()); err != nil {
		t.Fatal("revoking the original invalidated an independent device", err)
	}
	if _, err := f.s.Heartbeat(phoneCtx, phone.AgentID); err != nil {
		t.Fatal("remaining Founder device could not heartbeat", err)
	}
	if _, err := f.s.Updates(phoneCtx, phone.AgentID, "", 10); err != nil {
		t.Fatal("remaining Founder device lost scoped discovery", err)
	}
	_, err = f.s.ValidateCertificate(ctx, original.UID, securityCertificate(f.founder, "device"), f.keys.auth.Public())
	assertCode(t, err, domain.Denied)
	fresh := securityPublic(t, "Recovered Founder")
	recovered, err := f.s.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: op(), Reason: "Operator-authorized whole-Founder recovery", Identity: fresh.public})
	if err != nil || recovered.AgentID != phone.AgentID || recovered.State != "pending" {
		t.Fatal(recovered, err)
	}
	retired, err := f.s.Identity(ctx, phone.DeviceID)
	if err != nil || retired.State != "revoked" || retired.Generation <= phone.Generation {
		t.Fatal("whole-Founder recovery left an old device active", retired, err)
	}
	_, err = f.s.Me(phoneCtx, phone.AgentID)
	assertCode(t, err, domain.Denied)
	recovered, _ = securityActivate(t, f.s, recovered, fresh, f.founder.UID)
	_, err = f.s.ValidateCertificate(ctx, phone.UID, securityCertificate(phone, "device"), keys.auth.Public())
	assertCode(t, err, domain.Denied)
	agents, err := f.s.Agents(ctx, recovered.AgentID)
	if err != nil || len(agents) != 1 || agents[0].Mode != "manual" {
		t.Fatal("recovery created another actor or changed mode", agents, err)
	}
}

func TestFounderDeviceRestoreQuarantinesEveryBinding(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	phone, keys := founderDevice(t, f, "Founder Phone", 4101)
	phoneCtx := securityOwnerContext(t, f.s, phone)
	before, err := f.s.ManagedIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.s.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	for _, identity := range before {
		current, err := f.s.Identity(ctx, identity.BindingID())
		if err != nil || current.State != identityRestorePending || current.Generation != identity.Generation+1 || current.OwnerKey != "" {
			t.Fatal("restore failed to quarantine a Founder computer", current, err)
		}
	}
	_, err = f.s.ValidateCertificate(ctx, phone.UID, securityCertificate(phone, "device"), keys.auth.Public())
	assertCode(t, err, domain.Denied)
	_, err = f.s.Me(phoneCtx, phone.AgentID)
	assertCode(t, err, domain.Denied)
	retained, err := f.s.ManagedIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.QuarantineRestoredIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.ManagedIdentities(ctx)
	if err != nil || !reflect.DeepEqual(replayed, retained) {
		t.Fatal("quarantine retry advanced a device generation", replayed, err)
	}
}

func TestFounderDevicesStillShareOneWorkSlot(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	phone, _ := founderDevice(t, f, "Founder Phone", 4101)
	contexts := []context.Context{securityOwnerContext(t, f.s, f.founder), securityOwnerContext(t, f.s, phone)}
	project, err := f.s.CreateProject(ctx, f.founder.AgentID, domain.ProjectRequest{Command: op(), Name: "Shared Founder work slot"})
	if err != nil {
		t.Fatal(err)
	}
	var tasks []domain.Task
	for range 2 {
		task, err := f.s.CreateTask(ctx, f.founder.AgentID, domain.TaskRequest{Command: op(), ProjectID: project.ID,
			Title: "One Founder task", Description: "Execute within the one actor slot", AcceptanceCriteria: "Exactly one task is running"})
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"assign", "workspace", "status", "clarity", "approve"} {
			task = securityChangeTask(t, f.s, ctx, f.founder.AgentID, task.ID, kind, func(req *domain.TaskChange, current domain.Task) {
				switch kind {
				case "assign":
					req.AgentID, req.Provider = f.founder.AgentID, "codex"
				case "workspace":
					req.WorkspaceRef = "workspace_founder"
				case "status":
					req.Status = domain.Todo
				case "clarity", "approve":
					req.SpecGeneration = current.SpecGeneration
				}
			})
		}
		tasks = append(tasks, task)
	}
	type outcome struct {
		run domain.Run
		err error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for i := range contexts {
		go func(i int) {
			<-start
			run, err := f.s.Claim(contexts[i], f.founder.AgentID, tasks[i].ID, domain.ClaimRequest{
				Command:        domain.Command{OperationID: op().OperationID, ExpectedRevision: tasks[i].Revision},
				SpecGeneration: tasks[i].SpecGeneration, Provider: "codex", TokenHash: domain.Hash(domain.NewToken())})
			results <- outcome{run, err}
		}(i)
	}
	close(start)
	accepted, denied := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil && result.run.AgentID == f.founder.AgentID {
			accepted++
		} else if result.err != nil && domain.AsFault(result.err).Code == domain.Conflict {
			denied++
		} else {
			t.Fatal("unexpected simultaneous claim result", result.run, result.err)
		}
	}
	if accepted != 1 || denied != 1 {
		t.Fatal("Founder devices created multiple work slots", accepted, denied)
	}
}
