package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/local"
	"github.com/thtpark/fdn/internal/secure"
)

type transferFixture struct {
	base          *pushFixture
	files         map[string]domain.SharedFile
	content       map[string][]byte
	revoked       map[string]bool
	receipts      map[string]domain.FileDownloadReceipt
	receiptBodies map[string][]byte
	chunks        int
	notifications int
	lostAck       bool
	beforeChunk   func(*http.Request)
	owned         []domain.Task
	sessions      []domain.Session
	senderName    string
	senderLookups int
}

func newTransferFixture(t *testing.T) *transferFixture {
	t.Helper()
	f := &transferFixture{base: newPushFixture(t), files: map[string]domain.SharedFile{},
		content: map[string][]byte{}, revoked: map[string]bool{}, receipts: map[string]domain.FileDownloadReceipt{},
		receiptBodies: map[string][]byte{}, owned: []domain.Task{}, sessions: []domain.Session{}, senderName: "Sender"}
	f.base.engine.Client.HTTP.Transport = f
	return f
}

func (f *transferFixture) addFile(id string, content []byte) domain.Message {
	file := domain.SharedFile{FileReference: domain.FileReference{ID: id, Name: "private.bin", MediaType: "application/octet-stream",
		Size: int64(len(content)), SHA256: domain.Hash(string(content))}, OwnerID: "agent_sender",
		TransferTo: f.base.me.ID, DeleteAfterDownload: true, State: "ready", Revision: 2}
	notice := domain.Message{ID: "message_" + id, SenderID: file.OwnerID, RecipientID: f.base.me.ID,
		Origin: "service", Kind: "direct", State: "pending", Revision: 1, Attachments: []domain.FileReference{file.FileReference}}
	f.files[id], f.content[id], f.base.messages[notice.ID] = file, bytes.Clone(content), notice
	return notice
}

