package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFilesNativeTransferVerifiedDownloadAndPrivateReceipt(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	sender := messageFixtureClient(t, f.server, f.target.ID)
	receiver := messageFixtureClient(t, f.server, f.delegate.ID)
	founder := messageFixtureClient(t, f.server, f.founder.ID)
	content := bytes.Repeat([]byte("private transfer fixture\n"), domain.FileChunkSize/8)
	req := domain.FileUploadRequest{Command: domain.Command{OperationID: "native-private-transfer"}, Name: "transfer.txt",
		MediaType: "text/plain", Size: int64(len(content)), SHA256: domain.Hash(string(content)),
		TransferTo: f.delegate.ID, DeleteAfterDownload: true}
	file, err := sender.UploadFile(ctx, req, bytes.NewReader(content))
	if err != nil || file.State != "ready" {
		t.Fatal("native chunked transfer", file, err)
	}
	if _, err := Call[domain.SharedFile](ctx, founder, "GET", Path("files", file.ID), nil); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("Founder bypassed private transfer grant", err)
	}
	var downloaded bytes.Buffer
	if _, err := receiver.DownloadFile(ctx, file.ID, &downloaded); err != nil || !bytes.Equal(downloaded.Bytes(), content) {
		t.Fatal("download did not verify exact content", err)
	}
	available, err := Call[domain.SharedFile](ctx, receiver, "GET", Path("files", file.ID), nil)
	if err != nil || available.State != "ready" {
		t.Fatal("read-only download prematurely acknowledged/deleted content", available, err)
	}
	ack := domain.FileDownloadRequest{Command: domain.Command{OperationID: "native-downloaded"}, Size: file.Size, SHA256: file.SHA256}
	receipt, err := Call[domain.FileDownloadReceipt](ctx, receiver, "POST", Path("files", file.ID, "downloaded"), ack)
	if err != nil || !receipt.ContentDeleted || receipt.NotificationID == "" {
		t.Fatal("native verified receipt/deletion", receipt, err)
	}
	replayed, err := Call[domain.FileDownloadReceipt](ctx, receiver, "POST", Path("files", file.ID, "downloaded"), ack)
	if err != nil || replayed != receipt {
		t.Fatal("native lost receipt replay", replayed, err)
	}
	notice, err := Call[domain.Message](ctx, sender, "GET", Path("messages", receipt.NotificationID), nil)
	if err != nil || notice.ResponseRequest || notice.RecipientID != f.target.ID {
		t.Fatal("sender's private durable receipt notification", notice, err)
	}
	if err := receiver.Request(ctx, "GET", Path("files", file.ID, "chunks")+"?offset=0", nil, nil); err == nil || domain.AsFault(err).Code != domain.Prerequisite {
		t.Fatal("deleted transfer remained downloadable", err)
	}
}

