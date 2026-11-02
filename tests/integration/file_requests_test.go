//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/provider"
	"github.com/thtpark/fdn/internal/work"
)

type fileRequestExecutor struct {
	base       executor
	t          *testing.T
	engine     *work.Engine
	file       domain.SharedFile
	content    []byte
	preferred  string
	checks     []string
	planning   int
	executions int
}

func (x *fileRequestExecutor) Check(ctx context.Context, name string) (provider.Health, error) {
	x.checks = append(x.checks, name)
	health, err := x.base.Check(ctx, name)
	if name != x.preferred {
		health.Authentication = "login-required"
	} else if name == "codex" {
		health.Authentication = "credentials-present-unverified"
	}
	return health, err
}

func (x *fileRequestExecutor) Invoke(ctx context.Context, invocation provider.Invocation) (provider.Reply, error) {
	_, raw, ok := strings.Cut(invocation.Prompt, "\nFDN_CONTEXT_JSON\n")
	if !ok {
		x.t.Fatal("file request omitted its explicit controller context")
	}
	var launch work.Launch
	if err := json.Unmarshal([]byte(raw), &launch); err != nil {
		x.t.Fatal(err)
	}
	if invocation.Provider != x.preferred || invocation.Model != "fixture-model" || launch.Message == nil {
		x.t.Fatal("file request lost its recipient provider/model or private source", invocation.Provider, invocation.Model, launch)
	}
	if invocation.Started != nil {
		if err := invocation.Started(123456); err != nil {
			return provider.Reply{}, err
		}
	}
	if !invocation.Coding {
		x.planning++
		if launch.Phase != "message" || (!strings.Contains(launch.Message.Body, x.file.ID) &&
			!slices.Contains(launch.Message.Attachments, x.file.FileReference)) {
			x.t.Fatal("file request did not supply the exact private file reference", launch.Message)
		}
		return provider.Reply{Outcome: "responded", Summary: "Read and analyze the authorized private file before reporting a result.",
			Action: &provider.ConversationAction{Instructions: "Read the private file, sum its numbers, and verify the saved analysis."}}, nil
	}
	x.executions++
	if launch.Phase != domain.ExecutionRun || launch.ConversationID == "" || launch.Task == nil ||
		launch.Task.ProjectID != domain.AdhocProjectID || launch.Task.AssigneeID != launch.AgentID {
		x.t.Fatal("file analysis bypassed source-bound Adhoc execution", launch)
	}
	operation := "analyze-" + x.file.ID
	destination := filepath.Join(invocation.Workspace, "input.txt")
	if x.file.TransferTo != "" {
		operation = "receive-" + x.file.ID
		var retained filetransfer.Proposal
		found, err := x.engine.State.Get(ctx, "file-download:"+operation, &retained)
		if err != nil || !found || !retained.Complete || retained.Receipt == nil {
			x.t.Fatal("separately sent file was not durably received before analysis", found, err)
		}
		destination = retained.Destination
	} else if _, err := filetransfer.Download(ctx, x.engine.Client, x.engine.State, operation, x.file.ID, destination, nil); err != nil {
		return provider.Reply{}, err
	}
	content, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(content, x.content) {
		x.t.Fatal("analysis did not read the actual verified private bytes", err)
	}
	var total int
	for _, field := range strings.Fields(string(content)) {
		number, err := strconv.Atoi(field)
		if err != nil {
			return provider.Reply{}, err
		}
		total += number
	}
	result := fmt.Sprintf("Private file total: %d", total)
	output := filepath.Join(invocation.Workspace, "analysis.txt")
	if err := os.WriteFile(output, []byte(result), 0600); err != nil {
		return provider.Reply{}, err
	}
	checked, err := os.ReadFile(output)
	if err != nil || string(checked) != result {
		x.t.Fatal("saved analysis was not verified", err)
	}
	noResponse := false
	return provider.Reply{Outcome: "completed", Summary: result, Evidence: []string{"Verified the input bytes and saved analysis."},
		ResponseRequested: &noResponse}, nil
}

