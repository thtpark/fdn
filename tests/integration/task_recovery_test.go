//go:build integration

package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

func recoveryZIP(t *testing.T, exactSize int) []byte {
	t.Helper()
	xlsx := investigationArchive(t, map[string]string{
		"[Content_Types].xml":      `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1"><v>42</v></c></row></sheetData></worksheet>`,
	})
	var nested bytes.Buffer
	writer := zip.NewWriter(&nested)
	part, err := writer.Create("values.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(xlsx); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	build := func(padding int) []byte {
		var outer bytes.Buffer
		writer := zip.NewWriter(&outer)
		for _, entry := range []struct {
			name string
			data []byte
		}{{"nested.zip", nested.Bytes()}, {"padding.bin", make([]byte, padding)}} {
			part, err := writer.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return outer.Bytes()
	}
	base := build(0)
	if exactSize == 0 {
		return base
	}
	if exactSize < len(base) {
		t.Fatal("requested fixture size is smaller than its actual ZIP structure")
	}
	content := build(exactSize - len(base))
	if len(content) != exactSize {
		t.Fatal("synthetic ZIP did not match the reported file's exact byte size")
	}
	return content
}

func recoveryZIPPart(content []byte, name string) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, err
	}
	part, err := archive.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(part, 1<<20))
	return data, errors.Join(err, part.Close())
}

type taskRecoveryExecutor struct {
	base                          executor
	t                             *testing.T
	engine                        *work.Engine
	file                          domain.SharedFile
	content                       []byte
	ref                           string
	cancel                        context.CancelFunc
	blockRecovery, unproved       bool
	omitTaskID                    bool
	pid                           int
	planning, recovery, execution int
}

func (x *taskRecoveryExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	return x.base.Check(ctx, name)
}

