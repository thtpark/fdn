package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/filestore"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/testidentity"
)

func TestEncryptedSnapshotRestoreAndTamper(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	serverRoot := filepath.Join(root, "server")
	store, err := foundation.OpenWithFiles(ctx, filepath.Join(serverRoot, "foundation.sqlite"),
		foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(func(context.Context, string, []uint32) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agent, err := testidentity.Register(ctx, store, "", "founder", 4100)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("encrypted backup must retain the actual external filesystem bytes")
	file, err := store.BeginFile(ctx, agent.ID, domain.FileUploadRequest{
		Command: domain.Command{OperationID: "backup-file-begin"}, Name: "retained.txt",
		MediaType: "text/plain", Size: int64(len(content)), SHA256: domain.Hash(string(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutFileChunk(ctx, agent.ID, file.ID, domain.FileChunkRequest{
		Command: domain.Command{OperationID: "backup-file-chunk"}, Data: content, SHA256: file.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	file, err = store.FinalizeFile(ctx, agent.ID, file.ID, domain.FileChangeRequest{
		Command: domain.Command{OperationID: "backup-file-finalize"}, Reason: "explicit backup fixture upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(serverRoot); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "backup-key")
	recipient, err := Keygen(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Server{StateDir: serverRoot, CertFile: filepath.Join(serverRoot, "server.pem"), KeyFile: filepath.Join(serverRoot, "server-key.pem")}
	output := filepath.Join(root, "backup.age")
	if err := Create(ctx, store, c, output, recipient); err != nil {
		t.Fatal(err)
	}
	recovered := filepath.Join(root, "recovered")
	if err := Restore(ctx, output, keyFile, recovered); err != nil {
		t.Fatal(err)
	}
	restored, err := foundation.OpenWithFiles(ctx, filepath.Join(recovered, "foundation.sqlite"),
		foundation.FileStoreOptions{Permissions: filestore.PermissionFunc(func(_ context.Context, _ string, readers []uint32) error {
			if len(readers) != 0 {
				t.Error("restore reactivated old Foundation-host file access", readers)
			}
			return nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	saved, err := os.ReadFile(filepath.Join(recovered+"-files", file.ID, file.Name))
	if err != nil || !bytes.Equal(saved, content) {
		t.Fatal("encrypted database snapshot omitted external content", err)
	}
	if _, err := restored.Me(ctx, agent.ID); err == nil {
		t.Fatal("restored snapshot resurrected the old identity")
	}
	public, _, _, err := device.Generate("recovered-Founder")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := restored.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: domain.Command{OperationID: "restore-founder-recovery"}, Reason: "explicitly reconcile restored identity", Identity: public})
	if err != nil || identity.AgentID != agent.ID {
		t.Fatal("restoration lost the immutable Founder identity", identity, err)
	}
	restoredConfig, err := config.LoadServer(filepath.Join(recovered, "server.json"))
	if err != nil || restoredConfig.IngressSocket == "" || restoredConfig.JoinSocket == "" {
		t.Fatal("restore recreated a legacy TCP listener", restoredConfig, err)
	}
	if err := Restore(ctx, output, keyFile, recovered); err == nil {
		t.Fatal("overwrote existing state")
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	tampered := filepath.Join(root, "tampered.age")
	if err := secure.Create(tampered, data); err != nil {
		t.Fatal(err)
	}
	badTarget := filepath.Join(root, "bad-restore")
	if err := Restore(ctx, tampered, keyFile, badTarget); err == nil {
		t.Fatal("tampered backup accepted")
	}
	if _, err := os.Stat(badTarget); !os.IsNotExist(err) {
		t.Fatal("partial restore published")
	}
}
