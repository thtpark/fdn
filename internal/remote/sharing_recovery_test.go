package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

type sharingCleanupCounter struct {
	SharingBackend
	ends int
}

func (backend *sharingCleanupCounter) EndInput(context.Context, config.Worker, *local.State, domain.Session) error {
	backend.ends++
	return nil
}

func TestSharingInputRecoveryRechecksFenceUnderTheWorkLock(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "replaced", true: "retired"}[retired], func(t *testing.T) {
			fixture := newManagerFixture(t)
			ctx := context.Background()
			original := Fence{Active: true, ID: "session_fixture", Generation: "original_generation"}
			if err := fixture.manager.State.Put(ctx, "session-fence", original); err != nil {
				t.Fatal(err)
			}
			expected, err := ReadSessionFence(ctx, fixture.manager.State)
			if err != nil {
				t.Fatal(err)
			}
			slot, err := secure.Acquire(filepath.Join(fixture.manager.Config.StateDir, "work.lock"))
			if err != nil {
				t.Fatal(err)
			}
			defer slot.Close()
			current := Fence{Active: !retired, ID: "newer_session", Generation: "newer_generation"}
			if err := fixture.manager.State.Put(ctx, "session-fence", current); err != nil {
				t.Fatal(err)
			}
			backend := &sharingCleanupCounter{}
			err = fixture.manager.recoverSharingInputLocked(ctx, backend, fixture.manager.Config, expected)
			if retired && err != nil || !retired && (err == nil || domain.AsFault(err).Code != domain.Conflict) {
				t.Fatal("stale recovery did not recognize exclusive fence ownership", err)
			}
			after, readErr := ReadSessionFence(ctx, fixture.manager.State)
			if readErr != nil || after.Fence != current || backend.ends != 0 {
				t.Fatal("stale recovery cleaned or changed another native lease", after, backend.ends, readErr)
			}
		})
	}
}

func TestSharingInputRecoveryDoesNotRepeatProvenNativeCleanup(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	owner, err := inspectProcess(os.Getpid())
	if err != nil || owner == nil {
		t.Fatal("fixture has no kernel process identity", err)
	}
	fixture.session.State = "closed"
	fence := Fence{Active: true, ID: fixture.session.ID, Generation: "input_generation"}
	lease := sharingInputRecord{State: "closing", Session: fixture.session, Fence: fence,
		OwnerPID: owner.PID, OwnerStart: owner.Started}
	if err := fixture.manager.State.Put(ctx, sharingInputKey(lease.Session.ID), lease); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.State.Put(ctx, "session-fence", fence); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSessionFence(ctx, fixture.manager.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.State.ProveSessionCleanup(ctx, lease.Session.ID, snapshot.Fingerprint); err != nil {
		t.Fatal(err)
	}
	backend := &sharingCleanupCounter{}
	if err := fixture.manager.recoverSharingInput(ctx, backend); err != nil {
		t.Fatal(err)
	}
	after, err := ReadSessionFence(ctx, fixture.manager.State)
	if err != nil || after.Fence.Active || backend.ends != 0 {
		t.Fatal("proven cleanup was repeated or its exact fence was not retired", after, backend.ends, err)
	}
}

func TestSharingInputAcknowledgementGatesInputUntilDurableReadiness(t *testing.T) {
	for _, granted := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "granted"}[granted], func(t *testing.T) {
			fixture := newManagerFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			requestSeen, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(requestSeen)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if !granted {
					envelope(w, nil, domain.Fail(domain.Denied, "fixture declined input"))
					return
				}
				session := fixture.session
				session.InputGranted = true
				envelope(w, session, nil)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { released.Do(func() { close(release) }) })
			fixture.manager.Client = &api.Client{BaseURL: server.URL, HTTP: server.Client(), Local: true}
			lease := sharingInputRecord{State: "prepared", Session: fixture.session}
			connection := &sharedConnection{}
			acknowledged := make(chan error, 1)
			go func() { acknowledged <- fixture.manager.acknowledgeSharingInput(ctx, connection, &lease) }()
			select {
			case <-requestSeen:
			case <-ctx.Done():
				t.Fatal("input acknowledgement did not start", ctx.Err())
			}
			allowed := make(chan bool, 1)
			go func() { allowed <- connection.inputAllowed() }()
			select {
			case result := <-allowed:
				t.Fatal("input raced an incomplete target acknowledgement", result)
			case <-time.After(50 * time.Millisecond):
			}
			released.Do(func() { close(release) })
			err := <-acknowledged
			if granted && err != nil || !granted && (err == nil || domain.AsFault(err).Code != domain.Denied) {
				t.Fatal("target acknowledgement result was not preserved", err)
			}
			select {
			case result := <-allowed:
				if result != granted {
					t.Fatal("input did not observe the completed target acknowledgement", result)
				}
			case <-ctx.Done():
				t.Fatal("input gate did not join the completed acknowledgement", ctx.Err())
			}
			if granted {
				var durable sharingInputRecord
				found, err := fixture.manager.State.Get(ctx, sharingInputKey(lease.Session.ID), &durable)
				if err != nil || !found || durable.State != "granted" || !durable.Session.InputGranted {
					t.Fatal("input was enabled before durable native readiness", durable, err)
				}
			}
		})
	}
}