func (x *taskRecoveryExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	_, raw, found := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !found {
		x.t.Fatal("recovery omitted source-bound controller context")
	}
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		return provider.Reply{}, err
	}
	if launch.ExecutorAgent == nil || launch.ExecutorAgent.ID != launch.AgentID || launch.ExecutorAgent.Mode != "ai" {
		x.t.Fatal("current executor identity/mode was omitted or confused with the requester")
	}
	if !invocation.Coding {
		x.planning++
		action := &provider.ConversationAction{Instructions: "Unzip the shared file and inspect its nested workbook.", WorkspaceRef: x.ref}
		if x.execution != 0 {
			if len(launch.Message.Attachments) != 0 || len(launch.ReferencedTasks) != 1 {
				x.t.Fatal("plain follow-up did not resolve the existing task from its thread", launch.ReferencedTasks)
			}
			if !x.omitTaskID {
				action.TaskID = launch.ReferencedTasks[0].ID
			}
		}
		return provider.Reply{Outcome: "responded", Summary: "Inspect actual contents under existing authority.", Action: action}, nil
	}
	if launch.Task == nil || launch.Task.ProjectID != domain.AdhocProjectID || launch.ConversationID == "" {
		x.t.Fatal("file continuation lost its original admitted Adhoc task/source")
	}
	destination := filepath.Join(invocation.Workspace, x.file.Name)
	partial := filepath.Join(invocation.Workspace, "nested.zip")
	if launch.Phase == domain.RecoveryRun {
		x.recovery++
		if launch.Recovery == nil || launch.Recovery.SourceRun.ID != launch.Task.LastRunID ||
			!strings.Contains(invocation.Prompt, "not execution replay") {
			x.t.Fatal("recovery did not pin the exact interrupted execution")
		}
		if invocation.Started != nil {
			if err := invocation.Started(654321); err != nil {
				return provider.Reply{}, err
			}
		}
		input, err := os.ReadFile(destination)
		if err != nil {
			return provider.Reply{}, err
		}
		if !bytes.Equal(input, x.content) {
			return provider.Reply{}, errors.New("retained input bytes do not match the verified source")
		}
		previous, err := os.ReadFile(partial)
		if err != nil {
			return provider.Reply{}, err
		}
		expected, err := recoveryZIPPart(input, "nested.zip")
		if err != nil {
			return provider.Reply{}, err
		}
		if !bytes.Equal(previous, expected) {
			return provider.Reply{}, errors.New("actual partial archive output differs from the verified input")
		}
		var receipt filetransfer.Proposal
		found, err := x.engine.State.Get(ctx, "file-download:inspect-"+x.file.ID, &receipt)
		if err != nil || !found || !receipt.Complete || receipt.Receipt == nil || receipt.Receipt.SHA256 != x.file.SHA256 {
			x.t.Fatal("recovery ignored the original verified receipt", found, err)
		}
		if x.blockRecovery {
			return provider.Reply{Outcome: "paused",
				Summary:  "A prior external operation has no authoritative result; reconcile that operation before continuing.",
				Evidence: []string{"Retained local input/output checked; external result remains unknown."}}, nil
		}
		return provider.Reply{Outcome: "reconciled", SafeToRetry: true,
			Summary:  "Verified the original input and partial nested archive; local inspection can continue without repeating an uncertain external action.",
			Evidence: []string{"Verified original receipt and SHA-256", "Compared actual retained nested archive bytes"}}, nil
	}
	if launch.Phase != domain.ExecutionRun {
		x.t.Fatal("unexpected tool-enabled continuation phase", launch.Phase)
	}
	x.execution++
	if x.execution == 1 {
		if _, err := filetransfer.Download(ctx, x.engine.Client, x.engine.State, "inspect-"+x.file.ID, x.file.ID, destination, nil); err != nil {
			return provider.Reply{}, err
		}
		input, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(input, x.content) {
			x.t.Fatal("original file bytes were not verified", err)
		}
		nested, err := recoveryZIPPart(input, "nested.zip")
		if err != nil {
			return provider.Reply{}, err
		}
		if err := os.WriteFile(partial, nested, 0600); err != nil {
			return provider.Reply{}, err
		}
		err = (process.Native{}).Run(ctx, process.Spec{
			Path: "/bin/sleep", Args: []string{"60"}, Dir: invocation.Workspace,
			Env: []string{"PATH=/usr/bin:/bin", "HOME=" + invocation.Workspace}, Stdout: io.Discard, Stderr: io.Discard,
			Started: func(pid int) error {
				x.pid = pid
				if invocation.Started != nil {
					if err := invocation.Started(pid); err != nil {
						return err
					}
				}
				x.cancel()
				return nil
			},
		})
		if x.unproved {
			return provider.Reply{}, &process.CleanupError{Err: errors.Join(err, errors.New("synthetic unproved cleanup"))}
		}
		return provider.Reply{}, err
	}
	if invocation.Started != nil {
		if err := invocation.Started(654322); err != nil {
			return provider.Reply{}, err
		}
	}
	nested, err := os.ReadFile(partial)
	if err != nil {
		return provider.Reply{}, err
	}
	xlsx, err := recoveryZIPPart(nested, "values.xlsx")
	if err != nil {
		return provider.Reply{}, err
	}
	sheet, err := recoveryZIPPart(xlsx, "xl/worksheets/sheet1.xml")
	if err != nil {
		return provider.Reply{}, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(sheet))
	value := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return provider.Reply{}, err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "v" {
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return provider.Reply{}, err
			}
		}
	}
	if value != "42" {
		x.t.Fatal("nested workbook content was not actually inspected", value)
	}
	result := "Nested workbook A1: " + value
	if err := os.WriteFile(filepath.Join(invocation.Workspace, "analysis.txt"), []byte(result), 0600); err != nil {
		return provider.Reply{}, err
	}
	noResponse := false
	return provider.Reply{Outcome: "completed", Summary: result,
		Evidence: []string{"Retained nested archive inspected; workbook cell A1 read"}, ResponseRequested: &noResponse}, nil
}

