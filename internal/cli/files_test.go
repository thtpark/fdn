package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filetransfer"
	"github.com/thtpark/fdn/internal/local"
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
	envelope, code = invoke(t, "agent", []string{"--config", configFile, "file", "download-status", "download-exact"}, nil)
	var proposal filetransfer.Proposal
	if err := json.Unmarshal(envelope.Data, &proposal); err != nil || code != 0 || !envelope.OK ||
		proposal.Destination != destination || !proposal.Complete || proposal.Receipt == nil || acknowledgements != 2 {
		t.Fatal("local download status lost the chosen path or repeated a receipt", envelope, code, err)
	}
}

func TestFileDownloadStatusIsPrivateLocalAndOffline(t *testing.T) {
	ctx := context.Background()
	worker, file, requests := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("download status contacted Foundation", r.Method, r.URL.Path)
	})
	state, err := local.Open(ctx, worker.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	proposal := filetransfer.Proposal{Destination: filepath.Join(t.TempDir(), "chosen", "evidence.zip"),
		File: domain.SharedFile{FileReference: domain.FileReference{ID: "file_fixture", Name: "evidence.zip"}}, Complete: true}
	if err := state.Put(ctx, "file-download:receive-file_fixture", proposal); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(worker.CredentialFile); err != nil {
		t.Fatal(err)
	}
	envelope, code := invoke(t, "agent", []string{"--config", file, "file", "download-status", "receive-file_fixture"}, nil)
	var saved filetransfer.Proposal
	if err := json.Unmarshal(envelope.Data, &saved); err != nil || code != 0 || !envelope.OK ||
		saved.Destination != proposal.Destination || !saved.Complete {
		t.Fatal("offline download status required enrollment/transport or lost its private path", envelope, code, err)
	}
	for _, item := range []struct {
		args []string
		code int
	}{
		{[]string{"missing"}, 3}, {nil, 2}, {[]string{"receive-file_fixture", "extra"}, 2}, {[]string{"../foreign"}, 2},
	} {
		args := append([]string{"--config", file, "file", "download-status"}, item.args...)
		if envelope, code := invoke(t, "agent", args, nil); code != item.code || envelope.OK {
			t.Fatal("missing or malformed local status became success", envelope, code)
		}
	}
	if len(requests()) != 0 {
		t.Fatal("local download journal status contacted a remote service", requests())
	}
}

func TestFileDownloadedCLIRequiresVerifiedDurableLocalCopy(t *testing.T) {
	content := []byte("receipt requires actual receiver-local bytes")
	size, digest := int64(len(content)), domain.Hash(string(content))
	acks := 0
	destination := filepath.Join(t.TempDir(), "retained.bin")
	_, configuration, _ := operationCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/files/file_fixture/downloaded" {
			t.Error("unexpected receipt request", r.Method, r.URL.Path)
			diagnosticsEnvelope(t, w, nil, domain.Fail(domain.Invalid, "unexpected fixture request"))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["path"]; exists {
			t.Error("private local path was disclosed to Foundation")
		}
		if _, exists := body["localFilePath"]; exists {
			t.Error("native local witness was disclosed to Foundation")
		}
		actual, err := os.ReadFile(destination)
		if err != nil || !bytes.Equal(actual, content) {
			t.Error("receipt preceded a usable local file", err)
		}
		acks++
		diagnosticsEnvelope(t, w, domain.FileDownloadReceipt{FileID: "file_fixture", Size: size,
			SHA256: digest, ContentDeleted: true, NotificationID: "message_fixture"}, nil)
	})
	input := domain.FileDownloadRequest{Size: size, SHA256: digest}
	base := []string{"--config", configuration, "--operation-id", "actual-local-copy", "file", "downloaded", "file_fixture"}
	invokeReceipt := func(path string) (bool, int) {
		t.Helper()
		args := slices.Clone(base)
		if path != "" {
			args = append(args, "--path", path)
		}
		args = append(args, "--input", "-")
		envelope, code := invoke(t, "agent", args, input)
		return envelope.OK, code
	}
	if ok, code := invokeReceipt(""); ok || code != 2 || acks != 0 {
		t.Fatal("metadata-only receipt was accepted", ok, code, acks)
	}
	if ok, code := invokeReceipt(destination); ok || code == 0 || acks != 0 {
		t.Fatal("missing local copy was acknowledged", ok, code, acks)
	}
	if err := os.WriteFile(destination, []byte("wrong bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, code := invokeReceipt(destination); ok || code == 0 || acks != 0 {
		t.Fatal("unverified local bytes were acknowledged", ok, code, acks)
	}
	if err := os.WriteFile(destination, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0644); err != nil {
		t.Fatal(err)
	}
	if ok, code := invokeReceipt(destination); ok || code == 0 || acks != 0 {
		t.Fatal("shared local copy was acknowledged", ok, code, acks)
	}
	if err := os.Chmod(destination, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(destination), "borrowed-link")
	if err := os.Symlink(destination, link); err != nil {
		t.Fatal(err)
	}
	if ok, code := invokeReceipt(link); ok || code == 0 || acks != 0 {
		t.Fatal("symlink was acknowledged as retained content", ok, code, acks)
	}
	if ok, code := invokeReceipt(destination); !ok || code != 0 || acks != 1 {
		t.Fatal("verified durable local copy could not acknowledge", ok, code, acks)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if ok, code := invokeReceipt(destination); ok || code == 0 || acks != 1 {
		t.Fatal("receipt replay acknowledged a missing local copy", ok, code, acks)
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
