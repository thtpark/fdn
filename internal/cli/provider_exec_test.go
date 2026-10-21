package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

func TestProviderExecPreservesNativeProbeOutputAndExitStatus(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	c := config.DefaultWorker(filepath.Join(home, "state"))
	executable := filepath.Join(home, "fixture-native")
	if err := secure.Create(executable, []byte("#!/bin/sh\nprintf 'fixture-native version\\n'\nexit 37\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	c.ProviderPaths = map[string]string{"codex": executable}
	file := filepath.Join(home, "agent.json")
	if err := config.Write(file, c); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--version", "--help"} {
		var out, diagnostic bytes.Buffer
		code := RunAgent(context.Background(), []string{"--config", file, "provider", "exec", "--provider", "codex", "--", flag}, bytes.NewReader(nil), &out, &diagnostic)
		if code != 37 || out.String() != "fixture-native version\n" || diagnostic.Len() != 0 {
			t.Fatal("native output or exit status was changed", flag, code, out.String(), diagnostic.String())
		}
	}
	var out, diagnostic bytes.Buffer
	code := RunAgent(context.Background(), []string{"--config", file, "provider", "exec", "--provider", "codex", "--", "arbitrary-prompt"}, bytes.NewReader(nil), &out, &diagnostic)
	var envelope api.Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || code != 6 || envelope.OK {
		t.Fatal("non-human execution was not blocked", code, out.String(), err)
	}
}

func TestTerminalAdmissionPreservesPendingInvocationAndSessionFences(t *testing.T) {
	ctx := context.Background()
	state, err := local.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	r := &resources{config: config.DefaultWorker(state.Root), state: state}
	for _, key := range []string{"maintenance", "session-fence"} {
		if err := state.Put(ctx, key, map[string]bool{"active": true}); err != nil {
			t.Fatal(err)
		}
		if lock, err := terminalAdmission(ctx, r); err == nil {
			lock.Close()
			t.Fatal("interactive launch bypassed", key)
		}
		if err := state.Put(ctx, key, map[string]bool{"active": false}); err != nil {
			t.Fatal(err)
		}
	}
	journal := local.Journal{ID: "uncertain-command", Kind: "execution", State: "uncertain"}
	if err := state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if lock, err := terminalAdmission(ctx, r); err == nil {
		lock.Close()
		t.Fatal("interactive launch bypassed uncertain inference")
	}
	journal.State = "done"
	if err := state.SaveJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	lock, err := terminalAdmission(ctx, r)
	if err != nil {
		t.Fatal("resolved fences did not permit a human session", err)
	}
	lock.Close()
	reopened, err := secure.Acquire(filepath.Join(state.Root, "work.lock"))
	if err != nil {
		t.Fatal("admission left a slot locked", err)
	}
	reopened.Close()
}