func prepareTaskRecovery(t *testing.T, exactSize int) (*harness, *work.Engine, *taskRecoveryExecutor, domain.Message, string) {
	t.Helper()
	h := newHarnessWithFiles(t, foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
		func(context.Context, string, []uint32) error { return nil })})
	x := &taskRecoveryExecutor{t: t, content: recoveryZIP(t, exactSize)}
	engine := newEngine(t, h, &x.base)
	engine.Executor, x.engine = x, engine
	skipHealthOnboarding(t, engine)
	workspace := filepath.Join(t.TempDir(), "recovery-workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binding, err := engine.State.BindWorkspace(context.Background(), workspace, true, 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	x.ref = binding.Ref
	x.file, err = h.founder.UploadFile(context.Background(), domain.FileUploadRequest{
		Command: command(), Name: "shared.zip", MediaType: "application/zip",
		Size: int64(len(x.content)), SHA256: domain.Hash(string(x.content)),
	}, bytes.NewReader(x.content))
	if err != nil {
		t.Fatal(err)
	}
	source, err := api.Call[domain.Message](context.Background(), h.founder, "POST", "/v1/messages", map[string]any{
		"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID, "origin": "human", "kind": "direct",
		"body": "Unzip the shared file and investigate its content.", "attachment_ids": []string{x.file.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step(context.Background(), false); err != nil {
		t.Fatal("initial source planning failed", err)
	}
	return h, engine, x, source, workspace
}

func interruptTaskRecovery(t *testing.T, engine *work.Engine, x *taskRecoveryExecutor) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x.cancel = cancel
	_, err := engine.Step(ctx, false)
	if err == nil || x.execution != 1 || x.pid == 0 {
		t.Fatal("fixture did not interrupt an actual started provider process", err)
	}
	var cleanup *process.CleanupError
	if !x.unproved && errors.As(err, &cleanup) {
		t.Fatal("fixture interruption did not establish native joined cleanup", cleanup)
	}
	if err := syscall.Kill(-x.pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("native interrupted process group was not joined", err)
	}
}

func recoveryTasks(t *testing.T, h *harness) []domain.Task {
	t.Helper()
	tasks, err := api.Call[domain.Page[domain.Task]](context.Background(), h.worker, "GET", "/v1/tasks?project="+domain.AdhocProjectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tasks.Items
}

func TestInterruptedZIPReconcilesAndContinuesSameTaskAutomaticallyAfterRestart(t *testing.T) {
	h, engine, x, source, workspace := prepareTaskRecovery(t, 20_210_825)
	interruptTaskRecovery(t, engine, x)
	before := recoveryTasks(t, h)
	if len(before) != 1 || before[0].Attempts != 1 || !before[0].UnsafeRetry {
		t.Fatal("original interruption lost its exact task/retry fence", before)
	}
	engine = restartAuthenticationEngine(t, engine)
	x.engine = engine
	for range 8 {
		if _, err := engine.Step(context.Background(), false); err != nil {
			t.Fatal("automatic native reconciliation/continuation failed", err)
		}
		tasks := recoveryTasks(t, h)
		if tasks[0].Status == domain.Completed {
			break
		}
	}
	tasks := recoveryTasks(t, h)
	if len(tasks) != 1 || tasks[0].ID != before[0].ID || tasks[0].Status != domain.Completed ||
		tasks[0].Attempts != 2 || tasks[0].RecoveryAttempts != 1 || tasks[0].UnsafeRetry || tasks[0].RetryApproval != nil ||
		x.planning != 1 || x.execution != 2 || x.recovery != 1 {
		t.Fatal("recovery requested approval, created a new task or repeated work", tasks, x.planning, x.execution, x.recovery)
	}
	result, err := os.ReadFile(filepath.Join(workspace, "analysis.txt"))
	if err != nil || string(result) != "Nested workbook A1: 42" {
		t.Fatal("resumed nested ZIP/XLSX analysis was not actually completed", string(result), err)
	}
	runs, err := api.Call[[]domain.Run](context.Background(), h.worker, "GET", api.Path("tasks", tasks[0].ID, "runs"), nil)
	if err != nil || len(runs) != 3 {
		t.Fatal("original, recovery and continued execution were not retained separately", runs, err)
	}
	thread, err := api.Call[[]domain.Message](context.Background(), h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
	if err != nil {
		t.Fatal(err)
	}
	final := 0
	for _, message := range thread {
		if message.ReplyToID == source.ID && strings.Contains(message.Body, "Nested workbook A1: 42") {
			final++
		}
	}
	if final != 1 {
		t.Fatal("actual result was not delivered once to the original source")
	}
	for range 3 {
		if _, err := engine.Step(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	if x.execution != 2 || x.recovery != 1 {
		t.Fatal("polling repeated native recovery or execution")
	}
}

func TestUnprovedProviderCleanupCannotStartAutomaticTaskReconciliation(t *testing.T) {
	h, engine, x, _, workspace := prepareTaskRecovery(t, 0)
	x.unproved = true
	interruptTaskRecovery(t, engine, x)
	for range 3 {
		if _, err := engine.Step(context.Background(), false); err == nil {
			t.Fatal("unproved native cleanup fence was bypassed")
		}
	}
	tasks := recoveryTasks(t, h)
	if len(tasks) != 1 || tasks[0].ActiveRun == nil || tasks[0].Attempts != 1 || tasks[0].RecoveryAttempts != 0 ||
		x.recovery != 0 || x.execution != 1 {
		t.Fatal("uncertain original provider was adopted or reinvoked", tasks)
	}
	if _, err := os.Stat(filepath.Join(workspace, "analysis.txt")); !os.IsNotExist(err) {
		t.Fatal("unproved cleanup produced a false completed analysis", err)
	}
}

func TestBlockedRecoveryExplainsActualRiskAndFollowupDoesNotResetTask(t *testing.T) {
	h, engine, x, source, _ := prepareTaskRecovery(t, 0)
	x.blockRecovery = true
	x.omitTaskID = true
	interruptTaskRecovery(t, engine, x)
	for range 5 {
		if _, err := engine.Step(context.Background(), false); err != nil && domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal(err)
		}

	}
	tasks := recoveryTasks(t, h)
	if len(tasks) != 1 || !tasks[0].UnsafeRetry || tasks[0].RecoveryAttempts != 1 || x.recovery != 1 {
		t.Fatal("unknown external result was treated as safe or retried", tasks)
	}
	thread, err := api.Call[[]domain.Message](context.Background(), h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
	if err != nil {
		t.Fatal(err)
	}
	parent := ""
	for _, message := range thread {
		if strings.Contains(message.Body, "external operation has no authoritative result") {
			parent = message.ID
		}
	}
	if parent == "" {
		t.Fatal("automatic recovery did not explain the concrete remaining risk")
	}
	followup, err := api.Call[domain.Message](context.Background(), h.founder, "POST", "/v1/messages", domain.MessageRequest{
		Command: command(), RecipientID: h.workerAgent.ID, Origin: "human", Kind: "direct",
		Body: "Nothing more to resolve. Can you just try it?", ReplyToID: parent, ResponseRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := engine.Step(context.Background(), false); err != nil && domain.AsFault(err).Code != domain.Prerequisite {
			t.Fatal(err)
		}
	}
	tasks = recoveryTasks(t, h)
	if len(tasks) != 1 || tasks[0].Attempts != 1 || tasks[0].RecoveryAttempts != 1 ||
		x.execution != 1 || x.recovery != 1 || x.planning != 2 {
		t.Fatal("plain follow-up replaced the task or reset an exhausted recovery", tasks, x.planning)
	}
	thread, err = api.Call[[]domain.Message](context.Background(), h.founder, "GET", api.Path("messages", followup.ID, "thread"), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range thread {
		if message.ReplyToID == followup.ID && strings.Contains(message.Body, "external operation has no authoritative result") {
			found = true
		}
	}
	if !found {
		t.Fatal("follow-up refusal did not retain the actual risk instead of an unspecified guard")
	}
}

type lostTaskRecoveryAck struct {
	base http.RoundTripper
	mode string
	lost atomic.Bool
}

func (transport *lostTaskRecoveryAck) RoundTrip(request *http.Request) (*http.Response, error) {
	matches := false
	if request.Method == http.MethodPost &&
		(strings.HasSuffix(request.URL.Path, "/claim") || strings.HasSuffix(request.URL.Path, "/report") || request.URL.Path == "/v1/messages") {
		if request.GetBody == nil {
			return nil, errors.New("fixture mutation omitted its replayable body")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		var metadata struct {
			Kind      string `json:"kind"`
			Outcome   string `json:"outcome"`
			Operation string `json:"operation_id"`
		}
		err = json.NewDecoder(body).Decode(&metadata)
		err = errors.Join(err, body.Close())
		if err != nil {
			return nil, err
		}
		switch transport.mode {
		case "claim":
			matches = strings.HasSuffix(request.URL.Path, "/claim") && metadata.Kind == domain.RecoveryRun
		case "report":
			matches = strings.HasSuffix(request.URL.Path, "/report") && metadata.Outcome == "reconciled"
		case "reply":
			matches = request.URL.Path == "/v1/messages" && strings.Contains(metadata.Operation, ".resume.")
		}
	}
	response, err := transport.base.RoundTrip(request)
	if err == nil && matches && transport.lost.CompareAndSwap(false, true) {
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		return nil, errors.Join(errors.New("fixture lost exact recovery acknowledgement"), readErr, closeErr)
	}
	return response, err
}

func TestTaskRecoveryLostAcknowledgementsPreserveOneActualReconciliationAndResult(t *testing.T) {
	for _, mode := range []string{"claim", "report", "reply"} {
		t.Run(mode, func(t *testing.T) {
			h, engine, x, source, _ := prepareTaskRecovery(t, 0)
			interruptTaskRecovery(t, engine, x)
			transport := &lostTaskRecoveryAck{base: fixtureTransport(t, h.worker), mode: mode}
			setFixtureTransport(t, h.worker, transport)
			restarted := false
			for range 14 {
				if _, err := engine.Step(context.Background(), false); err != nil && domain.AsFault(err).Code != domain.Unavailable {
					t.Fatal(mode, "unexpected recovery failure", err)
				}
				if transport.lost.Load() && !restarted {
					engine = restartAuthenticationEngine(t, engine)
					x.engine, restarted = engine, true
				}
				tasks := recoveryTasks(t, h)
				if tasks[0].Status == domain.Completed {
					break
				}
			}
			tasks := recoveryTasks(t, h)
			if !restarted || !transport.lost.Load() || len(tasks) != 1 || tasks[0].Status != domain.Completed ||
				tasks[0].Attempts != 2 || tasks[0].RecoveryAttempts != 1 || x.recovery != 1 || x.execution != 2 || x.planning != 1 {
				t.Fatal("lost acknowledgement stranded work, reset accounting or repeated a provider", mode, tasks, x.recovery, x.execution)
			}
			for range 2 {
				if _, err := engine.Step(context.Background(), false); err != nil {
					t.Fatal(err)
				}
			}
			thread, err := api.Call[[]domain.Message](context.Background(), h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
			if err != nil {
				t.Fatal(err)
			}
			completed := 0
			for _, message := range thread {
				if strings.Contains(message.Body, "Nested workbook A1: 42") {
					completed++
				}
			}
			if completed != 1 || x.recovery != 1 || x.execution != 2 {
				t.Fatal("exact result replay duplicated output or inference", completed)
			}
		})
	}
}

func TestAutomaticTaskReconciliationPreservesFounderPauseAndChangedScope(t *testing.T) {
	for _, change := range []string{"pause", "scope"} {
		t.Run(change, func(t *testing.T) {
			h, engine, x, _, _ := prepareTaskRecovery(t, 0)
			interruptTaskRecovery(t, engine, x)
			if _, err := engine.Step(context.Background(), false); err != nil {
				t.Fatal("stopped result did not reconcile before control change", err)
			}
			tasks := recoveryTasks(t, h)
			if change == "scope" {
				h.change(t, h.founder, tasks[0].ID, "edit", func(request *domain.TaskChange) {
					description := "A newer scope must not inherit old action or recovery authority."
					request.Description = &description
				})
			} else {
				actor, err := api.Call[domain.Agent](context.Background(), h.worker, "GET", "/v1/me", nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := api.Call[domain.Agent](context.Background(), h.founder, "POST", api.Path("agents", actor.ID, "mode"),
					domain.ModeRequest{Command: domain.Command{OperationID: domain.NewID("pause"), ExpectedRevision: actor.Revision},
						Mode: "manual", Reason: "Explicit Founder pause in isolated recovery fixture"}); err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				if _, err := engine.Step(context.Background(), false); err != nil {
					t.Fatal(err)
				}
			}
			if x.recovery != 0 || x.execution != 1 {
				t.Fatal("automatic reconciliation bypassed newer pause or scope intent", change)
			}
		})
	}
}
