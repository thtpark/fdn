package foundation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func stagedFile(t *testing.T, f *fixture, owner string, content []byte, recipient string, remove bool) (domain.SharedFile, domain.FileUploadRequest) {
	t.Helper()
	ctx := context.Background()
	req := domain.FileUploadRequest{Command: op(), Name: "fixture.bin", MediaType: "application/octet-stream",
		Size: int64(len(content)), SHA256: domain.Hash(string(content)), TransferTo: recipient, DeleteAfterDownload: remove}
	file, err := f.s.BeginFile(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(content); offset += domain.FileChunkSize {
		data := content[offset:min(len(content), offset+domain.FileChunkSize)]
		chunk := domain.FileChunkRequest{Command: op(), Offset: int64(offset), Data: data, SHA256: domain.Hash(string(data))}
		if _, err := f.s.PutFileChunk(ctx, owner, file.ID, chunk); err != nil {
			t.Fatal(err)
		}
	}
	return file, req
}

func readyFile(t *testing.T, f *fixture, owner string, content []byte, recipient string, remove bool) domain.SharedFile {
	t.Helper()
	file, _ := stagedFile(t, f, owner, content, recipient, remove)
	file, err := f.s.FinalizeFile(context.Background(), owner, file.ID, domain.FileChangeRequest{Command: op(), Reason: "explicit upload"})
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func publishFile(t *testing.T, f *fixture, file domain.SharedFile) domain.SharedFile {
	t.Helper()
	req := domain.FileChangeRequest{Command: op(), Reason: "deliberate team publication"}
	req.ExpectedRevision = file.Revision
	published, err := f.s.PublishFile(context.Background(), file.OwnerID, file.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	return published
}

func TestFilesPrivateGrantsNoFounderBypassAndReadOnlyDownloads(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := []byte("PRIVATE fixture bytes")
	file := readyFile(t, f, f.worker.ID, content, "", false)
	for _, actor := range []string{f.founder.ID, f.other.ID} {
		_, err := f.s.File(ctx, actor, file.ID)
		assertCode(t, err, domain.Denied)
		_, err = f.s.FileChunk(ctx, actor, file.ID, 0)
		assertCode(t, err, domain.Denied)
		page, err := f.s.Files(ctx, actor, "", 50, false)
		if err != nil || len(page.Items) != 0 {
			t.Fatal("private file metadata leaked", page, err)
		}
		_, err = f.s.Histories(ctx, actor, file.ID)
		assertCode(t, err, domain.Denied)
		_, err = f.s.GrantFile(ctx, actor, file.ID, domain.FileGrantRequest{Command: op(), AgentID: actor, Reason: "not owner"})
		assertCode(t, err, domain.Denied)
	}
	grant := domain.FileGrantRequest{Command: op(), AgentID: f.other.ID, Reason: "read the exact file"}
	grant.ExpectedRevision = file.Revision
	file, err := f.s.GrantFile(ctx, f.worker.ID, file.ID, grant)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := f.s.FileChunk(ctx, f.other.ID, file.ID, 0)
	if err != nil || !bytes.Equal(chunk.Data, content) {
		t.Fatal("explicit grantee could not read content", err)
	}
	var receipts int
	if err := f.s.db.QueryRow("SELECT count(*) FROM file_downloads").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("read implied a receipt", receipts, err)
	}
	grant.Command, grant.Revoke = op(), true
	grant.ExpectedRevision = file.Revision
	if _, err := f.s.GrantFile(ctx, f.worker.ID, file.ID, grant); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.FileChunk(ctx, f.other.ID, file.ID, 0)
	assertCode(t, err, domain.Denied)
	_, err = f.s.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, domain.FileDownloadRequest{Command: op(), Size: file.Size, SHA256: file.SHA256})
	assertCode(t, err, domain.Denied)
}

func TestPrivateTransferVerifiedReceiptNotificationAndAtomicDeletion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := []byte("only the selected recipient")
	file := readyFile(t, f, f.worker.ID, content, f.other.ID, true)
	_, err := f.s.PublishFile(ctx, f.worker.ID, file.ID, domain.FileChangeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: file.Revision}, Reason: "must not broaden a transfer"})
	assertCode(t, err, domain.Prerequisite)
	_, err = f.s.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, domain.FileDownloadRequest{Command: op(), Size: file.Size, SHA256: domain.Hash("wrong")})
	assertCode(t, err, domain.Invalid)
	if _, err := f.s.FileChunk(ctx, f.other.ID, file.ID, 0); err != nil {
		t.Fatal("invalid receipt deleted transfer", err)
	}
	req := domain.FileDownloadRequest{Command: op(), Size: file.Size, SHA256: file.SHA256}
	var wait sync.WaitGroup
	results := make(chan domain.FileDownloadReceipt, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, err := f.s.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, req)
			if err != nil {
				failures <- err
			} else {
				results <- receipt
			}
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	notification := ""
	for receipt := range results {
		if !receipt.ContentDeleted || receipt.NotificationID == "" ||
			(notification != "" && notification != receipt.NotificationID) {
			t.Fatal("receipt replay duplicated or lost deletion/notification", receipt)
		}
		notification = receipt.NotificationID
	}
	replayed, err := f.s.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, domain.FileDownloadRequest{Command: op(), Size: file.Size, SHA256: file.SHA256})
	if err != nil || replayed.NotificationID != notification {
		t.Fatal("repeated verified download duplicated notification", replayed, err)
	}
	notice, err := f.s.Message(ctx, f.worker.ID, notification)
	if err != nil || notice.SenderID != f.other.ID || notice.ResponseRequest || !strings.Contains(notice.Body, "automatically deleted") {
		t.Fatal("sender did not get a durable non-inference notification", notice, err)
	}
	tombstone, err := f.s.File(ctx, f.other.ID, file.ID)
	if err != nil || tombstone.State != "deleted" || tombstone.SHA256 != file.SHA256 {
		t.Fatal("content deletion erased immutable identity/receipt", tombstone, err)
	}
	var chunks, receipts int
	if err := f.s.db.QueryRow("SELECT count(*) FROM file_chunks WHERE file_id=?", file.ID).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatal("transfer content not removed", chunks, err)
	}
	if err := f.s.db.QueryRow("SELECT count(*) FROM file_downloads WHERE file_id=?", file.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal("receipt not atomic/unique", receipts, err)
	}
	_, err = f.s.FileChunk(ctx, f.other.ID, file.ID, 0)
	assertCode(t, err, domain.Prerequisite)
	_, err = f.s.Message(ctx, f.founder.ID, notification)
	assertCode(t, err, domain.Denied)
}

