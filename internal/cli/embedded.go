package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/observe"
)

// Embedded reuses CLI dispatch and exact durable proposals, but never loads a
// desktop controller, process, provider, service installer, or runtime receiver.
type Embedded struct {
	res    resources
	status func() any
}

func NewEmbedded(c config.Worker, state *local.State, client *api.Client, status func() any) (*Embedded, error) {
	if state == nil || client == nil || !client.Local || status == nil {
		return nil, domain.Fail(domain.Invalid, "embedded CLI requires app-owned local identity, state and foreground status")
	}
	return &Embedded{res: resources{config: c, state: state, client: client}, status: status}, nil
}

func (e *Embedded) source() observationSource {
	return observationSource{app: &app{nonInteractive: true, json: true, diagnostic: io.Discard}, res: &e.res}
}

func EmbeddedOffline(ctx context.Context, q observe.Query) (json.RawMessage, error) {
	if !slices.Contains([]string{"version", "help", "guides", "guide"}, q.Kind) {
		return nil, domain.Fail(domain.Prerequisite, "an active enrolled mobile connection is required")
	}
	source := observationSource{app: &app{nonInteractive: true, json: true, diagnostic: io.Discard}}
	return source.Snapshot(ctx, q)
}

func (e *Embedded) Identity(ctx context.Context) (domain.AgentIdentity, error) {
	return e.source().Identity(ctx)
}
func (e *Embedded) Updates(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Update], error) {
	return e.source().Updates(ctx, cursor, filter, wait)
}
func (e *Embedded) Events(ctx context.Context, cursor string, filter domain.UpdateFilter, wait bool) (domain.Page[domain.Event], error) {
	return e.source().Events(ctx, cursor, filter, wait)
}

var embeddedReads = strings.Fields("version help status identity work agents agent projects project tasks task taskHistory runs discussion entry inbox message messageHistory thread threadPage messageDiagnostics mentions note noteHistory noteContext knowledge knowledgeItem knowledgeHistory sessions session grants grant devices diagnostics operation configuration guides guide events")

func (e *Embedded) ValidateQuery(q observe.Query) error {
	if err := q.Validate(); err != nil {
		return err
	}
	if !slices.Contains(embeddedReads, q.Kind) {
		return domain.Fail(domain.Prerequisite, "this foreground mobile runtime does not support desktop providers, workspaces, screens, inventory, native services or upgrades")
	}
	return nil
}

func (e *Embedded) Snapshot(ctx context.Context, q observe.Query) (json.RawMessage, error) {
	if err := e.ValidateQuery(q); err != nil {
		return nil, err
	}
	switch q.Kind {
	case "work":
		return json.Marshal(e.status())
	case "status":
		me, err := api.Call[domain.Agent](ctx, e.res.client, "GET", "/v1/me", nil)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"agent": me, "local": e.status()})
	case "configuration":
		return json.Marshal(map[string]any{"runtime": "foreground-founder", "controller": false, "provider_execution": false})
	default:
		return e.source().Snapshot(ctx, q)
	}
}

var embeddedWrites = []string{
	"agent status", "agent mode", "project create", "project change", "task create", "task change",
	"comment add", "comment edit", "worklog add", "message send", "message read", "message receipt",
	"message request-report", "note set", "mention acknowledge", "knowledge put",
}

func (e *Embedded) Mutate(ctx context.Context, args []string, body json.RawMessage, operation string, revision int64) (json.RawMessage, error) {
	if len(args) < 2 || len(args) > 10 || len(body) > domain.MaxBody || !domain.ValidOperation(operation) || revision < 0 ||
		!slices.Contains(embeddedWrites, args[0]+" "+args[1]) {
		return nil, domain.Fail(domain.Denied, "command is outside the mobile manual-content capability")
	}
	for _, arg := range args {
		if len(arg) > 512 || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, domain.Fail(domain.Invalid, "invalid embedded command argument")
		}
	}
	targeted := slices.Contains([]string{
		"agent mode", "project change", "task change", "comment add", "comment edit", "worklog add",
		"message read", "message receipt", "message request-report", "mention acknowledge",
	}, args[0]+" "+args[1])
	expected := 4
	if targeted {
		expected++
	}
	if args[0] == "message" && args[1] == "read" {
		expected = 3
	} else if len(args) < 2 || args[len(args)-2] != "--input" || args[len(args)-1] != "-" {
		return nil, domain.Fail(domain.Denied, "mobile inputs must be bounded in-memory JSON, never file paths")
	}
	if len(args) != expected {
		return nil, domain.Fail(domain.Invalid, "mobile command has unexpected options")
	}
	me, err := api.Call[domain.Agent](ctx, e.res.client, "GET", "/v1/me", nil)
	if err != nil {
		return nil, err
	}
	if !me.Founder || !me.Active || me.ID != e.res.client.Credential.AgentID {
		return nil, domain.Fail(domain.Denied, "mobile content requires this device's active Founder identity")
	}
	pause := args[0] == "agent" && args[1] == "mode"
	if me.Mode != "manual" && !pause {
		return nil, domain.Fail(domain.Prerequisite, "human publishing requires freshly confirmed manual mode")
	}
	a := &app{in: bytes.NewReader(body), out: io.Discard, diagnostic: io.Discard, json: true,
		nonInteractive: true, operation: operation, explicitOperation: true, expectedRevision: revision}
	var value any
	switch args[0] {
	case "agent":
		value, err = a.agentCommand(ctx, &e.res, args[1:])
	case "project":
		value, err = a.projectCommand(ctx, &e.res, args[1:])
	case "task":
		value, err = a.taskCommand(ctx, &e.res, args[1:])
	case "comment", "worklog":
		value, err = a.discussionCommand(ctx, &e.res, args[0], args[1:])
	case "message":
		value, err = a.messageCommand(ctx, &e.res, args[1:])
	case "note":
		value, err = a.noteCommand(ctx, &e.res, args[1:])
	case "mention":
		value, err = a.mentionCommand(ctx, &e.res, args[1:])
	case "knowledge":
		value, err = a.localCommand(ctx, &e.res, args[0], args[1:])
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
