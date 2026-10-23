package foundation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func readFixtureMessage(t *testing.T, f *fixture, requested bool) domain.Message {
	t.Helper()
	m, err := f.s.SendMessage(context.Background(), f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "fixture message", Origin: "human", Kind: "direct", ResponseRequest: requested})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageReadingIsExplicitRecipientOnlyAndFirstStampImmutable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := readFixtureMessage(t, f, true)
	for range 2 {
		if shown, err := f.s.Message(ctx, f.worker.ID, m.ID); err != nil || shown.ReadAt != 0 {
			t.Fatal("peek automatically acknowledged reading", shown, err)
		}
		if inbox, err := f.s.Inbox(ctx, f.worker.ID, "", 10, false); err != nil || len(inbox.Items) != 1 || inbox.Items[0].ReadAt != 0 {
			t.Fatal("listing automatically acknowledged reading", inbox, err)
		}
		if diagnosis, err := f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID); err != nil || diagnosis.ReadAt != 0 {
			t.Fatal("diagnosis marked recipient read", diagnosis, err)
		}
	}
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		if _, err := f.s.ReadMessage(ctx, actor, m.ID, domain.MessageReadRequest{Command: op()}); err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("sender/foreign actor forged recipient reading", actor, err)
		}
	}
	f.now = f.now.Add(time.Minute)
	request := domain.MessageReadRequest{Command: op()}
	read, err := f.s.ReadMessage(ctx, f.worker.ID, m.ID, request)
	if err != nil || read.ReadAt != f.now.Unix() || read.Revision != m.Revision || read.State != "pending" {
		t.Fatal("explicit read changed disposition or revision", read, err)
	}
	stamp := read.ReadAt
	f.now = f.now.Add(time.Minute)
	for _, req := range []domain.MessageReadRequest{request, {Command: op()}} {
		if again, err := f.s.ReadMessage(ctx, f.worker.ID, m.ID, req); err != nil || again.ReadAt != stamp || again.Revision != m.Revision {
			t.Fatal("retry/re-read overwrote first receipt", again, err)
		}
	}
	if sender, err := f.s.Message(ctx, f.founder.ID, m.ID); err != nil || sender.ReadAt != stamp {
		t.Fatal("sender cannot observe explicit reading", sender, err)
	}
	if diagnosis, err := f.s.MessageDiagnostics(ctx, f.founder.ID, m.ID); err != nil || diagnosis.ReadAt != stamp {
		t.Fatal("diagnosis omitted read receipt", diagnosis, err)
	}
	if handled, err := f.s.ReceiveMessage(ctx, f.worker.ID, m.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "handled"}); err != nil || handled.ReadAt != stamp {
		t.Fatal("read invalidated or was erased by prepared disposition", handled, err)
	}
}

func TestHandlingAndExpiryNeverManufactureOrRenewReadReceipt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := readFixtureMessage(t, f, false)
	handled, err := f.s.ReceiveMessage(ctx, f.worker.ID, m.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "handled"})
	if err != nil || handled.ReadAt != 0 {
		t.Fatal("handled receipt was treated as read", handled, err)
	}
	f.now = f.now.Add(time.Minute)
	read, err := f.s.ReadMessage(ctx, f.worker.ID, m.ID, domain.MessageReadRequest{Command: op()})
	if err != nil || read.State != "handled" || read.ReadAt != f.now.Unix() {
		t.Fatal("explicit reading after handling failed", read, err)
	}
	notice, err := f.s.SendMessage(ctx, f.founder.ID, domain.MessageRequest{Command: op(), RecipientID: f.worker.ID, Body: "safe expired sign-in notice", Origin: "service", Kind: "authentication", ExpiresAt: f.now.Unix() + 10})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(11 * time.Second)
	expired, err := f.s.ReadMessage(ctx, f.worker.ID, notice.ID, domain.MessageReadRequest{Command: op()})
	if err != nil || expired.ReadAt != f.now.Unix() || expired.State != "expired" || expired.ExpiresAt != notice.ExpiresAt {
		t.Fatal("read renewed or reopened expired notice", expired, err)
	}
	inbox, err := f.s.Inbox(ctx, f.worker.ID, "", 10, false)
	if err != nil || len(inbox.Items) != 0 {
		t.Fatal("expired/handled notice re-entered inbox", inbox, err)
	}
}

func TestReadReceiptAndDispositionCommuteWithoutRevisionRace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := readFixtureMessage(t, f, true)
	f.now = f.now.Add(time.Second)
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := f.s.ReadMessage(ctx, f.worker.ID, m.ID, domain.MessageReadRequest{Command: op()})
		errors <- err
	}()
	go func() {
		defer wait.Done()
		_, err := f.s.ReceiveMessage(ctx, f.worker.ID, m.ID, domain.MessageReceipt{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}, State: "handled"})
		errors <- err
	}()
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal("independent read receipt invalidated disposition", err)
		}
	}
	final, err := f.s.Message(ctx, f.founder.ID, m.ID)
	if err != nil || final.State != "handled" || final.ReadAt != f.now.Unix() || final.Revision != m.Revision+1 {
		t.Fatal("concurrent receipt dropped independent evidence", final, err)
	}
}

func TestReadReceiptCannotCompleteProtectedOperationOrUseDispositionRevision(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := readFixtureMessage(t, f, true)
	if _, err := f.s.ReadMessage(ctx, f.worker.ID, m.ID, domain.MessageReadRequest{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: m.Revision}}); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("read accepted disposition revision", err)
	}
	operation, err := f.s.RequestDiagnosticRefresh(ctx, f.founder.ID, f.worker.ID, domain.DiagnosticRefreshRequest{Command: op(), Reason: "fixture protected operation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadMessage(ctx, f.worker.ID, operation.ID, domain.MessageReadRequest{Command: op()}); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("ordinary reading forged protected control handling", err)
	}
	current, err := f.s.GetAgentOperation(ctx, f.worker.ID, operation.ID)
	if err != nil || current.State != "queued" {
		t.Fatal("read changed protected operation state", current, err)
	}
}
