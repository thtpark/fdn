package foundation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"

	"github.com/thtpark/fdn/internal/database"
	"github.com/thtpark/fdn/internal/domain"
)

type recordingFilePermissions struct {
	mu       sync.Mutex
	readers  map[string][]uint32
	failNext bool
}

func (p *recordingFilePermissions) Apply(_ context.Context, path string, readers []uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext && len(readers) != 0 {
		p.failNext = false
		return errors.New("fixture OS ACL installation failed")
	}
	key, err := recordedFileKey(path)
	if err != nil {
		return err
	}
	p.readers[key] = slices.Clone(readers)
	return nil
}

func (p *recordingFilePermissions) at(path string) []uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, err := recordedFileKey(path)
	if err != nil {
		return nil
	}
	return slices.Clone(p.readers[key])
}

func recordedFileKey(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("fixture file identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func TestFileStoreUsesOneFilesystemCopyAndExactHostUIDACLs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	content := []byte("Foundation-host file, not a direct peer transfer")
	file := readyFile(t, f, f.worker.ID, content, "", false)
	path, err := f.s.files.Path(file.ID, file.Name)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, content) {
		t.Fatal("Foundation did not retain actual filesystem bytes", err)
	}
	var blobBytes int
	if err := f.s.db.QueryRow("SELECT COALESCE(sum(length(data)),0) FROM file_chunks").Scan(&blobBytes); err != nil || blobBytes != 0 {
		t.Fatal("live payload storage retained a second SQLite route", blobBytes, err)
	}
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4101}) {
		t.Fatal("private owner ACL does not name its existing Foundation-host UID", got)
	}
	grant := domain.FileGrantRequest{Command: op(), AgentID: f.other.ID, Reason: "explicit file reader"}
	grant.ExpectedRevision = file.Revision
	file, err = f.s.GrantFile(ctx, f.worker.ID, file.ID, grant)
	if err != nil || !slices.Equal(f.fileACL.at(path), []uint32{4101, 4102}) {
		t.Fatal("read grant was not projected onto exact host accounts", f.fileACL.at(path), err)
	}
	grant.Command, grant.Revoke = op(), true
	grant.ExpectedRevision = file.Revision
	file, err = f.s.GrantFile(ctx, f.worker.ID, file.ID, grant)
	if err != nil || !slices.Equal(f.fileACL.at(path), []uint32{4101}) {
		t.Fatal("revocation retained an OS reader", f.fileACL.at(path), err)
	}
	file = publishFile(t, f, file)
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4100, 4101, 4102}) {
		t.Fatal("published file did not project the active team", got)
	}
	if err := f.s.BanIdentity(ctx, f.other.ID, "fixture identity revocation"); err != nil {
		t.Fatal(err)
	}
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4100, 4101}) {
		t.Fatal("identity ban retained native file access", got)
	}
	if _, err := f.s.DeleteFile(ctx, f.worker.ID, file.ID, domain.FileChangeRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: file.Revision}, Reason: "explicit deletion",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("logical deletion did not remove the sole live Foundation file", err)
	}
}

func TestFileStoreACLFailureRetainsCommittedProposalAndExactReplay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file := readyFile(t, f, f.worker.ID, []byte("read grant failure fixture"), "", false)
	path, err := f.s.files.Path(file.ID, file.Name)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.FileGrantRequest{Command: op(), AgentID: f.other.ID, Reason: "exact grant whose native response failed"}
	req.ExpectedRevision = file.Revision
	f.fileACL.failNext = true
	if _, err := f.s.GrantFile(ctx, f.worker.ID, file.ID, req); err == nil {
		t.Fatal("failed OS permission installation looked successful")
	}
	if got := f.fileACL.at(path); len(got) != 0 {
		t.Fatal("failure installed speculative readers", got)
	}
	if _, err := f.s.FileChunk(ctx, f.other.ID, file.ID, 0); err == nil || domain.AsFault(err).Code != domain.Unavailable {
		t.Fatal("pending ACL did not fence content", err)
	}
	granted, err := f.s.GrantFile(ctx, f.worker.ID, file.ID, req)
	if err != nil || granted.Revision != file.Revision+1 || !slices.Equal(f.fileACL.at(path), []uint32{4101, 4102}) {
		t.Fatal("exact replay failed to reconcile committed permissions", granted, f.fileACL.at(path), err)
	}
}