func TestPrivateFileRequestsUseReadyRecipientProviderWithoutSignIn(t *testing.T) {
	for _, preferred := range []string{"codex", "copilot"} {
		for _, delivery := range []string{"attached", "separate-retained", "separate-deleted"} {
			t.Run(preferred+"/"+delivery, func(t *testing.T) {
				// Synthetic enrollment UIDs exercise policy, not native multi-user ACLs.
				h := newHarnessWithFiles(t, foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(
					func(context.Context, string, []uint32) error { return nil })})
				ctx := context.Background()
				x := &fileRequestExecutor{t: t, preferred: preferred, content: []byte("4\n5\n6\n")}
				engine := newEngine(t, h, &x.base)
				engine.Executor, x.engine = x, engine
				engine.Config.Provider, engine.Config.Model = preferred, "fixture-model"
				skipHealthOnboarding(t, engine)
				upload := domain.FileUploadRequest{Command: command(), Name: "numbers.txt", MediaType: "text/plain",
					Size: int64(len(x.content)), SHA256: domain.Hash(string(x.content))}
				if delivery != "attached" {
					upload.TransferTo, upload.DeleteAfterDownload = h.workerAgent.ID, delivery == "separate-deleted"
				}
				file, err := h.founder.UploadFile(ctx, upload, bytes.NewReader(x.content))
				if err != nil {
					t.Fatal(err)
				}
				x.file = file
				if delivery != "attached" {
					status, err := engine.Step(ctx, false)
					if err != nil || status.State != "file-received" || len(x.checks) != 0 ||
						x.planning != 0 || x.executions != 0 || len(status.AuthenticationRequests) != 0 {
						t.Fatal("private receipt required a provider, onboarding or sign-in", status, err)
					}
				}
				input := map[string]any{"operation_id": command().OperationID, "recipient_id": h.workerAgent.ID,
					"origin": "human", "kind": "direct", "body": "Analyze the numbers in private file " + file.ID + " and save their total."}
				if delivery == "attached" {
					input["attachment_ids"] = []string{file.ID}
				}
				source, err := api.Call[domain.Message](ctx, h.founder, "POST", "/v1/messages", input)
				if err != nil || !source.ResponseRequest {
					t.Fatal("file request did not retain the default response requirement", source, err)
				}
				if len(x.checks) != 0 {
					t.Fatal("sending a file/message probed provider authentication", x.checks)
				}
				if status, err := engine.Step(ctx, false); err != nil || status.State != "message" ||
					x.planning != 1 || x.executions != 0 || len(status.AuthenticationRequests) != 0 {
					t.Fatal("private file was not planned without unrelated sign-in", status, err)
				}
				engine = restartAuthenticationEngine(t, engine)
				x.engine = engine
				if status, err := engine.Step(ctx, false); err != nil || x.executions != 1 || len(status.AuthenticationRequests) != 0 {
					t.Fatal("file analysis failed after a restart or requested sign-in", status, err)
				}
				thread, err := api.Call[[]domain.Message](ctx, h.founder, "GET", api.Path("messages", source.ID, "thread"), nil)
				if err != nil || len(thread) != 2 {
					t.Fatal("actual file analysis was not returned privately", thread, err)
				}
				var result domain.Message
				for _, message := range thread {
					if message.ReplyToID == source.ID {
						result = message
					}
				}
				if result.SenderID != h.workerAgent.ID || result.RecipientID != h.founderAgent.ID ||
					result.ResponseRequest || !strings.Contains(result.Body, "Private file total: 15") {
					t.Fatal("file response lost its recipient, actual analysis or explicit response opt-out", result)
				}
				if _, err := api.Call[domain.SharedFile](ctx, h.other, "GET", api.Path("files", file.ID), nil); err == nil || domain.AsFault(err).Code != domain.Denied {
					t.Fatal("private analysis broadened file readership", err)
				}
				engine = restartAuthenticationEngine(t, engine)
				x.engine = engine
				for range 2 {
					if status, err := engine.Step(ctx, false); err != nil || len(status.AuthenticationRequests) != 0 {
						t.Fatal("completed file request restarted authentication or work", status, err)
					}
				}
				if x.planning != 1 || x.executions != 1 || slices.ContainsFunc(x.checks, func(name string) bool { return name != preferred }) ||
					len(authenticationInbox(t, h.founder)) != 0 || len(authenticationInbox(t, h.other)) != 0 {
					t.Fatal("file request repeated inference or probed/escalated another provider", x.checks, x.planning, x.executions)
				}
			})
		}
	}
}
