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
