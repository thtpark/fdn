package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	root, err := os.MkdirTemp("..", ".mobile-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func call(t *testing.T, runtime *Runtime, request Request) api.Envelope {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var response api.Envelope
	if err := json.Unmarshal(runtime.Call(data), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestMobileOfflinePreparationRetainsKeysAndNeverStartsAController(t *testing.T) {
	r := testRuntime(t)
	first := call(t, r, Request{ID: "prepare-1", Op: "runtime.prepare", Name: "Founder phone"})
	second := call(t, r, Request{ID: "prepare-2", Op: "runtime.prepare", Name: "Founder phone"})
	if !first.OK || !second.OK || string(first.Data) != string(second.Data) || strings.Contains(string(first.Data), "PRIVATE KEY") {
		t.Fatal("public preparation was not exact, public-only or successful", first.Error, second.Error)
	}
	status := call(t, r, Request{ID: "status", Op: "runtime.status"})
	var state map[string]any
	if err := json.Unmarshal(status.Data, &state); err != nil || state["controller"] != false || state["provider_execution"] != false || state["state"] != "inactive" {
		t.Fatal("mobile runtime falsely claimed a worker", state, err)
	}
	changed := call(t, r, Request{ID: "prepare-3", Op: "runtime.prepare", Name: "Another identity"})
	if changed.Error == nil || changed.Error.Code != domain.Conflict {
		t.Fatal("preparation replaced another retained identity", changed)
	}
}

func TestMobileCancellationBeforeDispatchAndUnknownOperationsStayFailClosed(t *testing.T) {
	r := testRuntime(t)
	r.Cancel("not-yet-started")
	canceled := call(t, r, Request{ID: "not-yet-started", Op: "runtime.prepare", Name: "Must not exist"})
	if canceled.Error == nil || canceled.Error.Code != domain.Cancelled {
		t.Fatal("early cancellation was lost before dispatch", canceled)
	}
	if _, err := os.Stat(filepath.Join(r.root, "identity", "device.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled preparation wrote identity keys", err)
	}
	for _, op := range []string{"work run", "provider exec", "service install", "reset", "recover"} {
		response := call(t, r, Request{ID: op, Op: op})
		if response.Error == nil || response.Error.Code != domain.Denied {
			t.Fatal("unsupported mobile operation was accepted", op, response)
		}
	}

}

func TestMobileCanonicalRootDoesNotAdoptALeafSymlink(t *testing.T) {
	r := testRuntime(t)
	link := filepath.Join(filepath.Dir(r.root), filepath.Base(r.root)+"-link")
	if err := os.Symlink(r.root, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
	})
	if other, err := New(link); err == nil {
		other.Close()
		t.Fatal("mobile runtime adopted a symlink as app-private state")
	}
	canonical, err := filepath.EvalSymlinks(r.root)
	if err != nil || r.root != canonical {
		t.Fatal("mobile runtime did not retain the canonical app path", err)
	}
}