func (f *transferFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.base.mu.Lock()
	if r.Method == http.MethodGet {
		for _, file := range f.files {
			if r.URL.Path == "/v1/agents/"+file.OwnerID {
				f.senderLookups++
				sender := domain.Agent{ID: file.OwnerID, Name: f.senderName, Active: true}
				f.base.mu.Unlock()
				return f.base.response(sender, nil)
			}
		}
		switch r.URL.Path {
		case "/v1/messages":
			items := []domain.Message{}
			for _, notice := range f.base.messages {
				if notice.RecipientID == f.base.me.ID && notice.State == "pending" && (r.URL.Query().Get("urgent") != "true" || notice.Urgent) {
					items = append(items, notice)
				}
			}
			slices.SortFunc(items, func(a, b domain.Message) int { return strings.Compare(a.ID, b.ID) })
			f.base.mu.Unlock()
			return f.base.response(domain.Page[domain.Message]{Items: items}, nil)
		case "/v1/ownership":
			items := slices.Clone(f.owned)
			f.base.mu.Unlock()
			return f.base.response(items, nil)
		case "/v1/sessions":
			items := slices.Clone(f.sessions)
			f.base.mu.Unlock()
			return f.base.response(items, nil)
		}
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/files/") {
		f.base.mu.Unlock()
		return f.base.RoundTrip(r)
	}
	parts := strings.Split(r.URL.Path, "/")
	id := parts[3]
	file, found := f.files[id]
	if !found || f.revoked[id] {
		f.base.mu.Unlock()
		return f.base.response(nil, domain.Fail(domain.Denied, "private fixture grant withdrawn"))
	}
	if r.Method == http.MethodGet && len(parts) == 4 {
		f.base.mu.Unlock()
		return f.base.response(file, nil)
	}
	if r.Method == http.MethodGet && parts[4] == "chunks" {
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil || file.State != "ready" || offset < 0 || offset >= file.Size {
			f.base.mu.Unlock()
			return f.base.response(nil, domain.Fail(domain.Prerequisite, "fixture content unavailable"))
		}
		content := bytes.Clone(f.content[id][offset:min(file.Size, offset+domain.FileChunkSize)])
		f.chunks++
		hook := f.beforeChunk
		f.beforeChunk = nil
		f.base.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		return f.base.response(domain.FileChunk{FileID: id, Offset: offset, Data: content, SHA256: domain.Hash(string(content))}, nil)
	}
	if r.Method == http.MethodPost && parts[4] == "downloaded" {
		var req domain.FileDownloadRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			f.base.mu.Unlock()
			return nil, err
		}
		body, err := json.Marshal(req)
		if err != nil {
			f.base.mu.Unlock()
			return nil, err
		}
		var proposal filetransfer.Proposal
		found, readErr := f.base.engine.State.Get(r.Context(), "file-download:"+req.OperationID, &proposal)
		if readErr != nil || !found || !proposal.Complete {
			f.base.t.Error("receipt preceded durable local verification", found, readErr)
		}
		destination := proposal.Destination
		saved, readErr := os.ReadFile(destination)
		info, statErr := os.Lstat(destination)
		if readErr != nil || statErr != nil || info.Mode().Perm() != 0600 || int64(len(saved)) != file.Size ||
			domain.Hash(string(saved)) != file.SHA256 || req.Size != file.Size || req.SHA256 != file.SHA256 || req.OperationID != "receive-"+id {
			f.base.t.Error("transfer acknowledged before exact owner-only local commit", readErr, statErr, req)
		}
		receipt, replay := f.receipts[req.OperationID]
		if replay && !bytes.Equal(f.receiptBodies[req.OperationID], body) {
			f.base.t.Error("uncertain transfer receipt changed its original proposal")
		}
		if !replay {
			f.notifications++
			receipt = domain.FileDownloadReceipt{FileID: id, ReaderID: f.base.me.ID, Size: file.Size, SHA256: file.SHA256,
				ReceivedAt: time.Now().Unix(), NotificationID: "message_receipt_" + id, ContentDeleted: true}
			f.receipts[req.OperationID], f.receiptBodies[req.OperationID] = receipt, bytes.Clone(body)
			file.State, file.Revision = "deleted", file.Revision+1
			f.files[id], f.content[id] = file, nil
		}
		lost := f.lostAck
		f.lostAck = false
		f.base.mu.Unlock()
		if lost {
			return f.base.response(nil, domain.Fail(domain.Unavailable, "fixture lost committed download receipt"))
		}
		return f.base.response(receipt, nil)
	}
	f.base.mu.Unlock()
	f.base.t.Errorf("unexpected file transfer action: %s %s", r.Method, r.URL.Path)
	return f.base.response(nil, domain.Fail(domain.Invalid, "unsupported fixture file action"))
}

func TestPrivateTransferWaitsForAIThenReceivesWithoutOnboardingOrInference(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	f.base.me.Mode, f.base.me.Revision = "manual", 2
	f.base.me.ModeRequestedBy, f.base.me.ModeReason = "agent_founder", "Temporary Founder pause"
	content := bytes.Repeat([]byte("private-fixture"), domain.FileChunkSize/7)
	notice := f.addFile(domain.NewID("file"), content)
	if notice.ResponseRequest {
		t.Fatal("fixture does not reproduce the original non-response-requested transfer")
	}
	if err := f.base.engine.State.Put(ctx, "initial-review", "onboarding-not-yet-reviewed"); err != nil {
		t.Fatal(err)
	}
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "manual" || f.chunks != 0 || f.notifications != 0 || f.base.readEffects != 0 {
		t.Fatal("manual transfer executed or became a provider request", status, err)
	}
	f.base.me.Mode, f.base.me.Revision, f.base.me.ModeReason = "ai", 3, "Founder restores AI intent"
	f.beforeChunk = func(*http.Request) {
		f.base.mu.Lock()
		defer f.base.mu.Unlock()
		if f.base.messages[notice.ID].ReadAt == 0 {
			t.Error("transfer bytes were processed before acknowledging its admitted notice")
		}
	}
	status, err = f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" || f.notifications != 1 || f.chunks < 2 {
		t.Fatal("resumed AI did not deterministically receive the pending transfer", status, err)
	}
	if f.base.executor.checks.Load() != 0 || f.base.executor.invocations.Load() != 0 || status.InitialReviewDone {
		t.Fatal("private transfer depended on provider authentication or initial AI review")
	}
	if handled := f.base.messages[notice.ID]; handled.State != "handled" || handled.ReadAt == 0 || handled.ResponseRequest {
		t.Fatal("admitted transfer notice was not read and handled independently of an AI reply", handled)
	}
	directory := filepath.Join(f.base.engine.Config.WorkspaceRoot, "downloads", "sender-agent_sender", notice.Attachments[0].ID)
	if info, err := os.Lstat(directory); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("automatic destination directory is not recipient-owned/private", err)
	}
	var proposal filetransfer.Proposal
	found, err := f.base.engine.State.Get(ctx, "file-download:receive-"+notice.Attachments[0].ID, &proposal)
	if err != nil || !found || !proposal.Complete || !proposal.Automatic || proposal.Receipt == nil || !proposal.Receipt.ContentDeleted {
		t.Fatal("verified local receipt was not retained durably", proposal, err)
	}
	if proposal.Destination != filepath.Join(directory, "private.bin") {
		t.Fatal("automatic receipt did not preserve the readable file in the local workspace", proposal.Destination)
	}
}