func TestFilesNativePublicationAttachmentsAndStrictRawInputs(t *testing.T) {
	f := newAuthenticationAPIFixture(t)
	f.server.health.close()
	ctx := context.Background()
	owner := messageFixtureClient(t, f.server, f.target.ID)
	team := messageFixtureClient(t, f.server, f.delegate.ID)
	data := []byte("intentional published evidence")
	file, err := owner.UploadFile(ctx, domain.FileUploadRequest{Command: domain.Command{OperationID: "published-upload"},
		Name: "evidence.txt", MediaType: "text/plain", Size: int64(len(data)), SHA256: domain.Hash(string(data))}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req := domain.PublicationRequest{Command: domain.Command{OperationID: "not-yet-public"}, Kind: "skill", Scope: "company",
		Title: "Shared skill", Body: "Read [evidence](foundation-file://" + file.ID + ")", AttachmentIDs: []string{file.ID}}
	if err := owner.Request(ctx, "POST", "/v1/publications", req, nil); err == nil || domain.AsFault(err).Code != domain.Denied {
		t.Fatal("private file leaked through team publication", err)
	}
	change := domain.FileChangeRequest{Command: domain.Command{OperationID: "explicit-publication", ExpectedRevision: file.Revision}, Reason: "publish reviewed non-secret evidence"}
	file, err = Call[domain.SharedFile](ctx, owner, "POST", Path("files", file.ID, "publish"), change)
	if err != nil || !file.TeamReadable {
		t.Fatal(err)
	}
	req.OperationID = "shared-skill"
	publication, err := Call[domain.Publication](ctx, owner, "POST", "/v1/publications", req)
	if err != nil || len(publication.Attachments) != 1 {
		t.Fatal("published native attachment", publication, err)
	}
	read, err := Call[domain.PublicationVersion](ctx, team, "GET", Path("publication-versions", publication.VersionID), nil)
	if err != nil || read.ID != publication.VersionID || read.PublicationID != publication.ID {
		t.Fatal("team version read", read, err)
	}
	empty := []string{}
	revised, err := Call[domain.Publication](ctx, owner, "POST", Path("publications", publication.ID, "change"),
		domain.PublicationChange{Command: domain.Command{OperationID: "clear-attachments", ExpectedRevision: publication.Revision},
			Body: stringPointer("Explicitly remove the attachment"), AttachmentIDs: &empty, Reason: "reviewed change"})
	if err != nil || revised.Attachments == nil || len(revised.Attachments) != 0 {
		t.Fatal("native repeated-pointer presence lost explicit empty attachment list", revised, err)
	}
	for _, field := range []string{"owner_id", "team_readable", "revision", "state", "created_at", "id", "downloaded_by"} {
		input := map[string]any{"operation_id": domain.NewID("op"), "name": "forged.txt", "media_type": "text/plain", "size": 0,
			"sha256": domain.Hash(""), field: "forged"}
		envelope, status := f.request(t, f.target, http.MethodPost, "/v1/files", input)
		if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("raw file input accepted protected field", field, status, envelope)
		}
	}
	for _, path := range []string{"/v1/files?actor=" + f.target.ID, "/v1/files?limit=1&limit=2",
		Path("files", file.ID) + "?owner=true", Path("files", file.ID, "chunks") + "?offset=-1",
		"/v1/publications?kind=unknown", "/v1/publications?scope=private", Path("publications", publication.ID) + "?force=true"} {
		if err := team.Request(ctx, "GET", path, nil, nil); err == nil || domain.AsFault(err).Code != domain.Invalid {
			t.Fatal("native file/publication query accepted unknown, repeated or invalid fields", path, err)
		}
	}
	envelope, status := f.request(t, f.target, http.MethodPost, "/v1/messages", map[string]any{
		"operation_id": "forged-file-receipt", "recipient_id": f.delegate.ID, "body": "forged",
		"origin": "service", "kind": "direct", "file_download": map[string]any{"file_id": file.ID, "content_deleted": true}})
	if status != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
		t.Fatal("ordinary message forged server-only file receipt provenance", status, envelope)
	}
}

func stringPointer(value string) *string { return &value }

type fileResponseTransport struct {
	file   domain.SharedFile
	chunk  domain.FileChunk
	writes int
}

func (f *fileResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var value any = f.file
	if strings.Contains(r.URL.Path, "/chunks") {
		value = f.chunk
	}
	if r.Method != "GET" {
		f.writes++
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(Envelope{OK: true, Data: data, Warnings: []string{}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Fdn-Api-Version": {"2"}},
		Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func TestFileDownloadIntegrityFailureNeverSendsReceipt(t *testing.T) {
	expected := []byte("expected")
	file := domain.SharedFile{FileReference: domain.FileReference{ID: "file_fixture", Name: "fixture", Size: int64(len(expected)),
		SHA256: domain.Hash(string(expected))}, State: "ready"}
	for _, chunk := range []domain.FileChunk{
		{FileID: file.ID, Offset: 0, Data: []byte("corrupt!"), SHA256: domain.Hash(string(expected))},
		{FileID: "file_other", Offset: 0, Data: expected, SHA256: file.SHA256},
		{FileID: file.ID, Offset: 1, Data: expected, SHA256: file.SHA256},
		{FileID: file.ID, Offset: 0, Data: []byte("corrupt!"), SHA256: domain.Hash("corrupt!")},
	} {
		transport := &fileResponseTransport{file: file, chunk: chunk}
		client := &Client{BaseURL: "https://fixture", HTTP: &http.Client{Transport: transport}}
		var output bytes.Buffer
		if _, err := client.DownloadFile(context.Background(), file.ID, &output); err == nil || domain.AsFault(err).Code != domain.Internal {
			t.Fatal("unverified bytes appeared successful", err)
		}
		if transport.writes != 0 {
			t.Fatal("failed integrity verification sent a receipt/deletion")
		}
	}
}
