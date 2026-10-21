package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

func TestEncryptedSnapshotRestoreAndTamper(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	serverRoot := filepath.Join(root, "server")
	store, err := foundation.Open(ctx, filepath.Join(serverRoot, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, credential := domain.NewToken(), domain.NewToken()
	if _, err := store.Bootstrap(ctx, domain.Hash(token), time.Now().Unix()+600); err != nil {
		t.Fatal(err)
	}
	agent, err := store.Enroll(ctx, domain.EnrollRequest{Command: domain.Command{OperationID: "enroll"}, InvitationToken: token, CredentialHash: domain.Hash(credential), Name: "founder"})
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
	c := config.Server{StateDir: serverRoot, Listen: "127.0.0.1:8443", CertFile: filepath.Join(serverRoot, "server.pem"), KeyFile: filepath.Join(serverRoot, "server-key.pem")}
	output := filepath.Join(root, "backup.age")
	if err := Create(ctx, store, c, output, recipient); err != nil {
		t.Fatal(err)
	}
	recovered := filepath.Join(root, "recovered")
	if err := Restore(ctx, output, keyFile, recovered); err != nil {
		t.Fatal(err)
	}
	restored, err := foundation.Open(ctx, filepath.Join(recovered, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Authenticate(ctx, credential); err == nil {
		t.Fatal("restored snapshot resurrected an old credential")
	}
	public, _, _, err := device.Generate("recovered Founder")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := restored.RecoverFounderOffline(ctx, domain.RecoverIdentityRequest{Command: domain.Command{OperationID: "restore-founder-recovery"}, Reason: "explicitly reconcile restored identity", Identity: public})
	if err != nil || identity.AgentID != agent.ID {
		t.Fatal("restoration lost the immutable Founder identity", identity, err)
	}
	restoredConfig, err := config.LoadServer(filepath.Join(recovered, "server.json"))
	if err != nil || restoredConfig.Listen != "" || restoredConfig.IngressSocket == "" || restoredConfig.JoinSocket == "" {
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