func TestPrivateTransferReadAcknowledgementSettlesBeforeDownloading(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	notice := f.addFile(domain.NewID("file"), []byte("private-fixture"))
	f.base.lostReadAck = true
	if _, err := f.base.engine.Step(ctx, false); err == nil {
		t.Fatal("lost notice read acknowledgement was hidden")
	}
	if f.chunks != 0 || f.notifications != 0 || f.base.executor.invocations.Load() != 0 {
		t.Fatal("file processing started before read acknowledgement settled")
	}
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" {
		t.Fatal("exact notice read replay did not allow the admitted transfer", status, err)
	}
	if f.base.readEffects != 1 || len(f.base.readBodies) != 2 ||
		!bytes.Equal(f.base.readBodies[0], f.base.readBodies[1]) || f.base.messages[notice.ID].ReadAt == 0 {
		t.Fatal("file notice read replay changed its proposal or repeated its effect")
	}
}

func TestPrivateTransferLostReceiptSurvivesRestartWithoutRedownloadOrDuplicateNotification(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	notice := f.addFile(domain.NewID("file"), []byte("private durable restart fixture"))
	f.lostAck = true
	status, err := f.base.engine.Step(ctx, false)
	if domain.AsFault(err).Code != domain.Unavailable || f.notifications != 1 || status.State == "file-received" {
		t.Fatal("uncertain receipt was hidden or retried automatically", status, err)
	}
	root := f.base.engine.State.Root
	c, client := f.base.engine.Config, f.base.engine.Client
	if err := f.base.engine.State.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := local.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.base.engine = New(c, "", client, state, f.base.executor)
	f.base.engine.Config.WorkspaceRoot = filepath.Join(t.TempDir(), "changed-workspace")
	f.senderName = "Renamed sender"
	chunks := f.chunks
	status, err = f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" || f.notifications != 1 || f.chunks != chunks || f.base.messages[notice.ID].State != "handled" {
		t.Fatal("exact receipt replay repeated a download/deletion/notification or lost notice completion", status, err)
	}
	var proposal filetransfer.Proposal
	if found, err := state.Get(ctx, "file-download:receive-"+notice.Attachments[0].ID, &proposal); err != nil || !found {
		t.Fatal("completed receive lost its pinned workspace destination", err)
	}
	if f.senderLookups != 1 || !strings.HasPrefix(proposal.Destination, c.WorkspaceRoot+string(filepath.Separator)) {
		t.Fatal("replay chose a new destination after workspace/sender changes", proposal.Destination, f.senderLookups)
	}
	files, err := os.ReadDir(filepath.Dir(proposal.Destination))
	if err != nil || len(files) != 1 || strings.HasPrefix(files[0].Name(), ".fdn-download-") {
		t.Fatal("known staging files survived completed receipt replay", files, err)
	}
}

