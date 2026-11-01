package cli

import (
	"context"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/observe"
)

func TestEmbeddedCLIFailsClosedBeforeDesktopOrFileInputDispatch(t *testing.T) {
	e := &Embedded{}
	for _, args := range [][]string{
		{"work", "run"}, {"provider", "exec"}, {"service", "install"}, {"agent", "register"},
		{"device", "register"}, {"session", "open"}, {"upgrade", "apply"},
		{"agent", "add"}, {"agent", "request-sign-in"}, {"assistance", "grant"}, {"assistance", "open"},
		{"message", "send", "--input", "/private/must-not-read"},
		{"file", "upload", "--path", "/private/must-not-read"},
		{"file", "download", "file_fixture", "--output", "/private/must-not-write"},
		{"file", "begin", "--input", "/private/must-not-read"},
		{"file", "put-chunk", "file_fixture", "--input", "/private/must-not-read"},
		{"comment", "add", "task_fixture", "--input", "-",
			"--config", "/private/must-not-read"},
	} {
		if _, err := e.Mutate(context.Background(), args, []byte(`{}`), domain.NewID("op"), 0); err == nil {
			t.Fatal("embedded CLI admitted an unsupported capability", args)
		}
		for _, kind := range []string{"files", "file", "fileChunk", "publications", "publication", "publicationVersions", "publicationVersion"} {
			if err := e.ValidateQuery(observe.Query{Kind: kind, ID: "fixture"}); err != nil {
				t.Fatal("embedded runtime omitted a bounded file/publication read", kind, err)
			}
		}
	}
	for _, kind := range []string{"provider", "inventory", "inventoryRefresh", "workspaces", "screens", "upgrade", "doctor", "preferences"} {
		if err := e.ValidateQuery(observe.Query{Kind: kind}); domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal("mobile runtime invented a desktop read projection", kind, err)
		}
	}
}
