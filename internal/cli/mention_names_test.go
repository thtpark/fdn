package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
)

func TestDiscussionHelpDocumentsAgentNameAndIDMentionsOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, command := range [][]string{{"comment", "add"}, {"comment", "edit"}, {"worklog", "add"}} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := RunAgent(context.Background(), append(command, "--help"), strings.NewReader(""), &out, &diagnostic)
			var envelope api.Envelope
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || code != 0 || !envelope.OK {
				t.Fatal("offline discussion help failed", code, err, diagnostic.String())
			}
			var data struct {
				Usage string `json:"usage"`
			}
			if err := json.Unmarshal(envelope.Data, &data); err != nil || !strings.Contains(data.Usage, "@{NAME}") ||
				!strings.Contains(data.Usage, "@{AGENT_ID}") || !strings.Contains(data.Usage, "case-insensitive") {
				t.Fatal("name/ID mention contract missing from help", data, err)
			}
		})
	}
}