func TestPrivateTransferPreservesLegacyExplicitDownloadJournalOnReceiptReplay(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	content := []byte("legacy committed recipient copy")
	notice := f.addFile(domain.NewID("file"), content)
	id := notice.Attachments[0].ID
	directory := filepath.Join(f.base.engine.Config.StateDir, "transfers")
	if err := secure.Dir(directory); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, id+".bin")
	if err := os.WriteFile(destination, content, 0600); err != nil {
		t.Fatal(err)
	}
	proposal := filetransfer.Proposal{Destination: destination, File: f.files[id], Complete: true}
	if err := f.base.engine.State.Put(ctx, "file-download:receive-"+id, proposal); err != nil {
		t.Fatal(err)
	}
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" || f.chunks != 0 || f.notifications != 1 {
		t.Fatal("legacy exact committed file was redownloaded or could not reconcile", status, err)
	}
	var req domain.FileDownloadRequest
	if err := json.Unmarshal(f.receiptBodies["receive-"+id], &req); err != nil || req.Automatic {
		t.Fatal("legacy explicit proposal acquired a new automatic receipt fingerprint", req, err)
	}
	if f.senderLookups != 0 {
		t.Fatal("legacy replay chose a new sender-named workspace destination")
	}
}

func TestPrivateTransferWorkspaceDownloadsKeepReadableNamesAndSeparateSenders(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	f.senderName = "Alice Smith"
	type incoming struct {
		id, sender, content string
	}
	transfers := []incoming{
		{domain.NewID("file"), "agent_sender", "first reviewed report"},
		{domain.NewID("file"), "agent_sender", "second report with the same filename"},
		{domain.NewID("file"), "agent_another_sender", "same display name on another sender"},
	}
	name := "reviewed report.txt"
	for _, transfer := range transfers {
		notice := f.addFile(transfer.id, []byte(transfer.content))
		file := f.files[transfer.id]
		file.Name, file.OwnerID = name, transfer.sender
		f.files[file.ID] = file
		notice.SenderID, notice.Attachments = file.OwnerID, []domain.FileReference{file.FileReference}
		f.base.messages[notice.ID] = notice
	}
	for range transfers {
		if status, err := f.base.engine.Step(ctx, false); err != nil || status.State != "file-received" {
			t.Fatal("workspace receipt failed", status, err)
		}
	}
	for _, transfer := range transfers {
		want := filepath.Join(f.base.engine.Config.WorkspaceRoot, "downloads", "alice-smith-"+transfer.sender, transfer.id, name)
		var proposal filetransfer.Proposal
		if found, err := f.base.engine.State.Get(ctx, "file-download:receive-"+transfer.id, &proposal); err != nil || !found ||
			proposal.Destination != want || proposal.Receipt == nil {
			t.Fatal("workspace destination lost the original filename, sender or file identity", proposal, err)
		}
		bytes, err := os.ReadFile(want)
		if err != nil || string(bytes) != transfer.content {
			t.Fatal("received file was not actually persisted in the local workspace", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(f.base.engine.Config.StateDir, "transfers")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new downloads were duplicated into opaque private-state staging", err)
	}
	bindings, err := f.base.engine.State.Bindings(ctx)
	if err != nil || len(bindings) != 0 || f.base.executor.invocations.Load() != 0 {
		t.Fatal("receiving local bytes implicitly granted workspace tool execution", bindings, err)
	}
}

func TestPrivateTransferHonorsRecipientChosenPendingDestination(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	notice := f.addFile(domain.NewID("file"), []byte("recipient chose this project-local file"))
	id := notice.Attachments[0].ID
	directory := filepath.Join(t.TempDir(), "my-project", "incoming")
	if err := secure.Dir(directory); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "my-reviewed-copy.txt")
	proposal := filetransfer.Proposal{Destination: destination, File: f.files[id]}
	if err := f.base.engine.State.Put(ctx, "file-download:receive-"+id, proposal); err != nil {
		t.Fatal(err)
	}
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" || f.senderLookups != 0 {
		t.Fatal("receiver replaced the agent's chosen download destination", status, err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "recipient chose this project-local file" {
		t.Fatal("chosen local destination did not contain the verified bytes", err)
	}
}

func TestPrivateTransferWorkspaceKeepsOwnedRootPermissionsAndPrivateDownloadFolders(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	workspace := f.base.engine.Config.WorkspaceRoot
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	notice := f.addFile(domain.NewID("file"), []byte("private file in an owned workspace"))
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" {
		t.Fatal("an existing owned workspace root was incorrectly refused", status, err)
	}
	if info, err := os.Stat(workspace); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("receiving a file changed the preexisting workspace permissions", err)
	}
	path := workspace
	for _, name := range []string{"downloads", "sender-agent_sender", notice.Attachments[0].ID} {
		path = filepath.Join(path, name)
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatal("received file has a nonprivate download-directory ancestor", name, err)
		}
	}
}