func TestFileStoreChunkFailureKeepsExactTailAndSnapshotOnlyCommittedBytes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, tail := bytes.Repeat([]byte{0x61}, domain.FileChunkSize), []byte("last bytes")
	content := append(slices.Clone(first), tail...)
	file, err := f.s.BeginFile(ctx, f.worker.ID, domain.FileUploadRequest{
		Command: op(), Name: "interrupted.bin", MediaType: "application/octet-stream",
		Size: int64(len(content)), SHA256: domain.Hash(string(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, domain.FileChunkRequest{
		Command: op(), Data: first, SHA256: domain.Hash(string(first)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`CREATE TRIGGER fixture_chunk_failure BEFORE INSERT ON file_chunks BEGIN SELECT RAISE(ABORT,'fixture interrupted metadata commit'); END`); err != nil {
		t.Fatal(err)
	}
	req := domain.FileChunkRequest{Command: op(), Offset: domain.FileChunkSize, Data: tail, SHA256: domain.Hash(string(tail))}
	if _, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, req); err == nil {
		t.Fatal("failed metadata commit looked successful")
	}
	if _, err := f.s.db.Exec("DROP TRIGGER fixture_chunk_failure"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "backup", "foundation.sqlite")
	if err := f.s.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenWithFiles(ctx, snapshot, FileStoreOptions{Permissions: &recordingFilePermissions{readers: make(map[string][]uint32)}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	path, err := restored.files.Path(file.ID, file.Name)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(saved, first) {
		t.Fatal("backup adopted uncommitted filesystem tail", len(saved), err)
	}
	progress, err := f.s.PutFileChunk(ctx, f.worker.ID, file.ID, req)
	if err != nil || progress.UploadedBytes != int64(len(content)) {
		t.Fatal("exact uncommitted chunk could not reconcile after failure", progress, err)
	}
	if _, err := f.s.FinalizeFile(ctx, f.worker.ID, file.ID, domain.FileChangeRequest{
		Command: op(), Reason: "finish the exact interrupted upload",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreFounderDeviceActivationAndRevocationProjectAllCurrentUIDs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file := readyFile(t, f, f.worker.ID, []byte("private file granted to Founder actor"), "", false)
	if _, err := f.s.GrantFile(ctx, f.worker.ID, file.ID, domain.FileGrantRequest{
		Command: domain.Command{OperationID: op().OperationID, ExpectedRevision: file.Revision},
		AgentID: f.founder.ID, Reason: "explicit Founder reader, not a bypass",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := f.s.files.Path(file.ID, file.Name)
	if err != nil {
		t.Fatal(err)
	}
	keys := securityPublic(t, "file-reader-device")
	identity, err := f.s.RegisterFounderDevice(ctx, f.founder.ID, domain.RegisterIdentityRequest{Command: op(), Identity: keys.public})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4100, 4101}) {
		t.Fatal("pending device acquired OS access before activation", got)
	}
	identity, _ = securityActivate(t, f.s, identity, keys, 4200)
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4100, 4101, 4200}) {
		t.Fatal("new active Founder device did not receive actor's existing grant", got)
	}
	if _, err := f.s.RevokeFounderDevice(ctx, f.founder.ID, identity.DeviceID, domain.RevokeDeviceRequest{
		Command: op(), Reason: "lost device must lose native file reads",
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.fileACL.at(path); !slices.Equal(got, []uint32{4100, 4101}) {
		t.Fatal("revoked Founder device retained OS read access", got)
	}
}

func TestFileStorePinsSingleLiveRootAcrossConfigurationChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	file := readyFile(t, f, f.worker.ID, []byte("one live content store"), "", false)
	var sequence int
	var name, databasePath string
	if err := f.s.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	other, err := OpenWithFiles(ctx, databasePath, FileStoreOptions{
		Directory: filepath.Join(t.TempDir(), "changed-files"), Permissions: f.fileACL,
	})
	if other != nil {
		other.Close()
		t.Fatal("changed file_dir adopted a second live content root")
	}
	assertCode(t, err, domain.Conflict)
	chunk, err := f.s.FileChunk(ctx, f.worker.ID, file.ID, 0)
	if err != nil || string(chunk.Data) != "one live content store" {
		t.Fatal("rejected reconfiguration damaged the original file", err)
	}
}

func TestSQL10FileMigrationPreservesContentGrantsReceiptsAndRestartEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := bytes.Repeat([]byte("retained SQL9 bytes"), 20000)
	file := readyFile(t, f, f.worker.ID, content, f.other.ID, false)
	req := domain.FileDownloadRequest{Command: op(), Size: file.Size, SHA256: file.SHA256}
	receipt, err := f.s.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot", "foundation.sqlite")
	if err := f.s.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	image, err := database.Open(ctx, snapshot, serverSchemas(), 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		"file_acl_insert", "file_acl_update", "file_acl_grant_insert", "file_acl_grant_delete",
		"file_acl_identity_insert", "file_acl_identity_update", "file_acl_identity_delete", "file_acl_agent_update",
	} {
		if _, err := image.Exec("DROP TRIGGER " + trigger); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := image.Exec("DROP TABLE file_acl_pending; DROP TABLE file_store_state; PRAGMA user_version=9"); err != nil {
		t.Fatal(err)
	}

	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	permissions := &recordingFilePermissions{readers: make(map[string][]uint32)}
	reopened, err := OpenWithFiles(ctx, snapshot, FileStoreOptions{Permissions: permissions})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.File(ctx, f.other.ID, file.ID)
	if err != nil || got.FileReference != file.FileReference || got.Revision != file.Revision {
		t.Fatal("materialization rewrote immutable metadata or grants", got, err)
	}
	var copy []byte
	for offset := int64(0); offset < file.Size; offset += domain.FileChunkSize {
		chunk, err := reopened.FileChunk(ctx, f.other.ID, file.ID, offset)
		if err != nil {
			t.Fatal(err)
		}
		copy = append(copy, chunk.Data...)
	}
	if !bytes.Equal(copy, content) {
		t.Fatal("materialized filesystem bytes differ from retained content")
	}
	replayed, err := reopened.AcknowledgeFileDownload(ctx, f.other.ID, file.ID, req)
	if err != nil || replayed != receipt {
		t.Fatal("materialization lost exact receipt/notification evidence", replayed, receipt, err)
	}
}