func TestFileStagingImmutableChunksHashFinalizationAndSmallBoundary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, size := range []int{0, 10*1024*1024 - 1, 10 * 1024 * 1024, 10*1024*1024 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			content := bytes.Repeat([]byte{0x5a}, size)
			file, req := stagedFile(t, f, f.worker.ID, content, "", false)
			replayed, err := f.s.BeginFile(ctx, f.worker.ID, req)
			if err != nil || replayed.ID != file.ID || replayed.UploadedBytes != int64(size) {
				t.Fatal("staged replay lost committed progress", replayed, err)
			}
			finalize := domain.FileChangeRequest{Command: op(), Reason: "verify all staged bytes"}
			ready, err := f.s.FinalizeFile(ctx, f.worker.ID, file.ID, finalize)
			if err != nil || ready.State != "ready" || ready.Size != int64(size) {
				t.Fatal("single storage path changed around 10 MiB", ready, err)
			}
			replayed, err = f.s.FinalizeFile(ctx, f.worker.ID, file.ID, finalize)
			if err != nil || replayed.ID != ready.ID {
				t.Fatal("lost finalization acknowledgment", err)
			}
			_, err = f.s.PutFileChunk(ctx, f.worker.ID, file.ID, domain.FileChunkRequest{Command: op(), Data: []byte{1}, SHA256: domain.Hash(string([]byte{1}))})
			assertCode(t, err, domain.Prerequisite)
		})
	}
	request := domain.FileUploadRequest{Command: op(), Name: "incomplete", MediaType: "application/octet-stream",
		Size: domain.FileChunkSize + 1, SHA256: domain.Hash("not the uploaded bytes")}
	file, err := f.s.BeginFile(ctx, f.worker.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("x"), domain.FileChunkSize)
	chunk := domain.FileChunkRequest{Command: op(), Data: data, SHA256: domain.Hash(string(data))}
	if _, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, chunk); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.FinalizeFile(ctx, f.worker.ID, file.ID, domain.FileChangeRequest{Command: op(), Reason: "incomplete"})
	assertCode(t, err, domain.Prerequisite)
	chunk.Command = op()
	if _, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, chunk); err != nil {
		t.Fatal("exact chunk resume rejected", err)
	}
	chunk.Command, chunk.Data = op(), bytes.Repeat([]byte("y"), domain.FileChunkSize)
	chunk.SHA256 = domain.Hash(string(chunk.Data))
	_, err = f.s.PutFileChunk(ctx, f.worker.ID, file.ID, chunk)
	assertCode(t, err, domain.Conflict)
	last := domain.FileChunkRequest{Command: op(), Offset: domain.FileChunkSize, Data: []byte("z"), SHA256: domain.Hash("z")}
	if _, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, last); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.FinalizeFile(ctx, f.worker.ID, file.ID, domain.FileChangeRequest{Command: op(), Reason: "wrong full hash"})
	assertCode(t, err, domain.Invalid)
}

