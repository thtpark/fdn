//go:build integration

package integration_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/provider"
)

type readChoiceExecutor struct {
	base        executor
	acknowledge bool
}

func (x *readChoiceExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	return x.base.Check(ctx, name)
}
func (x *readChoiceExecutor) Invoke(ctx context.Context, inv provider.Invocation) (provider.Reply, error) {
	r, err := x.base.Invoke(ctx, inv)
	if err == nil && r.Outcome == "responded" {
		r.MessageRead = x.acknowledge
	}
	return r, err
}

type lostReadAcknowledgement struct {
	base http.RoundTripper
	lost atomic.Bool
}

func (transport *lostReadAcknowledgement) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil && request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/read") && transport.lost.CompareAndSwap(false, true) {
		_, copyErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		return nil, errors.Join(errors.New("fixture lost explicit-read acknowledgement"), copyErr, closeErr)
	}
	return response, err
}

func TestControllerMarksReadOnlyForExplicitRecipientDecision(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		name := "peek-without-receipt"
		if acknowledge {
			name = "explicit-finished-reading"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			x := &readChoiceExecutor{acknowledge: acknowledge}
			e := newEngine(t, h, &executor{})
			e.Executor = x
			skipHealthOnboarding(t, e)
			message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Body: "Fixture requested response with optional explicit read receipt", Origin: "human", Kind: "direct", ResponseRequest: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, client := range []*api.Client{h.founder, h.worker} {
				shown, err := api.Call[domain.Message](ctx, client, "GET", api.Path("messages", message.ID), nil)
				if err != nil || shown.ReadAt != 0 {
					t.Fatal("peek marked reading", shown, err)
				}
			}
			if _, err := e.Step(ctx, false); err != nil {
				t.Fatal(err)
			}
			read, err := api.Call[domain.Message](ctx, h.founder, "GET", api.Path("messages", message.ID), nil)
			if err != nil || read.State != "handled" || (read.ReadAt != 0) != acknowledge || x.base.count("message") != 1 {
				t.Fatal("reply implicitly stamped reading or lost explicit choice", read, err)
			}
			diagnosis, err := api.Call[domain.MessageDiagnostics](ctx, h.founder, "GET", api.Path("messages", message.ID, "diagnostics"), nil)
			if err != nil || diagnosis.ReadAt != read.ReadAt {
				t.Fatal("sender cannot inspect actual read acknowledgement", diagnosis, err)
			}
		})
	}
}

func TestExplicitReadLostAckReplaysWithoutAnotherModelCall(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	x := &readChoiceExecutor{acknowledge: true}
	e := newEngine(t, h, &executor{})
	e.Executor = x
	skipHealthOnboarding(t, e)
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Body: "Read exactly once despite response uncertainty", Origin: "human", Kind: "direct", ResponseRequest: true})
	if err != nil {
		t.Fatal(err)
	}
	base := h.worker.HTTP.Transport
	transport := &lostReadAcknowledgement{base: base}
	h.worker.HTTP.Transport = transport
	defer func() { h.worker.HTTP.Transport = base }()
	if status, err := e.Step(ctx, false); err == nil || domain.AsFault(err).Code != domain.Prerequisite || status.State != "blocked" {
		t.Fatal("lost read receipt did not retain prepared response", status, err)
	}
	read, err := api.Call[domain.Message](ctx, h.founder, "GET", api.Path("messages", message.ID), nil)
	if err != nil || read.ReadAt == 0 || read.State != "pending" || read.Revision != message.Revision {
		t.Fatal("read changed pending response disposition", read, err)
	}
	stamp := read.ReadAt
	e = restartAuthenticationEngine(t, e)
	if status, err := e.Step(ctx, false); err != nil || status.State != "reconciled" {
		t.Fatal("read replay lost prepared reply", status, err)
	}
	finished, err := api.Call[domain.Message](ctx, h.founder, "GET", api.Path("messages", message.ID), nil)
	if err != nil || finished.ReadAt != stamp || finished.State != "handled" || x.base.count("message") != 1 {
		t.Fatal("replay repeated inference or read time", finished, err, x.base.count("message"))
	}
}

func TestMessageReadRawAPIEnforcesRecipientAndProtectedTimestamp(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	message, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", domain.MessageRequest{Command: command(), RecipientID: h.workerAgent.ID, Body: "Explicit manual reading need not request a reply", Origin: "human", Kind: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	path := api.Path("messages", message.ID, "read")
	for _, client := range []*api.Client{h.founder, h.other} {
		if _, err := api.Call[domain.Message](ctx, client, "POST", path, domain.MessageReadRequest{Command: command()}); err == nil || domain.AsFault(err).Code != domain.Denied {
			t.Fatal("non-recipient marked reading", err)
		}
	}
	for _, field := range []string{"read_at", "created_at", "recipient_id", "state", "body"} {
		input := map[string]any{"operation_id": command().OperationID, field: "forged"}
		if _, err := api.Call[domain.Message](ctx, h.worker, "POST", path, input); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("protected read input accepted", field, err)
		}
	}
	if _, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", map[string]any{"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "body": "forged initial receipt", "origin": "human", "kind": "direct", "read_at": 1800000000}); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("sender supplied read timestamp at creation", err)
	}
	if _, err := api.Call[domain.Message](ctx, h.worker, "POST", api.Path("messages", message.ID, "receipt"), map[string]any{"operation_id": command().OperationID, "expected_revision": message.Revision, "state": "handled", "read_at": 1800000000}); err == nil || domain.AsFault(err).Code != domain.Invalid {
		t.Fatal("disposition endpoint accepted a forged read timestamp", err)
	}
	ack, err := api.Call[domain.Message](ctx, h.worker, "POST", path, domain.MessageReadRequest{Command: command()})
	if err != nil || ack.ReadAt == 0 || ack.State != "pending" || ack.ResponseRequest || ack.Revision != message.Revision {
		t.Fatal("explicit manual receipt changed message handling", ack, err)
	}
}
