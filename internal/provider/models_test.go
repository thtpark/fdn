package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
)

type modelCatalogRunner struct {
	catalogFailure  bool
	unauthenticated bool
	paginate        bool
	unprovedCleanup bool
	methods         []string
}

func (r *modelCatalogRunner) Run(ctx context.Context, spec process.Spec) (err error) {
	if slices.Contains(spec.Args, "--version") {
		_, err := io.WriteString(spec.Stdout, "fixture-cli 1.2.3\n")
		return err
	}
	if slices.Contains(spec.Args, "--help") {
		return (&fixtureRunner{}).Run(ctx, spec)
	}
	if slices.Contains(spec.Args, "login") {
		return nil
	}
	lineMode := len(spec.Args) != 0 && spec.Args[0] == "app-server"
	if !lineMode && !slices.Contains(spec.Args, "--headless") {
		return errors.New("catalog check attempted an inference or login")
	}
	if r.unprovedCleanup {
		defer func() { err = &process.CleanupError{Err: errors.Join(err, context.Canceled)} }()
	}
	reader := bufio.NewReader(spec.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		var data []byte
		if lineMode {
			data = []byte(line)
		} else {
			length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length: ")))
			if err != nil || length < 1 || length > 4096 {
				return errors.New("invalid fixture catalog frame")
			}
			if separator, err := reader.ReadString('\n'); err != nil || separator != "\r\n" {
				return errors.New("invalid fixture frame separator")
			}
			data = make([]byte, length)
			if _, err := io.ReadFull(reader, data); err != nil {
				return err
			}
		}
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return err
		}
		r.methods = append(r.methods, request.Method)
		var result any
		switch request.Method {
		case "connect":
			result = map[string]int{"protocolVersion": 3}
		case "initialize":
			result = map[string]string{"userAgent": "fixture"}
		case "initialized":
			continue
		case "auth.getStatus":
			result = map[string]bool{"isAuthenticated": !r.unauthenticated}
		case "models.list":
			result = map[string]any{"models": []map[string]string{{"id": "available-model"}}}
		case "model/list":
			if request.Params["includeHidden"] != false {
				return errors.New("model probe changed catalog scope")
			}
			result = map[string]any{"data": []map[string]string{{"id": "display-id", "model": "available-model"}}, "nextCursor": nil}
			if r.paginate && request.Params["cursor"] == nil {
				result = map[string]any{"data": []map[string]string{}, "nextCursor": "next-fixture"}
			}
		default:
			return errors.New("catalog probe sent an unauthorized RPC")
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		if r.catalogFailure && (request.Method == "model/list" || request.Method == "models.list") {
			response = map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "PRIVATE_CATALOG_DIAGNOSTIC"}}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if lineMode {
			_, err = fmt.Fprintln(spec.Stdout, string(encoded))
		} else {
			_, err = fmt.Fprintf(spec.Stdout, "Content-Length: %d\r\n\r\n%s", len(encoded), encoded)
		}
		if err != nil {
			return err
		}
	}
}

func TestModelCatalogChecksAreReadOnlyAndFailureDelegatesSelection(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", name, fail), func(t *testing.T) {
				c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
				c.ProviderPaths[name] = "/fixture/" + name
				runner := &modelCatalogRunner{catalogFailure: fail, paginate: true}
				m := &Manager{Config: c, Runner: runner}
				health, err := m.Check(context.Background(), name)
				if err != nil || (name == "copilot" && health.Authentication != "authenticated") ||
					(name == "codex" && health.Authentication != "credentials-present-unverified") {
					t.Fatal("catalog check altered authentication or required inference", health, err)
				}
				if fail {
					if health.ModelCatalogError == "" || len(health.Models) != 0 || strings.Contains(health.ModelCatalogError, "PRIVATE") {
						t.Fatal("catalog failure was hidden or exposed private diagnostics", health)
					}
				} else if !slices.Equal(health.Models, []string{"available-model"}) || health.ModelCatalogError != "" {
					t.Fatal("fresh catalog was not used", health, runner.methods)
				}
			})
		}
	}
}

type codexCancelRunner struct{ entered, stopped chan struct{} }

func (r codexCancelRunner) Run(ctx context.Context, spec process.Spec) error {
	defer close(r.stopped)
	if _, err := bufio.NewReader(spec.Stdin).ReadString('\n'); err != nil {
		return err
	}
	close(r.entered)
	<-ctx.Done()
	_, _ = io.Copy(io.Discard, spec.Stdin)
	return ctx.Err()
}

func TestModelCatalogUnprovedCleanupIsNotModelFallback(t *testing.T) {
	for _, name := range []string{"codex", "copilot"} {
		c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
		c.ProviderPaths[name] = "/fixture/" + name
		runner := &modelCatalogRunner{unprovedCleanup: true}
		health, err := (&Manager{Config: c, Runner: runner}).Check(context.Background(), name)
		var cleanup *process.CleanupError
		if !errors.As(err, &cleanup) || health.ModelCatalogError != "" {
			t.Fatal("unproved native cleanup was turned into successful model fallback", name, health, err)
		}
	}
}

func TestModelCatalogAuthenticationFailureDoesNotMasqueradeAsFallback(t *testing.T) {
	c := config.DefaultWorker(filepath.Join(t.TempDir(), "state"))
	c.ProviderPaths["copilot"] = "/fixture/copilot"
	runner := &modelCatalogRunner{unauthenticated: true}
	health, err := (&Manager{Config: c, Runner: runner}).Check(context.Background(), "copilot")
	if err != nil || health.Authentication != "login-required" || health.ModelCatalogError != "" ||
		slices.Contains(runner.methods, "models.list") {
		t.Fatal("missing authentication was hidden as a model fallback", health, runner.methods, err)
	}
	for _, name := range []string{"codex", "copilot"} {
		args, err := Arguments(name, "/fixture/workspace", "/fixture/output", "", true)
		if err != nil || slices.Contains(args, "--model") {
			t.Fatal("provider-selected fallback still forced a model", name, args, err)
		}
	}
}

func TestCodexModelCatalogCancellationJoinsOwnedProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := codexCancelRunner{make(chan struct{}), make(chan struct{})}
	manager := &Manager{Runner: runner}
	done := make(chan error, 1)
	go func() {
		_, err := manager.codexModels(ctx, "/fixture/codex")
		done <- err
	}()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("catalog did not send initialize")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("catalog lost cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog failed to close owned pipes")
	}
	select {
	case <-runner.stopped:
	default:
		t.Fatal("catalog returned without joining its owned process")
	}
}