func TestAttachmentsExplicitTeamPublicationAndGenerationFences(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task := f.ready(t)
	file := readyFile(t, f, f.worker.ID, []byte("deliberate artifact"), "", false)
	ids := []string{file.ID}
	_, err := f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment", Body: "private", AttachmentIDs: &ids})
	assertCode(t, err, domain.Denied)
	message, err := f.s.SendMessage(ctx, f.worker.ID, domain.MessageRequest{Command: op(), RecipientID: f.other.ID,
		Origin: "human", Kind: "direct", Body: "", AttachmentIDs: ids})
	if err != nil || len(message.Attachments) != 1 {
		t.Fatal("ordinary private attachment could not grant its recipient", message, err)
	}
	if _, err := f.s.FileChunk(ctx, f.other.ID, file.ID, 0); err != nil {
		t.Fatal("message recipient did not receive read grant", err)
	}
	_, err = f.s.SendMessage(ctx, f.other.ID, domain.MessageRequest{Command: op(), RecipientID: f.founder.ID,
		Origin: "human", Kind: "direct", Body: "must not re-share", AttachmentIDs: ids})
	assertCode(t, err, domain.Denied)
	file, err = f.s.File(ctx, f.worker.ID, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	file = publishFile(t, f, file)
	unchanged, err := f.s.Task(ctx, f.worker.ID, task.ID)
	if err != nil || unchanged.SpecGeneration != task.SpecGeneration || unchanged.Readiness == nil {
		t.Fatal("file publication changed approved task authority", unchanged, err)
	}
	task = f.change(t, f.worker.ID, task.ID, "edit", func(req *domain.TaskChange) {
		req.AttachmentIDs = &ids
		body := "See ![artifact](foundation-file://" + file.ID + ")"
		req.Description = &body
	})
	if len(task.Attachments) != 1 || task.Readiness != nil || task.SpecGeneration != unchanged.SpecGeneration+1 {
		t.Fatal("attachment edit bypassed specification approval fences", task)
	}
	entry, err := f.s.CreateEntry(ctx, f.other.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "worklog", AttachmentIDs: &ids})
	if err != nil || len(entry.Attachments) != 1 {
		t.Fatal("attachment-only worklog rejected", entry, err)
	}
	_, err = f.s.CreateEntry(ctx, f.worker.ID, task.ID, domain.EntryRequest{Command: op(), Kind: "comment",
		Body: "![unattached](foundation-file://file_not_attached)"})
	assertCode(t, err, domain.Invalid)
	deletion := domain.FileChangeRequest{Command: op(), Reason: "remove retained bytes"}
	deletion.ExpectedRevision = file.Revision
	if _, err := f.s.DeleteFile(ctx, f.worker.ID, file.ID, deletion); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.Entry(ctx, f.founder.ID, entry.ID)
	if err != nil || len(current.Attachments) != 1 || current.Attachments[0].SHA256 != file.SHA256 {
		t.Fatal("deletion erased historical attachment identity", current, err)
	}
}

