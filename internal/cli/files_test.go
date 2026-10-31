package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFileDownloadCLICommitsBeforeReceiptAndReplaysLostAcknowledgement(t *testing.T) {
	content := []byte("private verified download fixture")
	file := domain.SharedFile{FileReference: domain.FileReference{ID: "file_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name: "download.txt", MediaType: "text/plain", Size: int64(len(content)), SHA256: domain.Hash(string(content))},
		OwnerID: "agent_sender", State: "ready", Revision: 2}
	acknowledgements := 0
	reader := ""
	destination := filepath.Join(t.TempDir(), "verified.txt")
	worker, configFile, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/files/"+file.ID:
			diagnosticsEnvelope(t, w, file, nil)
		case r.Method == "GET" && r.URL.Path == "/v1/files/"+file.ID+"/chunks":
			diagnosticsEnvelope(t, w, domain.FileChunk{FileID: file.ID, Data: content, SHA256: file.SHA256}, nil)
		case r.Method == "POST" && r.URL.Path == "/v1/files/"+file.ID+"/downloaded":
			saved, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(saved, content) {
				t.Error("receipt preceded verified local commit", err)
			}
			info, err := os.Lstat(destination)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Error("download destination is not owner-only", err)
			}
			var req domain.FileDownloadRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OperationID != "download-exact" || req.Size != file.Size || req.SHA256 != file.SHA256 {
				t.Error("receipt lost exact verification/operation", req, err)
			}
			acknowledgements++
			if acknowledgements == 1 {
				diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Unavailable, "fixture lost receipt"))
			} else {
				diagnosticsEnvelope(t, w, domain.FileDownloadReceipt{FileID: file.ID, ReaderID: reader, Size: file.Size, SHA256: file.SHA256,
					ReceivedAt: 1, NotificationID: "message_receipt", ContentDeleted: true}, nil)
			}
		default:
			t.Error("unexpected transfer request", r.Method, r.URL.String())
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "unexpected fixture request"))
		}
	})
	data, err := os.ReadFile(worker.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var credential struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(data, &credential); err != nil {
		t.Fatal(err)
	}
	reader = credential.AgentID
	args := []string{"--config", configFile, "--operation-id", "download-exact", "file", "download", file.ID, "--output", destination}
	envelope, code := invoke(t, "agent", args, nil)
	if code != 7 || envelope.OK || acknowledgements != 1 {
		t.Fatal("lost receipt became successful or auto-retried", code, envelope, acknowledgements)
	}
	envelope, code = invoke(t, "agent", args, nil)
	if code != 0 || !envelope.OK || acknowledgements != 2 {
		t.Fatal("retained download could not reconcile receipt", code, envelope, acknowledgements)
	}
	files, err := os.ReadDir(filepath.Dir(destination))
	if err != nil || len(files) != 1 || strings.HasPrefix(files[0].Name(), ".fdn-") {
		t.Fatal("known staged files were not cleaned", files, err)
	}
}

func TestTransferFilePathsRejectSymlinksAndUnsafeParents(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("do not borrow a symlink"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	pinned, name, err := fileRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if file, err := openTransferSource(pinned, name, false); err == nil {
		file.Close()
		t.Fatal("upload followed a symlink")
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, parentLink); err != nil {
		t.Fatal(err)
	}
	if pinned, _, err := fileRoot(filepath.Join(parentLink, "source")); err == nil {
		pinned.Close()
		t.Fatal("transfer adopted a symlinked parent")
	}
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	if pinned, _, err := fileRoot(filepath.Join(unsafe, "source")); err == nil {
		pinned.Close()
		t.Fatal("transfer adopted a writable parent")
	}
}
