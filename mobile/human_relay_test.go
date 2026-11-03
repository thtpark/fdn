package mobile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
)

func TestHumanTerminalOpenRelayAndCloseKeepExactProposal(t *testing.T) {
	r := testRuntime(t)
	state, err := local.Open(context.Background(), r.root+"/runner")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	grant := domain.AssistanceGrant{ID: "grant_human", RequesterID: "agent_founder",
		TargetID: "agent_target", Mode: "tmux", Purpose: "authentication", Provider: "codex", Active: true}
	session := domain.Session{ID: "session_human", RequesterID: grant.RequesterID,
		TargetID: grant.TargetID, GrantID: grant.ID, Mode: grant.Mode, Purpose: grant.Purpose,
		Provider: grant.Provider, ExpiresAt: time.Now().Unix() + 300, State: "consented", Revision: 1}
	var mu sync.Mutex
	created, opened, closed := 0, 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("X-Fdn-API-Version", "2")
		if request.URL.Path == "/v1/sessions/session_human/relay/requester" {
			conn, err := websocket.Accept(w, request, nil)
			if err != nil {
				t.Error(err)
				return
			}
			stream := websocket.NetConn(request.Context(), conn, websocket.MessageBinary)
			defer stream.Close()
			_, _ = io.Copy(stream, stream)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var data any
		switch request.URL.Path {
		case "/v1/me":
			data = domain.Agent{ID: grant.RequesterID, Active: true, Founder: true, Mode: "manual"}
		case "/v1/assistance/grant_human":
			data = grant
		case "/v1/messages/message_help":
			data = domain.Message{ID: "message_help", Help: &domain.AssistanceHelpRequest{Mode: "tmux", Purpose: "authentication", Provider: "codex"}}
		case "/v1/sessions":
			var input domain.SessionRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.GrantID != grant.ID || input.HelpMessageID != "message_help" ||
				input.Provider != "codex" || input.OperationID != "assist_"+domain.Hash("phone-human-open") {
				t.Error("mobile creation changed the exact approved help proposal", input)
			}
			created++
			data = session
		case "/v1/sessions/session_human":
			if request.Method == "POST" {
				var input domain.SessionChange
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input.Action == "open" {
					opened++
					session.OpenRequested = true
				} else if input.Action == "close" {
					closed++
					session.State = "closed"
				} else {
					t.Error("unexpected human session action", input.Action)
				}
				session.Revision++
			}
			data = session
		default:
			t.Error("unexpected protected mobile request", request.URL.Path)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(api.Envelope{OK: true, Data: encoded, Warnings: []string{}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	f := &foreground{context: context.Background(), state: state,
		client: &api.Client{BaseURL: server.URL, HTTP: server.Client(), Credential: config.Credential{AgentID: grant.RequesterID}}}
	body, _ := json.Marshal(humanOpen{GrantID: grant.ID, HelpMessageID: "message_help", Provider: "codex"})
	_, err = r.openHumanIn(context.Background(), Request{OperationID: "phone-human-open", Body: body}, f)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("fictional private human input")
	read := make(chan string, 1)
	go func() {
		buffer := make([]byte, len(input))
		n, err := r.HumanRead(r.human.id, buffer)
		if err != nil {
			t.Error(err)
		}
		read <- string(buffer[:n])
	}()
	if _, err := r.HumanWrite(r.human.id, input); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-read:
		if got != string(input) {
			t.Fatal("protected relay did not retain exact bytes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("protected terminal relay did not deliver bytes")
	}
	if err := r.CloseHuman(r.human.id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := r.waitHuman(ctx, r.human.id)
	if err != nil || result.State != "closed" {
		t.Fatal("foreground viewer ended without confirmed server closure", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 1 || opened != 1 || closed != 1 {
		t.Fatal("human viewer repeated creation/open/close", created, opened, closed)
	}
}

func TestSharingHumanViewsInAIModeAndReconnectsTheSelectedTerminal(t *testing.T) {
	r := testRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := local.Open(ctx, r.root+"/runner")
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().Unix()
	session := domain.Session{
		ID: "session_sharing", RequesterID: "agent_founder", TargetID: "agent_target",
		Mode: "tmux", Purpose: "sharing", TerminalID: "first", SelectionRevision: 1,
		State: "consented", Revision: 1, ExpiresAt: now + 300, ServerObservedAt: now,
	}
	var mu sync.Mutex
	created, opened, closed, streams := 0, 0, 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("X-Fdn-API-Version", "2")
		if request.URL.Path == "/v1/sessions/session_sharing/relay/requester" {
			conn, err := websocket.Accept(w, request, nil)
			if err != nil {
				t.Error(err)
				return
			}
			stream := websocket.NetConn(request.Context(), conn, websocket.MessageBinary)
			defer stream.Close()
			mu.Lock()
			streams++
			number := streams
			mu.Unlock()
			if number == 1 {
				if _, err := stream.Write([]byte("first selected terminal")); err != nil {
					t.Error(err)
				}
				mu.Lock()
				session.TerminalID = "second"
				session.SelectionRevision++
				session.Revision++
				mu.Unlock()
				return
			}
			if number != 2 {
				t.Error("viewer opened an unexpected replacement relay", number)
				return
			}
			if _, err := stream.Write([]byte("second selected terminal")); err != nil {
				t.Error(err)
			}
			_, _ = io.Copy(io.Discard, stream)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var data any
		switch request.URL.Path {
		case "/v1/me":
			data = domain.Agent{ID: "agent_founder", Active: true, Founder: true, Mode: "ai"}
		case "/v1/agents/agent_target/sharing":
			if request.Method != http.MethodGet {
				t.Error("opening a passive viewer changed target selection")
			}
			data = domain.Sharing{AgentID: "agent_target", TerminalID: "first", SelectionRevision: 1}
		case "/v1/sessions":
			var input domain.SessionRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.GrantID != "" || input.Purpose != "sharing" || input.TerminalID != "first" ||
				input.SelectionRevision != 1 || input.LifetimeSeconds != domain.MaxSharingSeconds ||
				input.OperationID != "sharing_"+domain.Hash("phone-sharing-open") {
				t.Error("Founder view changed its exact selected-resource proposal", input)
			}
			created++
			data = session
		case "/v1/sessions/session_sharing/access":
			data = session
		case "/v1/sessions/session_sharing":
			if request.Method == http.MethodPost {
				var input domain.SessionChange
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input.ExpectedRevision != session.Revision {
					t.Error("viewer changed the session without a fresh revision")
				}
				switch input.Action {
				case "open":
					opened++
					session.OpenRequested = true
				case "close":
					closed++
					session.State = "closed"
				default:
					t.Error("viewer requested input, a new selection or another action", input.Action)
				}
				session.Revision++
			}
			data = session
		default:
			t.Error("unexpected sharing request", request.Method, request.URL.Path)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Error(err)
		}
		if err := json.NewEncoder(w).Encode(api.Envelope{OK: true, Data: encoded, Warnings: []string{}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	f := &foreground{context: ctx, state: state,
		client: &api.Client{BaseURL: server.URL, HTTP: server.Client(), Credential: config.Credential{AgentID: "agent_founder"}}}
	body, err := json.Marshal(humanOpen{TargetID: "agent_target", Mode: "tmux"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.openHumanIn(ctx, Request{OperationID: "phone-sharing-open", Body: body}, f); err != nil {
		t.Fatal("Founder could not view in AI mode without a grant", err)
	}
	id := r.human.id
	defer r.CloseHuman(id)
	for _, expected := range []string{"first selected terminal", "second selected terminal"} {
		buffer := make([]byte, len(expected))
		n, err := r.HumanRead(id, buffer)
		if err != nil || string(buffer[:n]) != expected {
			t.Fatal("same logical viewer did not follow actual terminal bytes", string(buffer[:n]), err)
		}
	}
	var proposal humanProposal
	found, err := state.Get(ctx, "sharing-open:phone-sharing-open", &proposal)
	if err != nil || !found || proposal.SessionID != "session_sharing" || proposal.Request.TerminalID != "first" {
		t.Fatal("selection switch replaced the original durable proposal", proposal, err)
	}
	if err := r.CloseHuman(id); err != nil {
		t.Fatal(err)
	}
	result, err := r.waitHuman(ctx, id)
	if err != nil || result.State != "closed" || result.ID != proposal.SessionID ||
		result.TerminalID != "second" || result.ExpiresAt != now+300 {
		t.Fatal("reconnection changed the logical view or original expiry", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 1 || opened != 1 || closed != 1 || streams != 2 {
		t.Fatal("selection switch repeated session creation, opening or closure", created, opened, closed, streams)
	}
}