func TestPublicationsCreatorOwnershipImmutableVersionsReplayAndPaging(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"diary", "skill", "idea"} {
		request := domain.PublicationRequest{Command: op(), Kind: kind, Scope: "company", Title: "Deliberate " + kind, Body: "Original Markdown"}
		p, err := f.s.CreatePublication(ctx, f.worker.ID, request)
		if err != nil {
			t.Fatal(err)
		}

		original := p
		for i := 0; i < 4; i++ {
			body := fmt.Sprintf("Version %d", i)
			change := domain.PublicationChange{Command: op(), Body: &body, Reason: "intentional update"}
			change.ExpectedRevision = p.Revision
			_, err := f.s.ChangePublication(ctx, f.founder.ID, p.ID, change)
			assertCode(t, err, domain.Denied)
			p, err = f.s.ChangePublication(ctx, f.worker.ID, p.ID, change)
			if err != nil {
				t.Fatal(err)
			}
		}
		replayed, err := f.s.CreatePublication(ctx, f.worker.ID, request)
		if err != nil || replayed.VersionID != original.VersionID || replayed.Body != original.Body {
			t.Fatal("exact publication replay drifted to a later version", replayed, err)
		}
		version, err := f.s.PublicationVersion(ctx, f.other.ID, original.VersionID)
		if err != nil || version.ID != original.VersionID || version.PublicationID != original.ID || version.Publication.Body != "Original Markdown" || version.Publication.SHA256 != original.SHA256 {
			t.Fatal("immutable version was rewritten", version, err)
		}
		page, err := f.s.PublicationVersions(ctx, f.other.ID, p.ID, "", 2)
		if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
			t.Fatal("versions pagination", page, err)
		}
		next, err := f.s.PublicationVersions(ctx, f.other.ID, p.ID, page.NextCursor, 2)
		if err != nil || len(next.Items) != 2 || next.Items[0].Publication.Revision >= page.Items[1].Publication.Revision || page.Items[0].ID == page.Items[1].ID {
			t.Fatal("versions paging repeated or reordered revisions", next, err)
		}
		data, err := json.Marshal(original)
		if err != nil || bytes.Contains(data, []byte("workspace")) {
			t.Fatal("publication unexpectedly borrowed private local knowledge", err)
		}
	}
	page, err := f.s.Publications(ctx, f.founder.ID, "skill", "company", "", "", 50, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].CreatedBy != f.worker.ID {
		t.Fatal("team-readable kind/scope filtering", page, err)
	}
}