func TestPrivateTransferWorkspaceRejectsUnsafeDirectoriesAndNames(t *testing.T) {
	for _, unsafe := range []string{"workspace-link", "workspace-writable", "downloads-link", "downloads-readable",
		"sender-link", "file-directory-link", "final-file-link", "filename-traversal"} {
		t.Run(unsafe, func(t *testing.T) {
			f := newTransferFixture(t)
			ctx := context.Background()
			notice := f.addFile(domain.NewID("file"), []byte("never escape recipient storage"))
			id := notice.Attachments[0].ID
			workspace := f.base.engine.Config.WorkspaceRoot
			directory := filepath.Join(workspace, "downloads", "sender-agent_sender", id)
			if unsafe == "filename-traversal" {
				file := f.files[id]
				file.Name = "../outside.txt"
				notice.Attachments = []domain.FileReference{file.FileReference}
				f.files[id], f.base.messages[notice.ID] = file, notice
			} else {
				target := map[string]string{
					"workspace-link": workspace, "workspace-writable": workspace,
					"downloads-link": filepath.Join(workspace, "downloads"), "downloads-readable": filepath.Join(workspace, "downloads"),
					"sender-link": filepath.Dir(directory), "file-directory-link": directory, "final-file-link": filepath.Join(directory, "private.bin"),
				}[unsafe]
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(unsafe, "-link") {
					outside := t.TempDir()
					if unsafe == "final-file-link" {
						outside = filepath.Join(outside, "existing.txt")
						if err := os.WriteFile(outside, []byte("retain recipient evidence"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(outside, target); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
					mode := os.FileMode(0755)
					if unsafe == "workspace-writable" {
						mode = 0777
					}
					if err := os.Chmod(target, mode); err != nil {
						t.Fatal(err)
					}
				}
			}
			if status, err := f.base.engine.Step(ctx, false); err == nil || status.State == "file-received" || f.notifications != 0 || f.chunks != 0 {
				t.Fatal("unsafe workspace resource was adopted, downloaded or acknowledged", status, err)
			}
		})
	}
}

func TestPrivateTransferUnsafeSenderLabelFallsBackToStableID(t *testing.T) {
	f := newTransferFixture(t)
	f.senderName = "../../sender\n"
	notice := f.addFile(domain.NewID("file"), []byte("an organizational hint is not a path"))
	status, err := f.base.engine.Step(context.Background(), false)
	if err != nil || status.State != "file-received" {
		t.Fatal("legacy unsafe display label prevented a stable-ID-organized receipt", status, err)
	}
	path := filepath.Join(f.base.engine.Config.WorkspaceRoot, "downloads", "agent_sender", notice.Attachments[0].ID, "private.bin")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatal("untrusted sender label escaped the local download directory", err)
	}
}

func TestPrivateTransferFounderAIAlsoReceivesWithoutProviderCalls(t *testing.T) {
	f := newTransferFixture(t)
	f.base.me.Founder = true
	f.addFile(domain.NewID("file"), []byte("designated Founder recipient"))
	status, err := f.base.engine.Step(context.Background(), false)
	if err != nil || status.State != "file-received" || f.notifications != 1 || f.base.executor.checks.Load() != 0 ||
		f.base.executor.invocations.Load() != 0 {
		t.Fatal("designated Founder AI transfer was skipped or became inference", status, err)
	}
}

func TestPrivateTransferAuthorityInterruptionsStopBeforeReceiptAndKeepExactJournal(t *testing.T) {
	for _, change := range []string{"founder-pause", "grant-revoked", "session-admitted", "maintenance", "feed-disconnected"} {
		t.Run(change, func(t *testing.T) {
			f := newTransferFixture(t)
			ctx := context.Background()
			notice := f.addFile(domain.NewID("file"), bytes.Repeat([]byte("x"), domain.FileChunkSize+17))
			id := notice.Attachments[0].ID
			f.base.engine.feedRunning, f.base.engine.feedOnline = true, true
			f.beforeChunk = func(r *http.Request) {
				switch change {
				case "founder-pause":
					f.base.mu.Lock()
					f.base.me.Mode, f.base.me.Revision = "manual", f.base.me.Revision+1
					f.base.me.ModeRequestedBy, f.base.me.ModeReason = "agent_founder", "Founder pauses this transfer"
					f.base.mu.Unlock()
					f.base.engine.invalidateAuthority(domain.Update{Topic: "agent-mode", SourceID: f.base.me.ID})
				case "grant-revoked":
					f.base.mu.Lock()
					f.revoked[id] = true
					f.base.mu.Unlock()
					f.base.engine.invalidateAuthority(domain.Update{Topic: "file", SourceID: id})
				case "session-admitted":
					f.base.mu.Lock()
					f.sessions = []domain.Session{{TargetID: f.base.me.ID, State: "consented", OpenRequested: true}}
					f.base.mu.Unlock()
					f.base.engine.invalidateAuthority(domain.Update{Topic: "session", SourceID: "session_exact"})
				case "maintenance":
					if err := f.base.engine.State.Put(ctx, "maintenance", struct {
						Active bool `json:"active"`
					}{true}); err != nil {
						t.Fatal(err)
					}
					f.base.engine.invalidateAuthority(domain.Update{Topic: "agent-mode", SourceID: f.base.me.ID})
				case "feed-disconnected":
					f.base.engine.setFeedOnline(false)
				}
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
					t.Error("authority interruption did not cancel the in-flight chunk")
				}
			}
			_, err := f.base.engine.Step(ctx, false)
			if err == nil || f.notifications != 0 || len(f.receipts) != 0 || f.base.executor.invocations.Load() != 0 {
				t.Fatal("authority interruption sent a receipt or invoked a provider", err)
			}
			var proposal filetransfer.Proposal
			found, readErr := f.base.engine.State.Get(ctx, "file-download:receive-"+id, &proposal)
			if readErr != nil || !found || proposal.File.ID != id || proposal.Receipt != nil {
				t.Fatal("interrupted transfer lost its exact private journal", proposal, readErr)
			}
		})
	}
}

func TestPrivateTransferPauseAfterLastChunkCannotAcknowledgeUntilFounderResumes(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	notice := f.addFile(domain.NewID("file"), []byte("complete but not yet acknowledged"))
	id := notice.Attachments[0].ID
	f.beforeChunk = func(*http.Request) {
		f.base.mu.Lock()
		f.base.me.Mode, f.base.me.Revision = "manual", 2
		f.base.me.ModeRequestedBy, f.base.me.ModeReason = "agent_founder", "Founder pause before receipt"
		f.base.mu.Unlock()
	}
	_, err := f.base.engine.Step(ctx, false)
	if domain.AsFault(err).Code != domain.Denied || f.notifications != 0 {
		t.Fatal("final receipt ignored a new Founder pause", err)
	}
	var proposal filetransfer.Proposal
	if found, err := f.base.engine.State.Get(ctx, "file-download:receive-"+id, &proposal); err != nil || !found || !proposal.Complete || proposal.Receipt != nil {
		t.Fatal("committed but unacknowledged local file was not retained", proposal, err)
	}
	f.base.me.Mode, f.base.me.Revision, f.base.me.ModeReason = "ai", 3, "Founder explicitly resumes"
	chunks := f.chunks
	if status, err := f.base.engine.Step(ctx, false); err != nil || status.State != "file-received" || f.chunks != chunks || f.notifications != 1 {
		t.Fatal("Founder resumption did not replay the committed file's exact receipt", status, err)
	}
}

func TestPrivateTransfersRespectExistingWorkAndSessionAdmission(t *testing.T) {
	for _, hold := range []string{"work-slot", "maintenance", "session-fence", "server-claim", "live-session"} {
		t.Run(hold, func(t *testing.T) {
			f := newTransferFixture(t)
			ctx := context.Background()
			f.addFile(domain.NewID("file"), []byte("fenced transfer"))
			switch hold {
			case "work-slot":
				lock, err := secure.Acquire(filepath.Join(f.base.engine.Config.StateDir, "work.lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "maintenance", "session-fence":
				if err := f.base.engine.State.Put(ctx, hold, struct {
					Active bool `json:"active"`
				}{true}); err != nil {
					t.Fatal(err)
				}
			case "server-claim":
				f.owned = []domain.Task{{ID: "task_owned"}}
			case "live-session":
				f.sessions = []domain.Session{{TargetID: f.base.me.ID, State: "active"}}
			}
			status, err := f.base.engine.Step(ctx, false)
			if (err == nil && status.State != "session-pending") || f.chunks != 0 || f.notifications != 0 ||
				f.base.executor.checks.Load() != 0 || f.base.executor.invocations.Load() != 0 {
				t.Fatal("file receipt bypassed shared work/session admission", status, err)
			}
		})
	}
}

func TestUnreadableAndOrdinaryAttachmentsDoNotStrandOtherPrivateTransfers(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	unreadable := f.addFile("file_00000000000000000000000000000000", []byte("withdrawn"))
	f.revoked[unreadable.Attachments[0].ID] = true
	ordinary := f.addFile("file_11111111111111111111111111111111", []byte("ordinary retained attachment"))
	file := f.files[ordinary.Attachments[0].ID]
	file.TransferTo = ""
	f.files[file.ID] = file
	deleted := f.addFile("file_22222222222222222222222222222222", []byte("sender deleted"))
	file = f.files[deleted.Attachments[0].ID]
	file.State = "deleted"
	f.files[file.ID] = file
	good := f.addFile("file_33333333333333333333333333333333", []byte("available designated transfer"))
	status, err := f.base.engine.Step(ctx, false)
	if err != nil || status.State != "file-received" || len(status.Blockers) != 2 || f.chunks != 1 || f.notifications != 1 ||
		f.base.messages[good.ID].State != "handled" || f.base.messages[ordinary.ID].State != "pending" {
		t.Fatal("an unavailable/ordinary attachment blocked unrelated designated receipt or was auto-fetched", status, err)
	}
}

func TestPrivateTransferDoesNotOverwriteOrAdoptReplacedStaging(t *testing.T) {
	for _, replaceStage := range []bool{false, true} {
		t.Run(strconv.FormatBool(replaceStage), func(t *testing.T) {
			f := newTransferFixture(t)
			ctx := context.Background()
			notice := f.addFile(domain.NewID("file"), []byte("never overwrite recipient resources"))
			id := notice.Attachments[0].ID
			directory := filepath.Join(f.base.engine.Config.WorkspaceRoot, "downloads", "sender-agent_sender", id)
			if err := secure.Dir(directory); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(directory, "private.bin")
			if replaceStage {
				f.beforeChunk = func(*http.Request) {
					stage := filepath.Join(directory, ".fdn-download-"+domain.Hash("receive-"+id))
					if err := os.Rename(stage, stage+".retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(stage, []byte("replacement evidence"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := os.WriteFile(destination, []byte("existing recipient data"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := f.base.engine.Step(ctx, false)
			if err == nil || f.notifications != 0 {
				t.Fatal("conflicting/replaced local resource produced a receipt", err)
			}
			if !replaceStage {
				saved, err := os.ReadFile(destination)
				if err != nil || string(saved) != "existing recipient data" {
					t.Fatal("automatic transfer overwrote existing recipient data", err)
				}
			} else if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("replaced staging file was adopted as verified destination", err)
			}
		})
	}
}
