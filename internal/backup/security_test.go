package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

func manifestVersionFixture(t *testing.T, input, keyFile, output string, version int) {
	t.Helper()
	key, err := secure.Read(keyFile, 8192)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(key)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer encrypted.Close()
	plaintext, err := age.Decrypt(encrypted, identity)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer, err := age.Encrypt(&buffer, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(writer)
	reader := tar.NewReader(plaintext)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(reader, 4<<20))
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "manifest.json" {
			var manifest Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Schema = version
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
		}
		copyHeader := *header
		copyHeader.Size = int64(len(data))
		if err := archive.WriteHeader(&copyHeader); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secure.Create(output, buffer.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsNoncurrentManifestsWithoutPublishingState(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	state := filepath.Join(root, "state")
	store, err := foundation.Open(ctx, filepath.Join(state, "foundation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bundle, err := pki.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Write(state); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "backup.key")
	recipient, err := Keygen(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "current.age")
	server := config.Server{StateDir: state, CertFile: filepath.Join(state, "server.pem"), KeyFile: filepath.Join(state, "server-key.pem")}
	if err := Create(ctx, store, server, original, recipient); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "known-older.age")
	manifestVersionFixture(t, original, keyFile, legacy, 1)
	oldDestination := filepath.Join(root, "old-restored")
	if err := Restore(ctx, legacy, keyFile, oldDestination); err == nil {
		t.Fatal("obsolete backup manifest was accepted")
	}
	if _, err := os.Stat(oldDestination); !os.IsNotExist(err) {
		t.Fatal("obsolete restore published partial state")
	}
	future := filepath.Join(root, "future.age")
	manifestVersionFixture(t, original, keyFile, future, domain.SchemaVersion+1)
	destination := filepath.Join(root, "future-restored")
	if err := Restore(ctx, future, keyFile, destination); err == nil {
		t.Fatal("future manifest accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("incompatible restore published partial state")
	}
}