func TestPublicationPublicObservationWakeAndPrivateFileSeparation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	filter := domain.UpdateFilter{Topics: []string{"publication", "file"}}
	key, waiter, err := f.s.subscribeObservation(f.founder.ID, filter, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.s.unsubscribeObservation(key)
	wake := waiter.wake
	readyFile(t, f, f.worker.ID, []byte("private"), "", false)
	select {
	case <-wake:
		t.Fatal("private file reached a public observer")
	default:
	}
	_, err = f.s.CreatePublication(ctx, f.worker.ID, domain.PublicationRequest{Command: op(), Kind: "idea", Scope: "company", Title: "Explicit idea", Body: "Team-readable"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("publication event omitted its topic/source from scoped observer notification")
	}
}

func TestPublicationDelegatedEditsAreExactProjectRevocableAndCreatorOwned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.s.CreateProject(ctx, f.founder.ID, domain.ProjectRequest{Command: op(), Name: "Publication project"})
	if err != nil {
		t.Fatal(err)
	}
	project, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := f.s.CreatePublication(ctx, f.worker.ID, domain.PublicationRequest{Command: op(), Kind: "idea",
		Scope: "project", TargetID: project.ID, Title: "Explicit edit delegation", Body: "Original", AllowProjectEdits: true})
	if err != nil {
		t.Fatal(err)
	}
	body := "Reviewed by the exact project's current delegate"
	change := domain.PublicationChange{Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: publication.Revision},
		Body: &body, Reason: "explicit delegated edit"}
	publication, err = f.s.ChangePublication(ctx, f.other.ID, publication.ID, change)
	if err != nil || publication.UpdatedBy != f.other.ID || publication.CreatedBy != f.worker.ID {
		t.Fatal("scoped edit changed creator or failed", publication, err)
	}
	withdraw := false
	change.Command = domain.Command{OperationID: op().OperationID, ExpectedRevision: publication.Revision}
	change.AllowProjectEdits = &withdraw
	_, err = f.s.ChangePublication(ctx, f.other.ID, publication.ID, change)
	assertCode(t, err, domain.Denied)
	_, err = f.s.Delegate(ctx, f.founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: project.Revision}, AgentID: f.other.ID, Revoke: true})
	if err != nil {
		t.Fatal(err)
	}
	change.AllowProjectEdits, change.Command = nil, domain.Command{OperationID: op().OperationID, ExpectedRevision: publication.Revision}
	_, err = f.s.ChangePublication(ctx, f.other.ID, publication.ID, change)
	assertCode(t, err, domain.Denied)
	change.Command = domain.Command{OperationID: op().OperationID, ExpectedRevision: publication.Revision}
	change.AllowProjectEdits = &withdraw
	publication, err = f.s.ChangePublication(ctx, f.worker.ID, publication.ID, change)
	if err != nil {
		t.Fatal("creator could not revoke project editing", err)
	}
	change.AllowProjectEdits = nil
	change.Command = domain.Command{OperationID: op().OperationID, ExpectedRevision: publication.Revision}
	_, err = f.s.ChangePublication(ctx, f.founder.ID, publication.ID, change)
	assertCode(t, err, domain.Denied)
}

func TestFileStagingReopenAndAnnouncementGrantsStayDurableAndPrivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := bytes.Repeat([]byte("x"), domain.FileChunkSize+1)
	staged, original := stagedFile(t, f, f.founder.ID, content, "", false)
	var sequence int
	var name, path string
	if err := f.s.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	file, err := reopened.BeginFile(ctx, f.founder.ID, original)
	if err != nil || file.ID != staged.ID || file.UploadedBytes != int64(len(content)) {
		t.Fatal("reopen lost staged immutable declaration/chunks", file, err)
	}
	file, err = reopened.FinalizeFile(ctx, f.founder.ID, file.ID, domain.FileChangeRequest{Command: op(), Reason: "resume the original staged upload"})
	if err != nil {
		t.Fatal(err)
	}
	audience, err := reopened.AnnouncementAudience(ctx, f.founder.ID, "global", "")
	if err != nil {
		t.Fatal(err)
	}
	req := announcementRequest(audience)
	req.Kind, req.AttachmentIDs = "notice", []string{file.ID}
	receipt, err := reopened.Announce(ctx, f.founder.ID, req)
	if err != nil || len(receipt.Deliveries) != 2 {
		t.Fatal("private attachment fan-out failed or conflicted update coalescing", receipt, err)
	}
	for _, delivery := range receipt.Deliveries {
		message, err := reopened.Message(ctx, delivery.AgentID, delivery.MessageID)
		if err != nil || len(message.Attachments) != 1 {
			t.Fatal("announcement lost file references", message, err)
		}
		chunk, err := reopened.FileChunk(ctx, delivery.AgentID, file.ID, 0)
		if err != nil || !bytes.Equal(chunk.Data, content[:domain.FileChunkSize]) {
			t.Fatal("exact announcement receiver did not get read access", err)
		}
	}
	current, err := reopened.File(ctx, f.founder.ID, file.ID)
	if err != nil || current.TeamReadable {
		t.Fatal("private audience grants implied team publication", current, err)
	}
	if err := reopened.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
